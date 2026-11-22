package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
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

var allowingRuleNeverMatchesAnAddress = map[string]struct{}{
	"domain":  {},
	"domains": {},
}

const privateAllowRuleTagStem = "PG_NODE_PRIVATE_ALLOW_"

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

func decodeRule(raw json.RawMessage) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var rule map[string]any
	if err := decoder.Decode(&rule); err != nil || rule == nil {
		return nil, false
	}
	return rule, true
}

func foldedFields(rule map[string]any) (map[string]any, bool) {
	fields := make(map[string]any, len(rule))
	duplicated := false
	for key, value := range rule {
		folded := strings.ToLower(key)
		if _, dup := fields[folded]; dup {
			duplicated = true
		}
		fields[folded] = value
	}
	return fields, duplicated
}

func ruleTagOf(fields map[string]any) string {
	tag, _ := fields["ruletag"].(string)
	return tag
}

func withRuleTag(rule map[string]any, tag string) (json.RawMessage, error) {
	tagged := make(map[string]any, len(rule)+1)
	for key, value := range rule {
		if !strings.EqualFold(key, "ruleTag") {
			tagged[key] = value
		}
	}
	tagged["ruleTag"] = tag
	raw, err := json.Marshal(tagged)
	return json.RawMessage(raw), err
}

func (c *Config) taggedAllowingRules(freedomTags map[string]struct{}) (*conf.RouterConfig, map[string][]any) {
	allows := make(map[string][]any)
	if c.RouterConfig == nil {
		return nil, allows
	}
	tagCount := make(map[string]int)
	for _, raw := range c.RouterConfig.RuleList {
		if rule, ok := decodeRule(raw); ok {
			fields, _ := foldedFields(rule)
			if tag := ruleTagOf(fields); tag != "" {
				tagCount[tag]++
			}
		}
	}
	rules := slices.Clone(c.RouterConfig.RuleList)
	seen := make(map[string]map[string]struct{})
	ordinal := 0
	for index, raw := range c.RouterConfig.RuleList {
		rule, ok := decodeRule(raw)
		if !ok {
			continue
		}
		fields, duplicated := foldedFields(rule)
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
		var unreachable []string
		for key, value := range rule {
			if _, never := allowingRuleNeverMatchesAnAddress[strings.ToLower(key)]; never && len(ruleEntries(value)) > 0 {
				unreachable = append(unreachable, key)
			}
		}
		if duplicated || len(unreachable) > 0 {
			sort.Strings(unreachable)
			log.Printf("xray config: %s sends private addresses %v to the freedom outbound %q but repeats a key or also matches on %v, so freedom gets no allow for them", describeRule(index, fields), private, target, unreachable)
			continue
		}
		tag := ruleTagOf(fields)
		if tag != "" && tagCount[tag] > 1 {
			log.Printf("xray config: %s sends private addresses %v to the freedom outbound %q under the ruleTag %q that other rules share, so freedom gets no allow for them", describeRule(index, fields), private, target, tag)
			continue
		}
		if tag == "" {
			for {
				ordinal++
				tag = privateAllowRuleTagStem + strconv.Itoa(ordinal)
				if tagCount[tag] == 0 {
					break
				}
			}
			tagCount[tag]++
			tagged, err := withRuleTag(rule, tag)
			if err != nil {
				continue
			}
			rules[index] = tagged
		}
		entry := map[string]any{"ruleTag": tag, "ip": private}
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
	routed := *c.RouterConfig
	routed.RuleList = rules
	return &routed, allows
}

func withoutPrivateAllow(settings map[string]any) (map[string]any, bool) {
	kept := make(map[string]any, len(settings)+1)
	removed := false
	for key, value := range settings {
		if strings.EqualFold(key, privateAllowKey) {
			removed = true
			continue
		}
		kept[key] = value
	}
	return kept, removed
}

func withoutPanelPrivateAllow(outbound map[string]any) map[string]any {
	var copied map[string]any
	for key, value := range outbound {
		settings, isMap := value.(map[string]any)
		if !isMap || !strings.EqualFold(key, "settings") {
			continue
		}
		if kept, removed := withoutPrivateAllow(settings); removed {
			if copied == nil {
				copied = maps.Clone(outbound)
			}
			copied[key] = kept
		}
	}
	if copied == nil {
		return outbound
	}
	return copied
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
	if settingsKeys > 1 || (settings == nil && outbound[settingsKey] != nil) {
		if len(entries) > 0 {
			tag, _ := foldedField(outbound, "tag")
			log.Printf("xray config: the freedom outbound %v has settings the node cannot edit, so it gets no privateAllow", tag)
		}
		return withoutPanelPrivateAllow(outbound)
	}
	kept, removed := withoutPrivateAllow(settings)
	if !removed && len(entries) == 0 {
		return outbound
	}
	if len(entries) > 0 {
		kept[privateAllowKey] = entries
	}
	copied := maps.Clone(outbound)
	copied[settingsKey] = kept
	return copied
}

func mightBeFreedom(outbound map[string]any) bool {
	for key, value := range outbound {
		if strings.EqualFold(key, "protocol") && isFreedomProtocol(value) {
			return true
		}
	}
	return false
}

func foldedField(object map[string]any, name string) (any, int) {
	var value any
	keys := 0
	for key, v := range object {
		if strings.EqualFold(key, name) {
			value = v
			keys++
		}
	}
	return value, keys
}

func freedomOutbound(outbound any) (map[string]any, string, bool) {
	obj, isMap := outbound.(map[string]any)
	if !isMap {
		return nil, "", false
	}
	protocol, keys := foldedField(obj, "protocol")
	if keys != 1 || !isFreedomProtocol(protocol) {
		return nil, "", false
	}
	value, keys := foldedField(obj, "tag")
	if keys > 1 {
		log.Printf("xray config: a freedom outbound repeats its tag key in another letter case, so it gets no privateAllow")
		return nil, "", false
	}
	tag, _ := value.(string)
	return obj, tag, true
}

func (c *Config) privateTargetRendering() (*conf.RouterConfig, any) {
	outbounds, ok := c.OutboundConfigs.([]any)
	if !ok {
		return c.RouterConfig, c.OutboundConfigs
	}
	freedomTags := make(map[string]struct{})
	freedomLike := false
	for _, outbound := range outbounds {
		if obj, isMap := outbound.(map[string]any); isMap && mightBeFreedom(obj) {
			freedomLike = true
		}
		if _, tag, freedom := freedomOutbound(outbound); freedom {
			freedomTags[tag] = struct{}{}
		}
	}
	if !freedomLike {
		return c.RouterConfig, c.OutboundConfigs
	}
	routerConfig, allows := c.taggedAllowingRules(freedomTags)
	rendered := make([]any, 0, len(outbounds))
	for _, outbound := range outbounds {
		if obj, tag, freedom := freedomOutbound(outbound); freedom {
			rendered = append(rendered, withPrivateAllow(obj, allows[tag]))
		} else if obj, isMap := outbound.(map[string]any); isMap && mightBeFreedom(obj) {
			rendered = append(rendered, withoutPanelPrivateAllow(obj))
		} else {
			rendered = append(rendered, outbound)
		}
	}
	return routerConfig, rendered
}
