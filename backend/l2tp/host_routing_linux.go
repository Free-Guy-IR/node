//go:build linux

package l2tp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	envNATOutputInterface = "PG_NODE_L2TP_NAT_OUTPUT_INTERFACE"
	ipv4ForwardPath       = "/proc/sys/net/ipv4/ip_forward"
	nftFamily             = "ip"
	nftTable              = "pg_node_l2tp_nat"
	nftPostroutingChain   = "postrouting"
	nftForwardHook        = "forward"
	nftCommentPrefix      = "pg_node_l2tp "
)

type nftChain struct {
	family string
	table  string
	name   string
}

func checkHostPrereqs() error {
	if _, err := exec.LookPath("nft"); err != nil {
		return errors.New("nft is not installed on this node; L2TP needs nftables for NAT and MSS clamping")
	}
	if err := ensureIPv4Forwarding(); err != nil {
		return err
	}
	return nil
}

func ensureIPv4Forwarding() error {
	out, err := os.ReadFile(ipv4ForwardPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", ipv4ForwardPath, err)
	}
	if strings.TrimSpace(string(out)) == "1" {
		return nil
	}
	if err := os.WriteFile(ipv4ForwardPath, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("net.ipv4.ip_forward is 0 and cannot be set from this container; set it on the host (sysctl -w net.ipv4.ip_forward=1): %w", err)
	}
	return nil
}

func applyHostRouting(pool, egressIface, ownerID string, logf func(string, ...any)) (func(), error) {
	outIf := strings.TrimSpace(egressIface)
	if outIf == "" {
		outIf = strings.TrimSpace(os.Getenv(envNATOutputInterface))
	}
	if outIf == "" {
		detected, ok := defaultRouteInterfaceIPv4()
		if !ok {
			return nil, errors.New("could not detect the default IPv4 egress interface; set egress_interface on the core or " + envNATOutputInterface)
		}
		outIf = detected
	}
	logf("l2tp host routing: pool %s via %s (owner %s)", pool, outIf, ownerID)

	if err := ensureNFTMasquerade(pool, outIf, ownerID); err != nil {
		return nil, err
	}
	if err := ensureNFTForwarding(pool, ownerID, logf); err != nil {
		_ = cleanupHostRouting(ownerID)
		return nil, err
	}
	if requireIPsecEnabled() {
		if err := ipsecGuardSupported(); err != nil {
			_ = cleanupHostRouting(ownerID)
			return nil, err
		}
		if err := ensureIPsecGuard(ownerID); err != nil {
			_ = cleanupHostRouting(ownerID)
			return nil, err
		}
		logf("l2tp host routing: udp/%s accepted only from IPsec (set %s=0 to disable)", l2tpListenerPort, envRequireIPsec)
	} else {
		if err := removeIPsecGuard(ownerID); err != nil {
			logf("l2tp host routing: could not drop a previous IPsec guard: %v", err)
		}
		logf("l2tp host routing: WARNING %s is off, plain L2TP without IPsec is accepted on udp/%s", envRequireIPsec, l2tpListenerPort)
	}
	return func() {
		if err := cleanupHostRouting(ownerID); err != nil {
			logf("l2tp host routing: cleanup failed: %v", err)
		}
	}, nil
}

func ensureNFTMasquerade(pool, outIf, ownerID string) error {
	if err := runNFT("add", "table", nftFamily, nftTable); err != nil && !nftAlreadyExists(err) {
		return err
	}
	if err := runNFT(
		"add", "chain", nftFamily, nftTable, nftPostroutingChain,
		"{", "type", "nat", "hook", "postrouting", "priority", "100", ";", "policy", "accept", ";", "}",
	); err != nil && !nftAlreadyExists(err) {
		return err
	}
	chain := nftChain{family: nftFamily, table: nftTable, name: nftPostroutingChain}
	if err := removeNFTRulesWithComment(chain, ownerCommentPrefix(ownerID)); err != nil {
		return err
	}
	return runNFT(
		"add", "rule", nftFamily, nftTable, nftPostroutingChain,
		"ip", "saddr", pool, "oifname", nftString(outIf), "masquerade",
		"comment", nftString(ownerCommentPrefix(ownerID)+"type=nat"),
	)
}

func ensureNFTForwarding(pool, ownerID string, logf func(string, ...any)) error {
	chains, err := nftForwardBaseChains()
	if err != nil {
		return err
	}
	if len(chains) == 0 {
		logf("l2tp host routing: no forward base chain found; kernel forward policy applies")
		return ensureOwnForwardChain(pool, ownerID)
	}
	for _, chain := range chains {
		if err := removeNFTRulesWithComment(chain, ownerCommentPrefix(ownerID)); err != nil {
			return err
		}
		if err := insertForwardRules(chain, pool, ownerID); err != nil {
			return err
		}
	}
	return nil
}

func ensureOwnForwardChain(pool, ownerID string) error {
	if err := runNFT(
		"add", "chain", nftFamily, nftTable, nftForwardHook,
		"{", "type", "filter", "hook", "forward", "priority", "0", ";", "policy", "accept", ";", "}",
	); err != nil && !nftAlreadyExists(err) {
		return err
	}
	chain := nftChain{family: nftFamily, table: nftTable, name: nftForwardHook}
	if err := removeNFTRulesWithComment(chain, ownerCommentPrefix(ownerID)); err != nil {
		return err
	}
	return insertForwardRules(chain, pool, ownerID)
}

func insertForwardRules(chain nftChain, pool, ownerID string) error {
	prefix := ownerCommentPrefix(ownerID)
	rules := [][]string{
		{"ip", "saddr", pool, "tcp", "flags", "syn", "tcp", "option", "maxseg", "size", "set", "rt", "mtu", "comment", nftString(prefix + "type=mss")},
		{"ip", "daddr", pool, "tcp", "flags", "syn", "tcp", "option", "maxseg", "size", "set", "rt", "mtu", "comment", nftString(prefix + "type=mss-return")},
		{"ip", "daddr", pool, "ct", "state", "established,related", "accept", "comment", nftString(prefix + "type=forward-return")},
		{"ip", "saddr", pool, "accept", "comment", nftString(prefix + "type=forward")},
	}
	for _, rule := range rules {
		args := append([]string{"insert", "rule", chain.family, chain.table, chain.name}, rule...)
		if err := runNFT(args...); err != nil {
			return err
		}
	}
	return nil
}

func cleanupHostRouting(ownerID string) error {
	var errs []error
	prefix := ownerCommentPrefix(ownerID)
	natChain := nftChain{family: nftFamily, table: nftTable, name: nftPostroutingChain}
	if err := removeNFTRulesWithComment(natChain, prefix); err != nil && !nftMissing(err) {
		errs = append(errs, err)
	}
	ownChain := nftChain{family: nftFamily, table: nftTable, name: nftForwardHook}
	if err := removeNFTRulesWithComment(ownChain, prefix); err != nil && !nftMissing(err) {
		errs = append(errs, err)
	}
	if err := removeIPsecGuard(ownerID); err != nil && !nftMissing(err) {
		errs = append(errs, err)
	}
	chains, err := nftForwardBaseChains()
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, chain := range chains {
			if err := removeNFTRulesWithComment(chain, prefix); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

type nftRuleset struct {
	NFTables []map[string]json.RawMessage `json:"nftables"`
}

type nftListedChain struct {
	Family string `json:"family"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	Hook   string `json:"hook"`
}

func nftForwardBaseChains() ([]nftChain, error) {
	out, err := exec.Command("nft", "-j", "list", "ruleset").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("nft -j list ruleset: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return parseForwardBaseChains(out)
}

func parseForwardBaseChains(data []byte) ([]nftChain, error) {
	var ruleset nftRuleset
	if err := json.Unmarshal(data, &ruleset); err != nil {
		return nil, fmt.Errorf("parse nft ruleset: %w", err)
	}
	chains := make([]nftChain, 0)
	for _, item := range ruleset.NFTables {
		raw, ok := item["chain"]
		if !ok {
			continue
		}
		var chain nftListedChain
		if err := json.Unmarshal(raw, &chain); err != nil {
			return nil, fmt.Errorf("parse nft chain: %w", err)
		}
		if chain.Hook != nftForwardHook || (chain.Family != "ip" && chain.Family != "inet") {
			continue
		}
		if chain.Table == nftTable {
			continue
		}
		chains = append(chains, nftChain{family: chain.Family, table: chain.Table, name: chain.Name})
	}
	return chains, nil
}

func removeNFTRulesWithComment(chain nftChain, commentPrefix string) error {
	out, err := exec.Command("nft", "-a", "list", "chain", chain.family, chain.table, chain.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft -a list chain %s %s %s: %w: %s", chain.family, chain.table, chain.name, err, strings.TrimSpace(string(out)))
	}
	for _, handle := range ruleHandlesWithComment(out, commentPrefix) {
		if err := runNFT("delete", "rule", chain.family, chain.table, chain.name, "handle", handle); err != nil {
			return err
		}
	}
	return nil
}

func ruleHandlesWithComment(data []byte, commentPrefix string) []string {
	handles := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, commentPrefix) {
			continue
		}
		before, handle, ok := strings.Cut(line, "# handle ")
		if !ok || strings.TrimSpace(before) == "" {
			continue
		}
		fields := strings.Fields(handle)
		if len(fields) == 0 {
			continue
		}
		handles = append(handles, fields[0])
	}
	return handles
}

func defaultRouteInterfaceIPv4() (string, bool) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", false
	}
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		flags, err := strconv.ParseInt(fields[3], 16, 64)
		if err != nil {
			continue
		}
		if fields[1] == "00000000" && flags&0x2 != 0 {
			return fields[0], true
		}
	}
	return "", false
}

func ownerCommentPrefix(ownerID string) string {
	return nftCommentPrefix + "owner=" + ownerID + " "
}

func nftString(s string) string {
	return fmt.Sprintf("%q", s)
}

func runNFT(args ...string) error {
	out, err := exec.Command("nft", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func nftAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "File exists")
}

func nftMissing(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "No such file or directory") || strings.Contains(err.Error(), "does not exist"))
}
