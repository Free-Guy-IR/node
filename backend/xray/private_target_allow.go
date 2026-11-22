package xray

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/netip"
	"sort"
	"strings"
)

const privateAllowKey = "privateAllow"

func privateV4Prefix(a, b, c, d byte, bits int) netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{a, b, c, d}), bits)
}

var privateTargetPrefixes = []netip.Prefix{
	privateV4Prefix(0, 0, 0, 0, 8),
	privateV4Prefix(10, 0, 0, 0, 8),
	privateV4Prefix(100, 64, 0, 0, 10),
	privateV4Prefix(127, 0, 0, 0, 8),
	privateV4Prefix(169, 254, 0, 0, 16),
	privateV4Prefix(172, 16, 0, 0, 12),
	privateV4Prefix(192, 0, 0, 0, 24),
	privateV4Prefix(192, 0, 2, 0, 24),
	privateV4Prefix(192, 88, 99, 0, 24),
	privateV4Prefix(192, 168, 0, 0, 16),
	privateV4Prefix(198, 18, 0, 0, 15),
	privateV4Prefix(198, 51, 100, 0, 24),
	privateV4Prefix(203, 0, 113, 0, 24),
	privateV4Prefix(224, 0, 0, 0, 3),
	netip.MustParsePrefix("::/127"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var nodeGuardedPrefixes = func() []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(apiGuardDestinationCIDRs))
	for _, cidr := range apiGuardDestinationCIDRs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixes
}()

var privateAllowRuleKeys = map[string]struct{}{
	"type":          {},
	"ruletag":       {},
	"outboundtag":   {},
	"balancertag":   {},
	"domainmatcher": {},
	"webhook":       {},
	"ip":            {},
	"inboundtag":    {},
	"network":       {},
	"port":          {},
}

func isFreedomProtocol(value any) bool {
	protocol, _ := value.(string)
	protocol = strings.TrimSpace(protocol)
	return strings.EqualFold(protocol, "freedom") || strings.EqualFold(protocol, "direct")
}

func literalPrefix(entry string) (netip.Prefix, bool) {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		return prefix.Masked(), !prefix.Addr().Is4In6() && prefix.Addr().Zone() == ""
	}
	if addr, err := netip.ParseAddr(entry); err == nil {
		return netip.PrefixFrom(addr, addr.BitLen()), !addr.Is4In6() && addr.Zone() == ""
	}
	return netip.Prefix{}, false
}

func explicitlyPrivate(prefix netip.Prefix) bool {
	for _, guarded := range nodeGuardedPrefixes {
		if guarded.Overlaps(prefix) {
			return false
		}
	}
	for _, private := range privateTargetPrefixes {
		if private.Bits() <= prefix.Bits() && private.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func ruleEntries(value any) []string {
	var entries []string
	switch v := value.(type) {
	case string:
		entries = strings.Split(v, ",")
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				entries = append(entries, s)
			}
		}
	}
	expanded := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if strings.EqualFold(entry, "geoip:private") {
			expanded = append(expanded, privateCIDRs...)
			continue
		}
		expanded = append(expanded, entry)
	}
	return expanded
}

func explicitPrivateAddresses(value any) []string {
	var private []string
	for _, entry := range ruleEntries(value) {
		if prefix, ok := literalPrefix(entry); ok && explicitlyPrivate(prefix) {
			private = append(private, entry)
		}
	}
	return private
}

func describeRule(index int, fields map[string]any) string {
	if tag, ok := fields["ruletag"].(string); ok && tag != "" {
		return fmt.Sprintf("routing rule %q", tag)
	}
	return fmt.Sprintf("routing rule #%d", index)
}

func (c *Config) privateAllowsByOutbound(freedomTags map[string]struct{}) map[string][]any {
	allows := make(map[string][]any)
	if c.RouterConfig == nil {
		return allows
	}
	seen := make(map[string]map[string]struct{})
	for index, raw := range c.RouterConfig.RuleList {
		var rule map[string]any
		if err := json.Unmarshal(raw, &rule); err != nil {
			continue
		}
		fields := make(map[string]any, len(rule))
		duplicated := false
		var others []string
		for key, value := range rule {
			folded := strings.ToLower(key)
			if _, dup := fields[folded]; dup {
				duplicated = true
			}
			fields[folded] = value
			if _, known := privateAllowRuleKeys[folded]; !known {
				others = append(others, key)
			}
		}
		private := explicitPrivateAddresses(fields["ip"])
		if len(private) == 0 {
			continue
		}
		target, _ := fields["outboundtag"].(string)
		if target == "" {
			if balancer, _ := fields["balancertag"].(string); balancer != "" {
				log.Printf("xray config: %s sends private addresses %v to the balancer %q; freedom gets no allow from a balancer rule", describeRule(index, fields), private, balancer)
			}
			continue
		}
		if _, freedom := freedomTags[target]; !freedom {
			continue
		}
		if duplicated || len(others) > 0 {
			sort.Strings(others)
			log.Printf("xray config: %s sends private addresses %v to the freedom outbound %q but also matches on %v, so freedom gets no allow for them", describeRule(index, fields), private, target, others)
			continue
		}
		entry := map[string]any{"ip": private}
		for _, key := range []string{"inboundTag", "network", "port"} {
			if value, ok := fields[strings.ToLower(key)]; ok {
				entry[key] = value
			}
		}
		canonical, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		if seen[target] == nil {
			seen[target] = make(map[string]struct{})
		}
		if _, dup := seen[target][string(canonical)]; dup {
			continue
		}
		seen[target][string(canonical)] = struct{}{}
		allows[target] = append(allows[target], entry)
	}
	return allows
}

func withPrivateAllow(outbound map[string]any, entries []any) map[string]any {
	settingsKey := "settings"
	var settings map[string]any
	settingsKeys := 0
	for key, value := range outbound {
		if strings.EqualFold(key, "settings") {
			settingsKeys++
			settingsKey = key
			settings, _ = value.(map[string]any)
		}
	}
	stale := false
	for key := range settings {
		if strings.EqualFold(key, privateAllowKey) {
			stale = true
		}
	}
	if !stale && len(entries) == 0 {
		return outbound
	}
	if settingsKeys > 1 || (settings == nil && outbound[settingsKey] != nil) {
		log.Printf("xray config: the freedom outbound %v has settings the node cannot edit, so it gets no privateAllow", outbound["tag"])
		return outbound
	}
	next := make(map[string]any, len(settings)+1)
	for key, value := range settings {
		if !strings.EqualFold(key, privateAllowKey) {
			next[key] = value
		}
	}
	if len(entries) > 0 {
		next[privateAllowKey] = entries
	}
	copied := maps.Clone(outbound)
	copied[settingsKey] = next
	return copied
}

func (c *Config) outboundsWithPrivateAllow() any {
	outbounds, ok := c.OutboundConfigs.([]any)
	if !ok {
		return c.OutboundConfigs
	}
	freedomTags := make(map[string]struct{})
	for _, outbound := range outbounds {
		if obj, isMap := outbound.(map[string]any); isMap && isFreedomProtocol(obj["protocol"]) {
			tag, _ := obj["tag"].(string)
			freedomTags[tag] = struct{}{}
		}
	}
	if len(freedomTags) == 0 {
		return c.OutboundConfigs
	}
	allows := c.privateAllowsByOutbound(freedomTags)
	rendered := make([]any, 0, len(outbounds))
	for _, outbound := range outbounds {
		obj, isMap := outbound.(map[string]any)
		if !isMap || !isFreedomProtocol(obj["protocol"]) {
			rendered = append(rendered, outbound)
			continue
		}
		tag, _ := obj["tag"].(string)
		rendered = append(rendered, withPrivateAllow(obj, allows[tag]))
	}
	return rendered
}
