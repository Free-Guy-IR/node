package l2tp

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pasarguard/node/common"
)

func TestNewConfigAppliesDefaultsAndValidates(t *testing.T) {
	cfg, err := NewConfig(`{"inbound_tag":"l2tp-main","server_addr":"1.2.3.4","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LocalIP != "10.31.0.1" {
		t.Fatalf("local ip = %q", cfg.LocalIP)
	}
	if len(cfg.DNS) != 2 || len(cfg.IKEProposals) == 0 || len(cfg.ESPProposals) == 0 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	for _, p := range append(append([]string(nil), cfg.IKEProposals...), cfg.ESPProposals...) {
		if strings.Contains(p, "3des") {
			t.Fatalf("3des must not be a default proposal: %v", p)
		}
	}
	legacy, err := NewConfig(`{"inbound_tag":"l","pool":"10.31.0.0/24","psk":"correct-horse-battery","legacy_clients":true}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.Join(legacy.IKEProposals, ","), "3des-sha1-modp1024") {
		t.Fatalf("legacy_clients must append the 3des proposal: %v", legacy.IKEProposals)
	}
}

func TestNewConfigRejectsBadInput(t *testing.T) {
	bad := []string{
		`{"inbound_tag":"../x","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`,
		`{"inbound_tag":"a b","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"short"}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"has\"quote-inside"}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"has{brace}inside"}`,
		`{"inbound_tag":"ok","pool":"fd00::/64","psk":"correct-horse-battery"}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/31","psk":"correct-horse-battery"}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"correct-horse-battery","local_ip":"10.99.0.1"}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"correct-horse-battery","dns":["not-an-ip"]}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"correct-horse-battery","ike_proposals":["aes256-sha1; rm -rf /"]}`,
		`{"inbound_tag":"ok","pool":"10.31.0.0/24","psk":"correct-horse-battery","egress_interface":"eth0; id"}`,
	}
	for _, raw := range bad {
		if _, err := NewConfig(raw); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestPoolRangeSkipsLocalIP(t *testing.T) {
	_, n, _ := net.ParseCIDR("10.31.0.0/24")
	start, end := poolRange(n, "10.31.0.1")
	if start != "10.31.0.2" || end != "10.31.0.254" {
		t.Fatalf("range = %s-%s", start, end)
	}
	start, end = poolRange(n, "10.31.0.100")
	if start != "10.31.0.1" || end != "10.31.0.254" {
		t.Fatalf("range = %s-%s", start, end)
	}
}

func user(id, password string, inbounds ...string) *common.User {
	return &common.User{
		Email:    id,
		Inbounds: inbounds,
		Proxies:  &common.Proxy{L2Tp: &common.L2TpUser{Username: id, Password: password}},
	}
}

func TestUserStoreKeysAddAndRemoveConsistently(t *testing.T) {
	s := newUserStore("l2tp-main")
	s.replaceAll([]*common.User{user("7", "pw", "l2tp-main"), user("8", "pw", "l2tp-main")})

	username, changed, removed := s.applyUser(user("7", "pw", "other-tag"))
	if username != "7" || changed || !removed {
		t.Fatalf("losing the tag must remove the user: %q changed=%v removed=%v", username, changed, removed)
	}
	if s.has("7") {
		t.Fatal("user 7 still present")
	}

	username, changed, removed = s.applyUser(user("8", "new-pw", "l2tp-main"))
	if username != "8" || !changed || removed {
		t.Fatalf("password rotation must report changed: %q changed=%v removed=%v", username, changed, removed)
	}

	gone := s.replaceAll([]*common.User{user("9", "pw", "l2tp-main")})
	if len(gone) != 1 || gone[0] != "8" {
		t.Fatalf("replaceAll removed = %v", gone)
	}
}

func useTempSessionDirs(t *testing.T) {
	t.Helper()
	prevState, prevFinal, prevAlive := sessionStateDir, sessionFinalDir, sessionIsAlive
	sessionStateDir = filepath.Join(t.TempDir(), "sessions")
	sessionFinalDir = filepath.Join(t.TempDir(), "final")
	sessionIsAlive = func(l2tpSession) bool { return true }
	t.Cleanup(func() { sessionStateDir, sessionFinalDir, sessionIsAlive = prevState, prevFinal, prevAlive })
	if err := os.MkdirAll(sessionStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionFinalDir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestFinalRecordsAreOrderedByTimeAndFilteredByTag(t *testing.T) {
	useTempSessionDirs(t)
	write := func(name, tag string, rx int64) {
		content := "user=7\ntag=" + tag + "\nifname=ppp0\nrx=" + itoa(rx) + "\ntx=0\n"
		if err := os.WriteFile(filepath.Join(sessionFinalDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ppp0.900.1700000100", "l2tp-main", 3)
	write("ppp0.1000.1700000050", "l2tp-main", 2)
	write("ppp0.10.1700000010", "l2tp-main", 1)
	write("ppp0.11.1700000001", "other", 99)
	write("ppp0.12.1700000005", "", 0)

	recs := readFinalRecords("l2tp-main")
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4 (own + untagged)", len(recs))
	}
	var order []int64
	for _, r := range recs {
		order = append(order, r.epoch)
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Fatalf("records not in time order: %v", order)
		}
	}
	for _, r := range recs {
		if r.tag == "other" {
			t.Fatal("another core's record leaked in")
		}
	}
}

func TestReadSessionsFiltersByTagWithoutReapingOthers(t *testing.T) {
	useTempSessionDirs(t)
	write := func(ifname, tag string) {
		content := "user=7\ntunnel_ip=10.31.0.2\ntag=" + tag + "\npid=1\nstarted=1\n"
		if err := os.WriteFile(filepath.Join(sessionStateDir, ifname), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ppp0", "l2tp-main")
	write("ppp1", "other")
	sessionIsAlive = func(l2tpSession) bool { return false }

	if got := readSessions("l2tp-main"); len(got) != 0 {
		t.Fatalf("dead session must be dropped, got %d", len(got))
	}
	if _, err := os.Stat(filepath.Join(sessionStateDir, "ppp1")); err != nil {
		t.Fatal("another core's session record was reaped")
	}
	if _, err := os.Stat(filepath.Join(sessionStateDir, "ppp0")); err == nil {
		t.Fatal("own dead session record was not reaped")
	}
}

func TestChapQuoteEscapes(t *testing.T) {
	if got := chapQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("chapQuote = %s", got)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestUnsupportedIPsecMatchOnlyClaimsAKernelLimitForARealOne(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"nft -c -f x: exit 1: Error: syntax error, unexpected ipsec", true},
		{"nft: unknown expression meta ipsec", true},
		{"meta ipsec is not supported by this kernel", true},
		{"nft -c -f /tmp/x.nft: exit status 1: Error: Could not process rule: File exists", false},
		{"nft: command not found", false},
		{"permission denied", false},
		{"Error: Could not process rule: Operation not permitted", false},
	}
	for _, c := range cases {
		if got := isUnsupportedIPsecMatch(errors.New(c.err)); got != c.want {
			t.Fatalf("isUnsupportedIPsecMatch(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestIPsecGuardIsOnUnlessExplicitlyDisabled(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{{"", true}, {"1", true}, {"true", true}, {"yes", true}, {"anything", true}, {"0", false}, {"false", false}, {"FALSE", false}, {"no", false}, {" 0 ", false}} {
		t.Setenv(envRequireIPsec, c.value)
		if got := requireIPsecEnabled(); got != c.want {
			t.Fatalf("%s=%q -> %v, want %v", envRequireIPsec, c.value, got, c.want)
		}
	}
	os.Unsetenv(envRequireIPsec)
	if !requireIPsecEnabled() {
		t.Fatal("an unset variable must keep the guard on")
	}
}

func TestUnsupportedIPsecMatchAgainstRealNftStderr(t *testing.T) {
	real := []struct {
		name string
		err  string
		want bool
	}{
		{"permission denied", `nft add rule ip pg_node_l2tp_nat input udp dport 1701 meta ipsec missing drop: exit status 1: Error: Could not process rule: Permission denied (you must be root)`, false},
		{"binary missing", `exec: "nft": executable file not found in $PATH`, false},
		{"table collision", `nft add table ip pg_l2tp_probe: exit status 1: Error: Could not process rule: File exists`, false},
		{"operation not permitted", `nft -c -f /tmp/p.nft: exit status 1: Error: Could not process rule: Operation not permitted`, false},
		{"no such chain", `nft -a list chain ip pg_node_l2tp_nat input: exit status 1: Error: No such file or directory`, false},
		{"old nft rejects the keyword", `nft -c -f /tmp/p.nft: exit status 1: /tmp/p.nft:3:20-24: Error: syntax error, unexpected ipsec, expecting string`, true},
		{"kernel lacks the expression", `nft -c -f /tmp/p.nft: exit status 1: Error: Could not process rule: meta ipsec is not supported by this kernel`, true},
		{"unknown expression", `nft -c -f /tmp/p.nft: exit status 1: Error: unknown expression 'meta ipsec'`, true},
	}
	for _, c := range real {
		if got := isUnsupportedIPsecMatch(errors.New(c.err)); got != c.want {
			t.Fatalf("%s: isUnsupportedIPsecMatch = %v, want %v (err: %s)", c.name, got, c.want, c.err)
		}
	}
}

func TestCredsForRejectsCredentialsThatCouldInjectAChapSecretsLine(t *testing.T) {
	cases := map[string]struct{ username, password string }{
		"newline in password": {"7", "good\nattacker l2tp-de \"pw\""},
		"newline in username": {"7\nattacker", "goodpassword"},
		"carriage return":     {"7", "good\rpassword"},
		"tab":                 {"7", "good\tpassword"},
		"space":               {"7", "good password"},
		"nul byte":            {"7", "good\x00password"},
		"non ascii":           {"7", "gööd-password"},
		"empty password":      {"7", ""},
		"empty username":      {"", "goodpassword"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u := &common.User{Proxies: &common.Proxy{L2Tp: &common.L2TpUser{Username: tc.username, Password: tc.password}}}
			if _, _, ok := credsFor(u); ok {
				t.Fatalf("credsFor accepted an unsafe credential: %q / %q", tc.username, tc.password)
			}
		})
	}
}

func TestCredsForAcceptsAPanelGeneratedCredential(t *testing.T) {
	u := &common.User{Proxies: &common.Proxy{L2Tp: &common.L2TpUser{Username: "27741", Password: "k81rSG4vJ9Ljhh4UJaPm"}}}
	username, password, ok := credsFor(u)
	if !ok || username != "27741" || password != "k81rSG4vJ9Ljhh4UJaPm" {
		t.Fatalf("credsFor rejected a valid credential: %q %q %v", username, password, ok)
	}
}

func TestCredsForRejectsOverlongCredentials(t *testing.T) {
	long := strings.Repeat("a", maxChapFieldLength+1)
	u := &common.User{Proxies: &common.Proxy{L2Tp: &common.L2TpUser{Username: "7", Password: long}}}
	if _, _, ok := credsFor(u); ok {
		t.Fatal("credsFor accepted an overlong password")
	}
	u = &common.User{Proxies: &common.Proxy{L2Tp: &common.L2TpUser{Username: long, Password: "goodpassword"}}}
	if _, _, ok := credsFor(u); ok {
		t.Fatal("credsFor accepted an overlong username")
	}
	atLimit := strings.Repeat("a", maxChapFieldLength)
	u = &common.User{Proxies: &common.Proxy{L2Tp: &common.L2TpUser{Username: "7", Password: atLimit}}}
	if _, _, ok := credsFor(u); !ok {
		t.Fatal("credsFor rejected a password exactly at the limit")
	}
}
