//go:build linux

package l2tp

import (
	"errors"
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
	probe := "table " + nftFamily + " pg_l2tp_probe {\n chain input {\n  type filter hook input priority 0; policy accept;\n  udp dport " +
		l2tpListenerPort + " meta ipsec missing drop\n }\n}\n"
	f, err := os.CreateTemp("", "pg-l2tp-probe-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(probe); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := runNFT("-c", "-f", f.Name()); err != nil {
		return errors.New("this kernel's nftables cannot match on IPsec (meta ipsec); set " + envRequireIPsec + "=0 to run L2TP without that protection")
	}
	return nil
}
