//go:build linux

package l2tp

import "testing"

func TestParseForwardBaseChainsSkipsMangleChains(t *testing.T) {
	const ruleset = `{
		"nftables": [
			{"chain": {"family": "ip", "table": "mangle", "name": "FORWARD", "type": "filter", "hook": "forward", "prio": -150, "policy": "accept"}},
			{"chain": {"family": "inet", "table": "fw4", "name": "mangle_forward", "type": "filter", "hook": "forward", "prio": -150, "policy": "accept"}},
			{"chain": {"family": "inet", "table": "fw4", "name": "forward", "type": "filter", "hook": "forward", "prio": 0, "policy": "drop"}},
			{"chain": {"family": "ip", "table": "filter", "name": "FORWARD", "type": "filter", "hook": "forward", "prio": 0, "policy": "drop"}},
			{"chain": {"family": "ip", "table": "custom", "name": "fwd", "type": "filter", "hook": "forward", "prio": "unexpected", "policy": "accept"}},
			{"chain": {"family": "ip", "table": "pg_node_l2tp_nat", "name": "forward", "type": "filter", "hook": "forward", "prio": 0, "policy": "accept"}}
		]
	}`

	chains, err := parseForwardBaseChains([]byte(ruleset))
	if err != nil {
		t.Fatalf("parseForwardBaseChains returned error: %v", err)
	}

	want := []nftChain{
		{family: "inet", table: "fw4", name: "forward"},
		{family: "ip", table: "filter", name: "FORWARD"},
		{family: "ip", table: "custom", name: "fwd"},
	}
	if len(chains) != len(want) {
		t.Fatalf("expected %d chains without the mangle ones, got %#v", len(want), chains)
	}
	for i := range want {
		if chains[i] != want[i] {
			t.Fatalf("chain %d: expected %#v, got %#v", i, want[i], chains[i])
		}
	}
}
