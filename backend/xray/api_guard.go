package xray

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	apiGuardRuleTag       = "PG_NODE_API_GUARD"
	apiGuardIPRuleTag     = apiGuardRuleTag + "_IP"
	apiGuardDomainRuleTag = apiGuardRuleTag + "_DOMAIN"
	apiGuardOutboundTag   = "PG_NODE_API_GUARD_BLOCK"
)

var apiGuardDestinationCIDRs = []string{
	"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16",
	"::/128",
	"::1/128",
	"fe80::/10",
}

var apiGuardDomains = []string{
	"regexp:(^|\\.)localhost\\.?$",
	"regexp:^[0-9][0-9a-fx.]*$",
}

func apiGuardDestinationIPs() []string {
	seen := make(map[string]struct{}, len(apiGuardDestinationCIDRs))
	ips := make([]string, 0, len(apiGuardDestinationCIDRs))
	for _, entry := range append(append([]string{}, apiGuardDestinationCIDRs...), apiRuleSources()...) {
		if _, dup := seen[entry]; dup {
			continue
		}
		seen[entry] = struct{}{}
		ips = append(ips, entry)
	}
	return ips
}

func apiGuardRules(ports ...int) ([]json.RawMessage, error) {
	portList := make([]string, 0, len(ports))
	for _, port := range ports {
		if port > 0 {
			portList = append(portList, fmt.Sprintf("%d", port))
		}
	}
	if len(portList) == 0 {
		return nil, nil
	}
	portSpec := strings.Join(portList, ",")

	rules := []map[string]any{
		{
			"type":        "field",
			"ruleTag":     apiGuardIPRuleTag,
			"ip":          apiGuardDestinationIPs(),
			"port":        portSpec,
			"outboundTag": apiGuardOutboundTag,
		},
		{
			"type":        "field",
			"ruleTag":     apiGuardDomainRuleTag,
			"domain":      apiGuardDomains,
			"port":        portSpec,
			"outboundTag": apiGuardOutboundTag,
		},
	}

	raws := make([]json.RawMessage, 0, len(rules))
	for _, rule := range rules {
		rawBytes, err := json.Marshal(rule)
		if err != nil {
			return nil, err
		}
		raws = append(raws, json.RawMessage(rawBytes))
	}
	return raws, nil
}

func (c *Config) ensureAPIGuardOutbound() {
	guard := map[string]any{"tag": apiGuardOutboundTag, "protocol": "blackhole"}

	if c.OutboundConfigs == nil {
		c.OutboundConfigs = []any{guard}
		return
	}

	outbounds, ok := c.OutboundConfigs.([]any)
	if !ok {
		return
	}

	kept := make([]any, 0, len(outbounds)+1)
	for _, outbound := range outbounds {
		if obj, isMap := outbound.(map[string]any); isMap {
			if tag, _ := obj["tag"].(string); strings.TrimSpace(tag) == apiGuardOutboundTag {
				continue
			}
		}
		kept = append(kept, outbound)
	}
	c.OutboundConfigs = append(kept, guard)
}

const (
	siblingGuardRuleTag       = "PG_NODE_SIBLING_LOOPBACK_GUARD"
	siblingGuardIPRuleTag     = siblingGuardRuleTag + "_IP"
	siblingGuardDomainRuleTag = siblingGuardRuleTag + "_DOMAIN"
)

func nodeGuardRuleTag(tag string) bool {
	switch tag {
	case malformedDomainGuardRuleTag,
		apiGuardRuleTag, apiGuardIPRuleTag, apiGuardDomainRuleTag,
		siblingGuardRuleTag, siblingGuardIPRuleTag, siblingGuardDomainRuleTag:
		return true
	default:
		return false
	}
}

var siblingGuardUserProtocols = protocolSet(userProtocols)

func protocolSet(protocols []string) map[string]struct{} {
	set := make(map[string]struct{}, len(protocols))
	for _, protocol := range protocols {
		set[protocol] = struct{}{}
	}
	return set
}

var siblingGuardDomains = []string{
	"regexp:(^|\\.)localhost\\.?$",
}

func (c *Config) userInboundTags() []string {
	seen := make(map[string]struct{})
	tags := make([]string, 0, len(c.InboundConfigs))
	for _, inbound := range c.InboundConfigs {
		if inbound == nil {
			continue
		}
		if _, ok := siblingGuardUserProtocols[strings.ToLower(strings.TrimSpace(inbound.Protocol))]; !ok {
			continue
		}
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func siblingGuardRules(userInboundTags []string) ([]json.RawMessage, error) {
	if len(userInboundTags) == 0 {
		return nil, nil
	}

	rules := []map[string]any{
		{
			"type":        "field",
			"ruleTag":     siblingGuardIPRuleTag,
			"inboundTag":  userInboundTags,
			"ip":          append([]string{}, apiGuardDestinationCIDRs...),
			"outboundTag": apiGuardOutboundTag,
		},
		{
			"type":        "field",
			"ruleTag":     siblingGuardDomainRuleTag,
			"inboundTag":  userInboundTags,
			"domain":      siblingGuardDomains,
			"outboundTag": apiGuardOutboundTag,
		},
	}

	raws := make([]json.RawMessage, 0, len(rules))
	for _, rule := range rules {
		rawBytes, err := json.Marshal(rule)
		if err != nil {
			return nil, err
		}
		raws = append(raws, json.RawMessage(rawBytes))
	}
	return raws, nil
}
