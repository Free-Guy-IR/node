//go:build linux

package l2tp

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
)

const (
	envRequireIPsec  = "PG_NODE_L2TP_REQUIRE_IPSEC"
	nftInputChain    = "input"
	l2tpListenerPort = "1701"
)

func requireIPsecEnabled() bool {
	v := strings.TrimSpace(os.Getenv(envRequireIPsec))
	return !(v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "no"))
}

func ensureIPsecGuard(ownerID string) error {
	if err := runNFT("add", "table", nftFamily, nftTable); err != nil && !nftAlreadyExists(err) {
		return err
	}
	if err := runNFT(
		"add", "chain", nftFamily, nftTable, nftInputChain,
		"{", "type", "filter", "hook", "input", "priority", "-10", ";", "policy", "accept", ";", "}",
	); err != nil && !nftAlreadyExists(err) {
		return err
	}
	chain := nftChain{family: nftFamily, table: nftTable, name: nftInputChain}
	if err := removeNFTRulesWithComment(chain, ownerCommentPrefix(ownerID)); err != nil {
		return err
	}
	return runNFT(
		"add", "rule", nftFamily, nftTable, nftInputChain,
		"udp", "dport", l2tpListenerPort, "meta", "ipsec", "missing", "drop",
		"comment", nftString(ownerCommentPrefix(ownerID)+"type=require-ipsec"),
	)
}

func removeIPsecGuard(ownerID string) error {
	chain := nftChain{family: nftFamily, table: nftTable, name: nftInputChain}
	if err := removeNFTRulesWithComment(chain, ownerCommentPrefix(ownerID)); err != nil && !nftMissing(err) {
		return err
	}
	return nil
}

func ipsecGuardSupported() error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("l2tp ipsec guard: cannot name a probe table: %w", err)
	}
	table := fmt.Sprintf("pg_l2tp_probe_%x", suffix)
	probe := "table " + nftFamily + " " + table + " {\n chain input {\n  type filter hook input priority 0; policy accept;\n  udp dport " +
		l2tpListenerPort + " meta ipsec missing drop\n }\n}\n"
	f, err := os.CreateTemp("", "pg-l2tp-probe-*.nft")
	if err != nil {
		return fmt.Errorf("l2tp ipsec guard: cannot write a probe ruleset: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(probe); err != nil {
		f.Close()
		return fmt.Errorf("l2tp ipsec guard: cannot write a probe ruleset: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("l2tp ipsec guard: cannot write a probe ruleset: %w", err)
	}
	err = runNFT("-c", "-f", f.Name())
	if err == nil {
		return nil
	}
	if isUnsupportedIPsecMatch(err) {
		return fmt.Errorf("this kernel's nftables cannot match on IPsec (meta ipsec); set %s=0 to run L2TP without that protection: %w", envRequireIPsec, err)
	}
	return fmt.Errorf("l2tp ipsec guard: could not validate the nftables rule: %w", err)
}

func isUnsupportedIPsecMatch(err error) bool {
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "ipsec") {
		return false
	}
	for _, marker := range []string{"unknown", "not supported", "unsupported", "syntax error", "invalid", "no such file"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
