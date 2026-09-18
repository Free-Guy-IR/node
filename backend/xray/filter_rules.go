package xray

import (
	"encoding/json"
	"strings"
)

const contentFilterTagPrefix = "pgcf"

var contentFilterTagParts = map[string]struct{}{
	"allow":  {},
	"block":  {},
	"cat":    {},
	"strict": {},
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}

	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}

	return true
}

func isContentFilterRuleTag(tag string) bool {
	parts := strings.Split(tag, "-")
	if len(parts) != 3 && len(parts) != 5 {
		return false
	}

	if parts[0] != contentFilterTagPrefix || !isDecimal(parts[1]) {
		return false
	}

	_, known := contentFilterTagParts[parts[2]]
	return known
}

func routingRuleTag(raw json.RawMessage) string {
	var rule struct {
		RuleTag string `json:"ruleTag"`
	}

	if err := json.Unmarshal(raw, &rule); err != nil {
		return ""
	}

	return rule.RuleTag
}

func contentFilterRuleTags(c *Config) []string {
	if c == nil || c.RouterConfig == nil {
		return nil
	}

	var tags []string
	for _, raw := range c.RouterConfig.RuleList {
		if tag := routingRuleTag(raw); isContentFilterRuleTag(tag) {
			tags = append(tags, tag)
		}
	}

	return tags
}

func stripContentFilterRules(c *Config) []string {
	if c == nil || c.RouterConfig == nil {
		return nil
	}

	kept := make([]json.RawMessage, 0, len(c.RouterConfig.RuleList))
	var removed []string

	for _, raw := range c.RouterConfig.RuleList {
		tag := routingRuleTag(raw)
		if isContentFilterRuleTag(tag) {
			removed = append(removed, tag)
			continue
		}
		kept = append(kept, raw)
	}

	if len(removed) == 0 {
		return nil
	}

	c.RouterConfig.RuleList = kept
	return removed
}
