package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const fakeOpenVPNEnv = "PASARGUARD_NODE_FAKE_OPENVPN"

func TestMain(m *testing.M) {
	if os.Getenv(fakeOpenVPNEnv) == "1" {
		os.Exit(runFakeOpenVPN(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeOpenVPN struct {
	mu     sync.Mutex
	conn   net.Conn
	onTerm []string
}

func runFakeOpenVPN(args []string) int {
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("OpenVPN 2.6.99 fake")
		return 1
	}
	var configPath string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--config" {
			configPath = args[i+1]
		}
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake openvpn: cannot read config:", err)
		return 1
	}
	var managementPath string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "management" && fields[2] == "unix" {
			managementPath = fields[1]
		}
	}
	if managementPath == "" {
		fmt.Fprintln(os.Stderr, "fake openvpn: no management socket in config")
		return 1
	}

	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM, syscall.SIGINT)

	controlPath := filepath.Join(filepath.Dir(managementPath), "control.sock")
	_ = os.Remove(managementPath)
	_ = os.Remove(controlPath)
	managementListener, err := net.Listen("unix", managementPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake openvpn: management listen:", err)
		return 1
	}
	controlListener, err := net.Listen("unix", controlPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake openvpn: control listen:", err)
		return 1
	}

	f := &fakeOpenVPN{}
	go f.serveManagement(managementListener)
	go f.serveControl(controlListener)

	<-terms

	f.mu.Lock()
	if f.conn != nil {
		for _, line := range f.onTerm {
			fmt.Fprintf(f.conn, "%s\r\n", line)
		}
		f.conn.Close()
	}
	f.mu.Unlock()
	managementListener.Close()
	controlListener.Close()
	return 0
}

func (f *fakeOpenVPN) write(conn net.Conn, line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Fprintf(conn, "%s\r\n", line)
}

func (f *fakeOpenVPN) serveManagement(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		f.write(conn, ">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info")
		go f.answer(conn)
	}
}

func (f *fakeOpenVPN) answer(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		raw, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line := strings.TrimSpace(raw)
		command, _, _ := strings.Cut(line, " ")
		switch {
		case line == "state on" || line == "bytecount 5":
			f.write(conn, "SUCCESS: "+line)
		case command == "client-auth" || command == "client-deny" || command == "client-kill":
			f.write(conn, "SUCCESS: "+command+" command succeeded")
		}
	}
}

func (f *fakeOpenVPN) serveControl(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			r := bufio.NewReader(conn)
			for {
				raw, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line := strings.TrimRight(raw, "\r\n")
				f.mu.Lock()
				if rest, ok := strings.CutPrefix(line, "@term "); ok {
					f.onTerm = append(f.onTerm, rest)
				} else if f.conn != nil {
					fmt.Fprintf(f.conn, "%s\r\n", line)
				}
				f.mu.Unlock()
				fmt.Fprint(conn, "ok\n")
			}
		}()
	}
}

type fakeControl struct {
	conn net.Conn
	r    *bufio.Reader
}

func handoverDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ovh")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func dialFakeControl(t *testing.T, b *Backend, tag string) *fakeControl {
	t.Helper()
	b.mu.RLock()
	instance, ok := b.instances[tag]
	b.mu.RUnlock()
	if !ok {
		t.Fatalf("no instance %q", tag)
	}
	path := filepath.Join(instance.configDir, "control.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			t.Cleanup(func() { conn.Close() })
			return &fakeControl{conn: conn, r: bufio.NewReader(conn)}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake openvpn control socket %s never came up: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *fakeControl) send(t *testing.T, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if _, err := fmt.Fprintf(c.conn, "%s\n", line); err != nil {
			t.Fatalf("control write: %v", err)
		}
		if ack, err := c.r.ReadString('\n'); err != nil || ack != "ok\n" {
			t.Fatalf("control ack for %q: %q %v", line, ack, err)
		}
	}
}

func (c *fakeControl) connect(t *testing.T, cid, username, password string) {
	t.Helper()
	c.send(t,
		">CLIENT:CONNECT,"+cid+",0",
		">CLIENT:ENV,username="+username,
		">CLIENT:ENV,password="+password,
		">CLIENT:ENV,END",
		">CLIENT:ESTABLISHED,"+cid,
		">CLIENT:ENV,trusted_ip=127.0.0.1",
		">CLIENT:ENV,END",
	)
}

func (c *fakeControl) byteCount(t *testing.T, cid string, fromClient, toClient uint64) {
	t.Helper()
	c.send(t, fmt.Sprintf(">BYTECOUNT_CLI:%s,%d,%d", cid, fromClient, toClient))
}

func (c *fakeControl) disconnectOnTerm(t *testing.T, cid string, fromClient, toClient uint64) {
	t.Helper()
	c.send(t,
		"@term >CLIENT:DISCONNECT,"+cid,
		fmt.Sprintf("@term >CLIENT:ENV,bytes_received=%d", fromClient),
		fmt.Sprintf("@term >CLIENT:ENV,bytes_sent=%d", toClient),
		"@term >CLIENT:ENV,END",
	)
}

func waitForUserTotals(t *testing.T, b *Backend, tag, email string, fromClient, toClient uint64) {
	t.Helper()
	b.mu.RLock()
	instance := b.instances[tag]
	b.mu.RUnlock()
	want := ClientStats{Uplink: toClient, Downlink: fromClient}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mgmt := instance.managementClient()
		if mgmt != nil && mgmt.UserStats(email) == want {
			return
		}
		if time.Now().After(deadline) {
			var got ClientStats
			if mgmt != nil {
				got = mgmt.UserStats(email)
			}
			t.Fatalf("%s on %s never reached %+v, got %+v", email, tag, want, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForOnline(t *testing.T, b *Backend, tag, email string, want int) {
	t.Helper()
	b.mu.RLock()
	instance := b.instances[tag]
	b.mu.RUnlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mgmt := instance.managementClient()
		if mgmt != nil && mgmt.OnlineUserCount(email) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s on %s never had %d sessions", email, tag, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func handoverUser(email, password string, tags ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: tags,
		Proxies: &common.Proxy{
			OpenVpn: &common.OpenVpnUser{Username: email, Password: password},
		},
	}
}

func handoverOVConfig(t *testing.T, tags ...string) *Config {
	t.Helper()
	instances := make([]string, 0, len(tags))
	for i, tag := range tags {
		instances = append(instances, fmt.Sprintf(`{"tag":%q,"protocol":"udp","port":%d,"network":"127.0.0.1/24"}`, tag, 21194+i))
	}
	cfg, err := NewConfig(testConfigJSON(strings.Join(instances, ",")))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	return cfg
}

func handoverNodeConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	t.Setenv(fakeOpenVPNEnv, "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return &config.Config{
		OpenVPNExecutablePath:      exe,
		GeneratedConfigPath:        dir,
		LogBufferSize:              4096,
		StatsUpdateIntervalSeconds: 3600,
	}
}

func startFakeOpenVPN(t *testing.T, dir string, users []*common.User, tags ...string) *Backend {
	t.Helper()
	b, err := New(context.Background(), handoverOVConfig(t, tags...), users, handoverNodeConfig(t, dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b
}

func clearOpenVPNHandOver(t *testing.T) {
	t.Helper()
	takeHandOver()
	t.Cleanup(func() { takeHandOver() })
}

func readOpenVPN(t *testing.T, b *Backend, statType common.StatType, name string, reset bool) map[string]int64 {
	t.Helper()
	resp, err := b.GetStats(context.Background(), &common.StatRequest{Type: statType, Name: name, Reset_: reset})
	if err != nil {
		t.Fatalf("GetStats(%v, %q, reset=%v): %v", statType, name, reset, err)
	}
	out := map[string]int64{}
	for _, s := range resp.GetStats() {
		key := s.GetName() + "|" + s.GetLink() + "|" + s.GetType()
		if _, dup := out[key]; dup {
			t.Fatalf("GetStats(%v, reset=%v) reported %s twice", statType, reset, key)
		}
		out[key] = s.GetValue()
	}
	return out
}

func describeOpenVPN(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.FormatInt(m[k], 10))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func expectOpenVPN(t *testing.T, what string, got, want map[string]int64) {
	t.Helper()
	if describeOpenVPN(got) != describeOpenVPN(want) {
		t.Fatalf("%s: got %s, want %s", what, describeOpenVPN(got), describeOpenVPN(want))
	}
}

func ovTraffic(name, link string, up, down int64) map[string]int64 {
	out := map[string]int64{}
	if up > 0 {
		out[name+"|"+link+"|uplink"] = up
	}
	if down > 0 {
		out[name+"|"+link+"|downlink"] = down
	}
	return out
}

func ovMerge(parts ...map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for _, part := range parts {
		for k, v := range part {
			out[k] += v
		}
	}
	return out
}

func expectOpenVPNRead(t *testing.T, what string, b *Backend, users, perTag map[string]int64) {
	t.Helper()
	expectOpenVPN(t, what+": users", readOpenVPN(t, b, common.StatType_UsersStat, "", true), users)
	expectOpenVPN(t, what+": inbounds", readOpenVPN(t, b, common.StatType_Inbounds, "", true), perTag)
	expectOpenVPN(t, what+": outbounds", readOpenVPN(t, b, common.StatType_Outbounds, "", true), perTag)
}

func expectOpenVPNEmpty(t *testing.T, what string, b *Backend) {
	t.Helper()
	expectOpenVPNRead(t, what, b, map[string]int64{}, map[string]int64{})
}

const (
	ovTagA     = "ovpn-ho-a"
	ovTagB     = "ovpn-ho-b"
	ovAlice    = "alice@example.com"
	ovBob      = "bob@example.com"
	ovPassword = "pw"
)

func TestOpenVPNStartCarriesUnreadTrafficExactlyOnce(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)
	users := []*common.User{handoverUser(ovAlice, ovPassword, ovTagA), handoverUser(ovBob, ovPassword, ovTagA)}

	first := startFakeOpenVPN(t, dir, users, ovTagA)
	ctl := dialFakeControl(t, first, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 300, 1200)
	ctl.disconnectOnTerm(t, "1", 350, 1500)
	ctl.connect(t, "2", ovBob, ovPassword)
	ctl.byteCount(t, "2", 40, 60)
	waitForUserTotals(t, first, ovTagA, ovAlice, 300, 1200)
	waitForUserTotals(t, first, ovTagA, ovBob, 40, 60)
	first.Shutdown()

	second := startFakeOpenVPN(t, dir, users, ovTagA)

	expectOpenVPN(t, "a peek right after the Start",
		readOpenVPN(t, second, common.StatType_UserStat, ovAlice, false),
		ovTraffic(ovAlice, ovTagA, 350, 1500))
	expectOpenVPNRead(t, "first read after the Start", second,
		ovMerge(ovTraffic(ovAlice, ovTagA, 350, 1500), ovTraffic(ovBob, ovTagA, 40, 60)),
		ovTraffic(ovTagA, ovTagA, 390, 1560))
	expectOpenVPNEmpty(t, "second read after the Start", second)

	ctl = dialFakeControl(t, second, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 7, 11)
	waitForUserTotals(t, second, ovTagA, ovAlice, 7, 11)
	expectOpenVPNRead(t, "read after new growth", second,
		ovTraffic(ovAlice, ovTagA, 7, 11),
		ovTraffic(ovTagA, ovTagA, 7, 11))
	expectOpenVPNEmpty(t, "read after the growth was reported", second)
}

func TestOpenVPNTwoStartsWithoutAReadCarryEveryByteOnce(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)
	users := []*common.User{handoverUser(ovAlice, ovPassword, ovTagA), handoverUser(ovBob, ovPassword, ovTagA)}

	first := startFakeOpenVPN(t, dir, users, ovTagA)
	ctl := dialFakeControl(t, first, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 100, 200)
	waitForUserTotals(t, first, ovTagA, ovAlice, 100, 200)
	first.Shutdown()

	second := startFakeOpenVPN(t, dir, users, ovTagA)
	ctl = dialFakeControl(t, second, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 10, 20)
	ctl.connect(t, "2", ovBob, ovPassword)
	ctl.byteCount(t, "2", 1, 2)
	ctl.disconnectOnTerm(t, "2", 3, 4)
	waitForUserTotals(t, second, ovTagA, ovAlice, 10, 20)
	waitForUserTotals(t, second, ovTagA, ovBob, 1, 2)
	second.Shutdown()

	third := startFakeOpenVPN(t, dir, users, ovTagA)
	expectOpenVPNRead(t, "first read on the third backend", third,
		ovMerge(ovTraffic(ovAlice, ovTagA, 110, 220), ovTraffic(ovBob, ovTagA, 3, 4)),
		ovTraffic(ovTagA, ovTagA, 113, 224))
	expectOpenVPNEmpty(t, "second read on the third backend", third)
}

func TestOpenVPNFailedNewPutsTheHandOverBack(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)
	users := []*common.User{handoverUser(ovAlice, ovPassword, ovTagA, ovTagB)}

	first := startFakeOpenVPN(t, dir, users, ovTagA)
	ctl := dialFakeControl(t, first, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 500, 9000)
	waitForUserTotals(t, first, ovTagA, ovAlice, 500, 9000)
	first.Shutdown()

	blocker := filepath.Join(dir, "openvpn", "instance-1")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block the second instance: %v", err)
	}
	if failed, err := New(context.Background(), handoverOVConfig(t, ovTagA, ovTagB), users, handoverNodeConfig(t, dir)); err == nil {
		failed.Shutdown()
		t.Fatal("New must fail while the second instance cannot write its config")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatalf("unblock: %v", err)
	}

	second := startFakeOpenVPN(t, dir, users, ovTagA)
	expectOpenVPNRead(t, "first read after the failed Start", second,
		ovTraffic(ovAlice, ovTagA, 500, 9000),
		ovTraffic(ovTagA, ovTagA, 500, 9000))
	expectOpenVPNEmpty(t, "second read after the failed Start", second)
}

func TestOpenVPNStartReportsAbsentUsersAndTagsOnceThenForgetsThem(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)

	first := startFakeOpenVPN(t, dir, []*common.User{
		handoverUser(ovAlice, ovPassword, ovTagA, ovTagB),
		handoverUser(ovBob, ovPassword, ovTagA),
	}, ovTagA, ovTagB)
	ctlA := dialFakeControl(t, first, ovTagA)
	ctlB := dialFakeControl(t, first, ovTagB)
	ctlA.connect(t, "1", ovAlice, ovPassword)
	ctlA.byteCount(t, "1", 10, 20)
	ctlA.connect(t, "2", ovBob, ovPassword)
	ctlA.byteCount(t, "2", 5, 6)
	ctlB.connect(t, "1", ovAlice, ovPassword)
	ctlB.byteCount(t, "1", 30, 40)
	waitForUserTotals(t, first, ovTagA, ovAlice, 10, 20)
	waitForUserTotals(t, first, ovTagA, ovBob, 5, 6)
	waitForUserTotals(t, first, ovTagB, ovAlice, 30, 40)
	first.Shutdown()

	second := startFakeOpenVPN(t, dir, []*common.User{handoverUser(ovAlice, ovPassword, ovTagA)}, ovTagA)

	expectOpenVPN(t, "a peek at the gone tag",
		readOpenVPN(t, second, common.StatType_Inbound, ovTagB, false),
		ovTraffic(ovTagB, ovTagB, 30, 40))
	expectOpenVPNRead(t, "first read after the Start", second,
		ovMerge(ovTraffic(ovAlice, ovTagA, 10, 20), ovTraffic(ovAlice, ovTagB, 30, 40), ovTraffic(ovBob, ovTagA, 5, 6)),
		ovMerge(ovTraffic(ovTagA, ovTagA, 15, 26), ovTraffic(ovTagB, ovTagB, 30, 40)))
	expectOpenVPNEmpty(t, "second read after the Start", second)

	keys := second.statsTracker.Keys()
	sort.Strings(keys)
	if strings.Join(keys, ",") != statsKey(ovTagA, ovAlice) {
		t.Fatalf("absent users and tags must be forgotten once reported, tracker keys: %v", keys)
	}
	for _, statType := range []common.StatType{common.StatType_Inbound, common.StatType_Outbound} {
		if _, err := second.GetStats(context.Background(), &common.StatRequest{Type: statType, Name: ovTagB}); err == nil {
			t.Fatalf("%v of a gone tag must be unknown once it was reported", statType)
		}
	}
}

func TestOpenVPNStartKeepsOnlineAndIPAnswersLikeWireGuard(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)
	ctx := context.Background()

	first := startFakeOpenVPN(t, dir, []*common.User{
		handoverUser(ovAlice, ovPassword, ovTagA),
		handoverUser(ovBob, ovPassword, ovTagA),
	}, ovTagA)
	ctl := dialFakeControl(t, first, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 50, 60)
	ctl.connect(t, "2", ovBob, ovPassword)
	ctl.byteCount(t, "2", 5, 6)
	waitForUserTotals(t, first, ovTagA, ovAlice, 50, 60)
	waitForUserTotals(t, first, ovTagA, ovBob, 5, 6)
	first.Shutdown()

	second := startFakeOpenVPN(t, dir, []*common.User{handoverUser(ovAlice, ovPassword, ovTagA)}, ovTagA)

	for _, email := range []string{ovAlice, ovBob} {
		online, err := second.GetUserOnlineStats(ctx, email)
		if err != nil {
			t.Fatalf("GetUserOnlineStats(%s): %v", email, err)
		}
		if online.GetValue() != 0 {
			t.Fatalf("%s has no session on the new process and must be offline, got %d", email, online.GetValue())
		}
	}
	aliceIPs, err := second.GetUserOnlineIpListStats(ctx, ovAlice)
	if err != nil {
		t.Fatalf("GetUserOnlineIpListStats(alice): %v", err)
	}
	if _, ok := aliceIPs.GetIps()["127.0.0.1"]; !ok || len(aliceIPs.GetIps()) != 1 {
		t.Fatalf("a still configured user keeps its last known ip across the Start, got %v", aliceIPs.GetIps())
	}
	bobIPs, err := second.GetUserOnlineIpListStats(ctx, ovBob)
	if err != nil {
		t.Fatalf("GetUserOnlineIpListStats(bob): %v", err)
	}
	if len(bobIPs.GetIps()) != 0 {
		t.Fatalf("a user absent from the Start must not keep its ips, got %v", bobIPs.GetIps())
	}

	expectOpenVPN(t, "carried traffic of the live sessions",
		readOpenVPN(t, second, common.StatType_UsersStat, "", true),
		ovMerge(ovTraffic(ovAlice, ovTagA, 50, 60), ovTraffic(ovBob, ovTagA, 5, 6)))

	ctl = dialFakeControl(t, second, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	waitForOnline(t, second, ovTagA, ovAlice, 1)
	online, err := second.GetUserOnlineStats(ctx, ovAlice)
	if err != nil {
		t.Fatalf("GetUserOnlineStats: %v", err)
	}
	if online.GetValue() != 1 {
		t.Fatalf("alice reconnected to the new process and must be online, got %d", online.GetValue())
	}
}

func TestOpenVPNStoppedBackendCannotReportWhatItHandedOver(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)
	users := []*common.User{handoverUser(ovAlice, ovPassword, ovTagA)}

	first := startFakeOpenVPN(t, dir, users, ovTagA)
	ctl := dialFakeControl(t, first, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 40, 400)
	waitForUserTotals(t, first, ovTagA, ovAlice, 40, 400)
	if got := readOpenVPN(t, first, common.StatType_UserStat, ovAlice, false); len(got) == 0 {
		t.Fatal("the running backend must see alice's traffic")
	}
	first.Shutdown()

	for _, statType := range []common.StatType{common.StatType_UsersStat, common.StatType_Inbounds, common.StatType_Outbounds} {
		resp, err := first.GetStats(context.Background(), &common.StatRequest{Type: statType, Reset_: true})
		if err == nil && len(resp.GetStats()) != 0 {
			t.Fatalf("a stopped backend reported %v for %v after handing its accounting over", resp.GetStats(), statType)
		}
	}

	second := startFakeOpenVPN(t, dir, users, ovTagA)
	expectOpenVPNRead(t, "first read on the new backend", second,
		ovTraffic(ovAlice, ovTagA, 40, 400),
		ovTraffic(ovTagA, ovTagA, 40, 400))
}

func TestOpenVPNDoubleShutdownKeepsTheHandOver(t *testing.T) {
	clearOpenVPNHandOver(t)
	dir := handoverDir(t)
	users := []*common.User{handoverUser(ovAlice, ovPassword, ovTagA)}

	first := startFakeOpenVPN(t, dir, users, ovTagA)
	ctl := dialFakeControl(t, first, ovTagA)
	ctl.connect(t, "1", ovAlice, ovPassword)
	ctl.byteCount(t, "1", 8, 9)
	waitForUserTotals(t, first, ovTagA, ovAlice, 8, 9)
	first.Shutdown()
	first.Shutdown()

	second := startFakeOpenVPN(t, dir, users, ovTagA)
	expectOpenVPN(t, "users after a repeated Shutdown",
		readOpenVPN(t, second, common.StatType_UsersStat, "", true),
		ovTraffic(ovAlice, ovTagA, 8, 9))
}
