package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

const (
	evTagA = "ovpn-ev-a"
	evTagB = "ovpn-ev-b"

	evAlice   = "101"
	evAlicePW = "pw-101"
	evBob     = "102"
	evBobPW   = "pw-102"

	evWait  = 10 * time.Second
	evQuiet = 1500 * time.Millisecond
)

type evLine struct {
	text string
	at   time.Time
}

type evServer struct {
	path string

	mu              sync.Mutex
	ln              net.Listener
	conn            net.Conn
	lines           []evLine
	handshakes      int
	nextKillFailure string
	live            map[string]struct{}
	gone            map[string]struct{}

	writeMu sync.Mutex
}

func newEvServer(t *testing.T, dir string) *evServer {
	t.Helper()
	s := &evServer{path: filepath.Join(dir, "m.sock"), live: make(map[string]struct{}), gone: make(map[string]struct{})}
	s.listen(t)
	t.Cleanup(s.stop)
	return s
}

func (s *evServer) listen(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		t.Fatalf("listen on %s: %v", s.path, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go s.acceptLoop(ln)
}

func (s *evServer) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conn = conn
		s.mu.Unlock()
		s.writeTo(conn, ">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info")
		go s.serve(conn)
	}
}

func (s *evServer) writeTo(conn net.Conn, lines ...string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, line := range lines {
		fmt.Fprintf(conn, "%s\r\n", line)
		s.track(line)
	}
}

func (s *evServer) track(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rest, ok := strings.CutPrefix(line, ">CLIENT:ESTABLISHED,"); ok {
		cid, _, _ := strings.Cut(rest, ",")
		if _, ended := s.gone[cid]; !ended {
			s.live[cid] = struct{}{}
		}
	}
	if rest, ok := strings.CutPrefix(line, ">CLIENT:DISCONNECT,"); ok {
		cid, _, _ := strings.Cut(rest, ",")
		delete(s.live, cid)
		s.gone[cid] = struct{}{}
	}
}

func (s *evServer) liveCIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cids := make([]string, 0, len(s.live))
	for cid := range s.live {
		cids = append(cids, cid)
	}
	sort.Strings(cids)
	return cids
}

func (s *evServer) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		raw, err := r.ReadString('\n')
		if err != nil {
			return
		}
		at := time.Now()
		line := strings.TrimRight(raw, "\r\n")
		if line == "state on" || line == "bytecount 5" {
			s.writeTo(conn, "SUCCESS: "+line)
			if line == "bytecount 5" {
				s.mu.Lock()
				s.handshakes++
				s.mu.Unlock()
			}
			continue
		}
		command, argument, _ := strings.Cut(line, " ")
		s.mu.Lock()
		s.lines = append(s.lines, evLine{text: line, at: at})
		failure := ""
		if command == "client-kill" {
			failure = s.nextKillFailure
			s.nextKillFailure = ""
		}
		s.mu.Unlock()
		switch command {
		case "END":
			s.writeTo(conn, "SUCCESS: client-auth command succeeded")
		case "status":
			s.writeTo(conn, statusThreeReply(s.liveCIDs()...)...)
		case "client-deny":
			s.writeTo(conn, "SUCCESS: client-deny command succeeded")
			cid, _, _ := strings.Cut(argument, " ")
			s.mu.Lock()
			_, wasLive := s.live[cid]
			s.mu.Unlock()
			if wasLive {
				s.writeTo(conn, ">CLIENT:DISCONNECT,"+cid, ">CLIENT:ENV,END")
			}
		case "client-kill":
			switch failure {
			case "error":
				s.writeTo(conn, "ERROR: client-kill command failed")
			case "silent":
			default:
				s.writeTo(conn, "SUCCESS: client-kill command succeeded", ">CLIENT:DISCONNECT,"+argument, ">CLIENT:ENV,END")
			}
		}
	}
}

func (s *evServer) send(t *testing.T, lines ...string) {
	t.Helper()
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		t.Fatal("fake management server has no client connection to write to")
	}
	s.writeTo(conn, lines...)
}

func (s *evServer) drop() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

func (s *evServer) stop() {
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	s.drop()
}

func (s *evServer) failNextKill(mode string) {
	s.mu.Lock()
	s.nextKillFailure = mode
	s.mu.Unlock()
}

func (s *evServer) handshakeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshakes
}

func (s *evServer) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

func (s *evServer) find(from int, match func(string) bool) (evLine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := from; i < len(s.lines); i++ {
		if match(s.lines[i].text) {
			return s.lines[i], true
		}
	}
	return evLine{}, false
}

func (s *evServer) count(from int, match func(string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i := from; i < len(s.lines); i++ {
		if match(s.lines[i].text) {
			n++
		}
	}
	return n
}

func (s *evServer) await(from int, timeout time.Duration, match func(string) bool) (evLine, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if line, ok := s.find(from, match); ok {
			return line, true
		}
		if time.Now().After(deadline) {
			return evLine{}, false
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *evServer) texts(from int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.lines))
	for i := from; i < len(s.lines); i++ {
		out = append(out, s.lines[i].text)
	}
	return out
}

func evIs(text string) func(string) bool {
	return func(line string) bool { return line == text }
}

func evAnswerTo(cid string) func(string) bool {
	return func(line string) bool {
		return strings.HasPrefix(line, "client-auth "+cid+" ") || strings.HasPrefix(line, "client-deny "+cid+" ")
	}
}

type evHarness struct {
	b       *Backend
	servers map[string]*evServer
}

func newEvHarness(t *testing.T, gate func(tag, username string), tags ...string) *evHarness {
	t.Helper()
	b := &Backend{
		instances:    make(map[string]*instanceProcess, len(tags)),
		statsTracker: stats.New(),
		retired:      make(map[string]*tagAccounting),
		carriedIPs:   make(map[string]map[string]map[string]int64),
	}
	h := &evHarness{b: b, servers: make(map[string]*evServer, len(tags))}
	for index, tag := range tags {
		dir, err := os.MkdirTemp("/tmp", "ev")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		server := newEvServer(t, dir)
		instance := newInstanceProcess(&InstanceConfig{Tag: tag}, PKI{}, index, dir, "openvpn", nil, 0)
		authenticate := instance.auth.authenticate
		if gate != nil {
			authenticate = func(username, password string) (string, uint32, bool) {
				email, maxConcurrent, ok := instance.auth.authenticate(username, password)
				gate(tag, username)
				return email, maxConcurrent, ok
			}
		}
		mgmt := newManagementClient(tag, server.path, authenticate)
		ctx, cancel := context.WithTimeout(context.Background(), evWait)
		err = mgmt.Connect(ctx)
		cancel()
		if err != nil {
			t.Fatalf("connect the management client of %s: %v", tag, err)
		}
		t.Cleanup(mgmt.Close)
		instance.mgmt = mgmt
		b.instances[tag] = instance
		b.order = append(b.order, tag)
		h.servers[tag] = server
	}
	return h
}

func (h *evHarness) mgmt(tag string) *ManagementClient {
	return h.b.instances[tag].managementClient()
}

func (h *evHarness) authRequest(t *testing.T, tag, kind, cid, kid, username, password string) string {
	t.Helper()
	server := h.servers[tag]
	from := server.mark()
	server.send(t,
		">CLIENT:"+kind+","+cid+","+kid,
		">CLIENT:ENV,username="+username,
		">CLIENT:ENV,password="+password,
		">CLIENT:ENV,END",
	)
	line, ok := server.await(from, evWait, evAnswerTo(cid))
	if !ok {
		t.Fatalf("%s cid=%s on %s for %q got no client-auth/client-deny answer, saw %v", kind, cid, tag, username, server.texts(from))
	}
	if strings.HasPrefix(line.text, "client-auth ") {
		return "auth"
	}
	return "deny"
}

func (h *evHarness) connect(t *testing.T, tag, cid, username, password string) string {
	t.Helper()
	return h.authRequest(t, tag, "CONNECT", cid, "0", username, password)
}

func (h *evHarness) reauth(t *testing.T, tag, cid, kid, username, password string) string {
	t.Helper()
	return h.authRequest(t, tag, "REAUTH", cid, kid, username, password)
}

func (h *evHarness) establish(t *testing.T, tag, cid, ip string) {
	t.Helper()
	h.servers[tag].send(t, ">CLIENT:ESTABLISHED,"+cid, ">CLIENT:ENV,trusted_ip="+ip, ">CLIENT:ENV,END")
	evEventually(t, "session "+cid+" on "+tag+" established", func() bool { return evSessionOnline(h.mgmt(tag), cid) })
}

func (h *evHarness) login(t *testing.T, tag, cid, username, password string) {
	t.Helper()
	if got := h.connect(t, tag, cid, username, password); got != "auth" {
		t.Fatalf("%q must authenticate cid=%s on %s before any removal, got %s", username, cid, tag, got)
	}
	h.establish(t, tag, cid, "127.0.0.1")
}

func (h *evHarness) online(t *testing.T, email string) int64 {
	t.Helper()
	resp, err := h.b.GetUserOnlineStats(context.Background(), email)
	if err != nil {
		t.Fatalf("GetUserOnlineStats(%s): %v", email, err)
	}
	return resp.GetValue()
}

func evSessionOnline(m *ManagementClient, cid string) bool {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	_, ok := m.online[cid]
	return ok
}

func evSessionKnown(m *ManagementClient, cid string) bool {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	_, ok := m.cidIdentity[cid]
	return ok
}

func evConnected(m *ManagementClient) bool {
	m.connMu.Lock()
	defer m.connMu.Unlock()
	return m.conn != nil
}

func evEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(evWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %s", what, evWait)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func statusThreeReply(cids ...string) []string {
	lines := []string{
		"TITLE\tOpenVPN 2.6.99 fake",
		"TIME\t2026-09-29 00:00:00\t1790640000",
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\tData Channel Cipher",
	}
	for _, cid := range cids {
		lines = append(lines, "CLIENT_LIST\tclient\t127.0.0.1:1194\t\t\t0\t0\t2026-09-29 00:00:00\t1790640000\tuser\t"+cid+"\t0\tAES-256-GCM")
	}
	return append(lines,
		"HEADER\tROUTING_TABLE\tVirtual Address\tCommon Name\tReal Address\tLast Ref\tLast Ref (time_t)",
		"GLOBAL_STATS\tMax bcast/mcast queue length\t0",
		"END",
	)
}

func ovpnUser(email, username, pw string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies:  &common.Proxy{OpenVpn: &common.OpenVpnUser{Username: username, Password: pw}},
	}
}

func authEntries(b *Backend, tag string) map[string]string {
	store := b.instances[tag].auth
	store.mu.RLock()
	defer store.mu.RUnlock()
	out := make(map[string]string, len(store.users))
	for username, entry := range store.users {
		out[username] = entry.email + "/" + entry.password
	}
	return out
}
