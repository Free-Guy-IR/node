package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const (
	revokeCutBound       = 3 * time.Second
	revokeWarmupBytes    = 256 * 1024
	revokeWarmupTimeout  = 15 * time.Second
	revokeBystanderProbe = 1500 * time.Millisecond
	revokeReconnectProbe = 2 * time.Second
)

type revokeCase struct {
	name       string
	tag        string
	protocol   string
	network    string
	xhttpMode  string
	mux        bool
	idleBefore bool
}

func revokeCases() []revokeCase {
	return []revokeCase{
		{name: "vless tcp", tag: "revoke-vless-tcp", protocol: Vless, network: "tcp"},
		{name: "vmess tcp", tag: "revoke-vmess-tcp", protocol: Vmess, network: "tcp"},
		{name: "trojan tcp", tag: "revoke-trojan-tcp", protocol: Trojan, network: "tcp"},
		{name: "vless xhttp packet-up", tag: "revoke-vless-xhttp", protocol: Vless, network: "xhttp", xhttpMode: "packet-up"},
		{name: "vless xhttp stream-one", tag: "revoke-vless-xhttp-one", protocol: Vless, network: "xhttp", xhttpMode: "stream-one"},
		{name: "trojan xhttp packet-up", tag: "revoke-trojan-xhttp", protocol: Trojan, network: "xhttp", xhttpMode: "packet-up"},
		{name: "vless tcp mux", tag: "revoke-vless-mux", protocol: Vless, network: "tcp", mux: true},
		{name: "vless tcp idle mux", tag: "revoke-vless-mux-idle", protocol: Vless, network: "tcp", mux: true, idleBefore: true},
	}
}

func revokeTestUser(email string, inbounds []string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Vmess:  &common.Vmess{Id: uuid.New().String()},
			Vless:  &common.Vless{Id: uuid.New().String()},
			Trojan: &common.Trojan{Password: uuid.New().String()},
		},
	}
}

func withoutInbounds(user *common.User) *common.User {
	return &common.User{
		Email:    user.GetEmail(),
		Inbounds: nil,
		Proxies:  user.GetProxies(),
	}
}

type streamingTarget struct {
	listener net.Listener
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func startStreamingTarget(t *testing.T) *streamingTarget {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	target := &streamingTarget{listener: listener, conns: make(map[net.Conn]struct{})}
	go target.serve()
	t.Cleanup(target.close)
	return target
}

func (s *streamingTarget) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func (s *streamingTarget) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go s.stream(conn)
	}
}

func (s *streamingTarget) stream(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	chunk := bytes.Repeat([]byte{'x'}, 16*1024)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := conn.Write(chunk); err != nil {
			return
		}
	}
}

func (s *streamingTarget) close() {
	_ = s.listener.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		_ = conn.Close()
	}
}

type streamProbe struct {
	conn     net.Conn
	received atomic.Int64
	ended    chan struct{}
	endedAt  time.Time
}

func openStreamProbe(t *testing.T, port int) *streamProbe {
	t.Helper()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial client inbound %d: %v", port, err)
	}

	probe := &streamProbe{conn: conn, ended: make(chan struct{})}
	go probe.read()
	t.Cleanup(probe.close)
	return probe
}

func (p *streamProbe) read() {
	buffer := make([]byte, 64*1024)
	for {
		n, err := p.conn.Read(buffer)
		p.received.Add(int64(n))
		if err != nil {
			p.endedAt = time.Now()
			close(p.ended)
			return
		}
	}
}

func (p *streamProbe) close() {
	_ = p.conn.Close()
}

func (p *streamProbe) isOpen() bool {
	select {
	case <-p.ended:
		return false
	default:
		return true
	}
}

func (p *streamProbe) waitForBytes(t *testing.T, label string, want int64) {
	t.Helper()

	deadline := time.Now().Add(revokeWarmupTimeout)
	for time.Now().Before(deadline) {
		if p.received.Load() >= want {
			return
		}
		if !p.isOpen() {
			t.Fatalf("%s stream ended after %d bytes before it carried %d", label, p.received.Load(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s stream carried only %d of %d bytes in %s", label, p.received.Load(), want, revokeWarmupTimeout)
}

func revokeServerInbound(c revokeCase, port int) map[string]any {
	settings := map[string]any{"clients": []any{}}
	if c.protocol == Vless {
		settings["decryption"] = "none"
	}

	stream := map[string]any{"network": c.network}
	if c.network == "xhttp" {
		stream["xhttpSettings"] = map[string]any{"path": "/" + c.tag}
	}

	return map[string]any{
		"tag":            c.tag,
		"listen":         "127.0.0.1",
		"port":           port,
		"protocol":       c.protocol,
		"settings":       settings,
		"streamSettings": stream,
	}
}

func revokeClientOutbound(c revokeCase, tag string, serverPort int, user *common.User) map[string]any {
	var settings map[string]any
	switch c.protocol {
	case Vless:
		settings = map[string]any{"vnext": []any{map[string]any{
			"address": "127.0.0.1",
			"port":    serverPort,
			"users":   []any{map[string]any{"id": user.GetProxies().GetVless().GetId(), "encryption": "none"}},
		}}}
	case Vmess:
		settings = map[string]any{"vnext": []any{map[string]any{
			"address": "127.0.0.1",
			"port":    serverPort,
			"users":   []any{map[string]any{"id": user.GetProxies().GetVmess().GetId(), "security": "auto"}},
		}}}
	case Trojan:
		settings = map[string]any{"servers": []any{map[string]any{
			"address":  "127.0.0.1",
			"port":     serverPort,
			"password": user.GetProxies().GetTrojan().GetPassword(),
		}}}
	}

	stream := map[string]any{"network": c.network}
	if c.network == "xhttp" {
		stream["xhttpSettings"] = map[string]any{"path": "/" + c.tag, "mode": c.xhttpMode}
	}

	outbound := map[string]any{
		"tag":            tag,
		"protocol":       c.protocol,
		"settings":       settings,
		"streamSettings": stream,
	}
	if c.mux {
		outbound["mux"] = map[string]any{"enabled": true, "concurrency": 8}
	}
	return outbound
}

type revokeHarness struct {
	backend       *Xray
	target        *streamingTarget
	bystander     *common.User
	victims       map[string]*common.User
	victimPort    map[string]int
	bystanderPort map[string]int
}

func startRevokeHarness(t *testing.T, cases []revokeCase) *revokeHarness {
	t.Helper()

	target := startStreamingTarget(t)

	tags := make([]string, 0, len(cases))
	serverPorts := make(map[string]int, len(cases))
	serverInbounds := make([]any, 0, len(cases))
	for _, c := range cases {
		tags = append(tags, c.tag)
		serverPorts[c.tag] = netutil.FindFreePort()
		serverInbounds = append(serverInbounds, revokeServerInbound(c, serverPorts[c.tag]))
	}

	serverConfig := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  serverInbounds,
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	}
	rawServer, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}

	xrayConfig, err := NewConfig(string(rawServer), nil)
	if err != nil {
		t.Fatal(err)
	}

	harness := &revokeHarness{
		target:        target,
		bystander:     revokeTestUser("revoke-bystander", tags),
		victims:       make(map[string]*common.User, len(cases)),
		victimPort:    make(map[string]int, len(cases)),
		bystanderPort: make(map[string]int, len(cases)),
	}

	users := []*common.User{harness.bystander}
	for _, c := range cases {
		victim := revokeTestUser("revoke-victim-"+c.tag, []string{c.tag})
		harness.victims[c.tag] = victim
		users = append(users, victim)
	}

	cfg := &config.Config{
		XrayExecutablePath:  executablePath,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	}

	backend, err := New(context.Background(), xrayConfig, users, netutil.FindFreePort(), netutil.FindFreePort(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)
	harness.backend = backend

	clientInbounds := make([]any, 0, 2*len(cases))
	clientOutbounds := make([]any, 0, 2*len(cases))
	clientRules := make([]any, 0, 2*len(cases))
	for _, c := range cases {
		for _, role := range []string{"victim", "bystander"} {
			user := harness.bystander
			ports := harness.bystanderPort
			if role == "victim" {
				user = harness.victims[c.tag]
				ports = harness.victimPort
			}

			port := netutil.FindFreePort()
			ports[c.tag] = port
			inTag := "in-" + role + "-" + c.tag
			outTag := "out-" + role + "-" + c.tag

			clientInbounds = append(clientInbounds, map[string]any{
				"tag":      inTag,
				"listen":   "127.0.0.1",
				"port":     port,
				"protocol": "dokodemo-door",
				"settings": map[string]any{"address": "127.0.0.1", "port": target.port(), "network": "tcp"},
			})
			clientOutbounds = append(clientOutbounds, revokeClientOutbound(c, outTag, serverPorts[c.tag], user))
			clientRules = append(clientRules, map[string]any{
				"type":        "field",
				"inboundTag":  []string{inTag},
				"outboundTag": outTag,
			})
		}
	}

	clientConfig := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{
			"uplinkOnly":   0,
			"downlinkOnly": 0,
		}}},
		"inbounds":  clientInbounds,
		"outbounds": clientOutbounds,
		"routing":   map[string]any{"rules": clientRules},
	}
	startRevokeClient(t, clientConfig)

	for _, c := range cases {
		waitForListener(t, harness.victimPort[c.tag])
		waitForListener(t, harness.bystanderPort[c.tag])
	}

	return harness
}

func startRevokeClient(t *testing.T, clientConfig map[string]any) {
	t.Helper()

	raw, err := json.Marshal(clientConfig)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	configFile := filepath.Join(dir, "client.json")
	if err := os.WriteFile(configFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	clientBinary := filepath.Join(dir, "xray-client")
	binary, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read xray binary: %v", err)
	}
	if err := os.WriteFile(clientBinary, binary, 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	cmd := exec.Command(clientBinary, "run", "-c", configFile)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+assetsPath)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start xray client: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("xray client output:\n%s", output.String())
		}
	})
}

func waitForListener(t *testing.T, port int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("client inbound on port %d never started listening", port)
}

func TestRemovedUserEstablishedSessionsAreClosed(t *testing.T) {
	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}

	cases := revokeCases()
	harness := startRevokeHarness(t, cases)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			victimUser := harness.victims[c.tag]

			bystander := openStreamProbe(t, harness.bystanderPort[c.tag])
			victim := openStreamProbe(t, harness.victimPort[c.tag])
			bystander.waitForBytes(t, "bystander", revokeWarmupBytes)
			victim.waitForBytes(t, "victim", revokeWarmupBytes)

			if c.idleBefore {
				victim.close()
				<-victim.ended
				time.Sleep(300 * time.Millisecond)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			removedAt := time.Now()
			if err := harness.backend.SyncUser(ctx, withoutInbounds(victimUser)); err != nil {
				t.Fatalf("remove victim: %v", err)
			}

			if !c.idleBefore {
				receivedAtRemoval := victim.received.Load()
				select {
				case <-victim.ended:
					t.Logf("victim stream closed %s after removal", victim.endedAt.Sub(removedAt).Round(time.Millisecond))
				case <-time.After(revokeCutBound):
					t.Fatalf("victim stream still open %s after removal; it carried %d more bytes after the user was removed",
						revokeCutBound, victim.received.Load()-receivedAtRemoval)
				}
			}

			retry := openStreamProbe(t, harness.victimPort[c.tag])
			time.Sleep(revokeReconnectProbe)
			if got := retry.received.Load(); got != 0 {
				t.Fatalf("removed victim still got %d bytes through a new stream", got)
			}

			if err := harness.backend.SyncUser(ctx, harness.bystander); err != nil {
				t.Fatalf("resync unchanged bystander: %v", err)
			}

			before := bystander.received.Load()
			time.Sleep(revokeBystanderProbe)
			if !bystander.isOpen() {
				t.Fatalf("bystander stream on the same inbound was closed after %d bytes", bystander.received.Load())
			}
			if after := bystander.received.Load(); after-before < revokeWarmupBytes/4 {
				t.Fatalf("bystander stream stalled: only %d bytes in %s", after-before, revokeBystanderProbe)
			}
		})
	}
}
