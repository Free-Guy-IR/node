package openvpn

import (
	"fmt"
	"os/exec"
	"strings"
)

// NAT rules are managed per instance, keyed by that instance's configured
// network CIDR.
//
// Deliberately iptables (not WireGuard's nftables - see
// backend/wireguard/host_routing_linux.go): this backend only ever needs one
// simple, singular MASQUERADE rule per instance, not WireGuard's more
// elaborate scoped forwarding+NAT+policy-routing setup, and iptables' "-C"
// existence check gives trivial, precise idempotency for that simpler shape.
//
// Known limitation: if an instance's "network" changes between restarts, the
// old CIDR's rule is not retroactively cleaned up - there is no persistent
// store recording what a *previous* process's network was to diff against
// (this node keeps no database). In practice this only matters across a
// config change, not a crash/restart with the same config: natApplied's
// in-memory bookkeeping plus the idempotent -C check together mean a
// same-network restart never duplicates or leaks its own rule, and
// instanceProcess.Stop/Shutdown always attempts removal for the network it
// currently knows about.

func natCheckArgs(network string) []string {
	return []string{"-t", "nat", "-C", "POSTROUTING", "-s", network, "-j", "MASQUERADE"}
}

func natRuleExists(network string) bool {
	return exec.Command("iptables", natCheckArgs(network)...).Run() == nil
}

func addNATRule(network string) error {
	if natRuleExists(network) {
		return nil
	}
	out, err := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", network, "-j", "MASQUERADE").CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables: add MASQUERADE rule for %s: %w: %s", network, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeNATRule(network string) error {
	if !natRuleExists(network) {
		return nil
	}
	out, err := exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", network, "-j", "MASQUERADE").CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables: remove MASQUERADE rule for %s: %w: %s", network, err, strings.TrimSpace(string(out)))
	}
	return nil
}
