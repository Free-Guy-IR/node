//go:build linux

package l2tp

import (
	"errors"
	"os"
	"testing"
)

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
