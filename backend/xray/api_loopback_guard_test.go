package xray

import (
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type resolvedRule struct {
	InboundTag  []string `json:"inboundTag"`
	IP          []string `json:"ip"`
	Domain      []string `json:"domain"`
	Port        any      `json:"port"`
	Network     any      `json:"network"`
	OutboundTag string   `json:"outboundTag"`
	RuleTag     string   `json:"ruleTag"`
}

type destQuery struct {
	inbound string
	isIP    bool
	value   string
	port    int
}

func parsePortSpec(spec any) (map[int]struct{}, bool) {
	ports := map[int]struct{}{}
	switch v := spec.(type) {
	case nil:
		return nil, false
	case float64:
		ports[int(v)] = struct{}{}
	case int:
		ports[v] = struct{}{}
	case string:
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.Contains(part, "-") {
				bounds := strings.SplitN(part, "-", 2)
				lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
				hi, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
				if err1 == nil && err2 == nil {
					for p := lo; p <= hi; p++ {
						ports[p] = struct{}{}
					}
				}
				continue
			}
			if p, err := strconv.Atoi(part); err == nil {
				ports[p] = struct{}{}
			}
		}
	case []any:
		for _, item := range v {
			switch n := item.(type) {
			case float64:
				ports[int(n)] = struct{}{}
			case string:
				if p, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
					ports[p] = struct{}{}
				}
			}
		}
	}
	return ports, true
}

func ipMatchesList(list []string, value string) bool {
	target := net.ParseIP(value)
	if target == nil {
		return false
	}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err == nil && cidr.Contains(target) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(entry); ip != nil && ip.Equal(target) {
			return true
		}
	}
	return false
}

func domainMatchesList(list []string, value string) bool {
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		switch {
		case strings.HasPrefix(entry, "full:"):
			if value == strings.TrimPrefix(entry, "full:") {
				return true
			}
		case strings.HasPrefix(entry, "domain:"):
			base := strings.TrimPrefix(entry, "domain:")
			if value == base || strings.HasSuffix(value, "."+base) {
				return true
			}
		case strings.HasPrefix(entry, "regexp:"):
			if re, err := regexp.Compile(strings.TrimPrefix(entry, "regexp:")); err == nil && re.MatchString(value) {
				return true
			}
		case strings.HasPrefix(entry, "keyword:"):
			if strings.Contains(value, strings.TrimPrefix(entry, "keyword:")) {
				return true
			}
		default:
			if value == entry || strings.HasSuffix(value, "."+entry) || strings.Contains(value, entry) {
				return true
			}
		}
	}
	return false
}

func ruleMatches(r resolvedRule, q destQuery) bool {
	if len(r.InboundTag) > 0 {
		found := false
		for _, tag := range r.InboundTag {
			if tag == q.inbound {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if ports, present := parsePortSpec(r.Port); present {
		if _, ok := ports[q.port]; !ok {
			return false
		}
	}

	if !networkMatches(r.Network, "tcp") {
		return false
	}

	if q.isIP {
		if len(r.Domain) > 0 {
			return false
		}
		if len(r.IP) > 0 {
			return ipMatchesList(r.IP, q.value)
		}
		return true
	}

	if len(r.IP) > 0 {
		return false
	}
	if len(r.Domain) > 0 {
		return domainMatchesList(r.Domain, q.value)
	}
	return true
}

func networkMatches(spec any, network string) bool {
	switch v := spec.(type) {
	case nil:
		return true
	case string:
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), network) {
				return true
			}
		}
		return false
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.EqualFold(strings.TrimSpace(s), network) {
				return true
			}
		}
		return false
	}
	return true
}

func resolveOutbound(t *testing.T, cfg *Config, q destQuery) (tag string, protocol string) {
	t.Helper()

	tag, protocol, err := routeFor(cfg, q)
	if err != nil {
		t.Fatal(err)
	}
	return tag, protocol
}

func routeFor(cfg *Config, q destQuery) (tag string, protocol string, err error) {
	protocolByTag := cfg.outboundProtocolByTag()

	defaultTag := ""
	if raw, ok := cfg.OutboundConfigs.([]any); ok {
		for _, outbound := range raw {
			if obj, ok := outbound.(map[string]any); ok {
				if first, _ := obj["tag"].(string); first != "" {
					defaultTag = first
					break
				}
			}
		}
	}

	if cfg.RouterConfig != nil {
		for _, raw := range cfg.RouterConfig.RuleList {
			var r resolvedRule
			if err := json.Unmarshal(raw, &r); err != nil {
				return "", "", err
			}
			if ruleMatches(r, q) {
				if proto, ok := protocolByTag[r.OutboundTag]; ok {
					return r.OutboundTag, proto, nil
				}
				return r.OutboundTag, "__dropped__", nil
			}
		}
	}

	if proto, ok := protocolByTag[defaultTag]; ok {
		return defaultTag, proto, nil
	}
	return defaultTag, "__unknown__", nil
}

func destinationIsBlocked(protocol string) bool {
	return protocol == "blackhole" || protocol == "__dropped__"
}

const userInboundTag = "VLESS LOOPBACK PROBE"

type panelRouting int

const (
	panelNoRules panelRouting = iota
	panelPrivateBlock
	panelCatchAllDirect
)

func loopbackProbeConfig(t *testing.T, routing panelRouting) *Config {
	t.Helper()

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag":      userInboundTag,
				"listen":   "0.0.0.0",
				"port":     12345,
				"protocol": "vless",
				"settings": map[string]any{"clients": []any{}, "decryption": "none"},
			},
		},
		"outbounds": []any{
			map[string]any{"tag": "DIRECT", "protocol": "freedom"},
			map[string]any{"tag": "BLOCK", "protocol": "blackhole"},
		},
	}
	switch routing {
	case panelPrivateBlock:
		cfg["routing"] = map[string]any{
			"rules": []any{
				map[string]any{"type": "field", "ip": []any{"geoip:private"}, "outboundTag": "BLOCK"},
			},
		}
	case panelCatchAllDirect:
		cfg["routing"] = map[string]any{
			"rules": []any{
				map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "DIRECT"},
			},
		}
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestApplyAPIBlocksProxyUserFromReachingControlPorts(t *testing.T) {
	const (
		apiPort    = 48001
		metricPort = 48002
	)

	variants := map[string]panelRouting{
		"noPanelRules":        panelNoRules,
		"panelPrivateBlock":   panelPrivateBlock,
		"panelCatchAllDirect": panelCatchAllDirect,
	}
	for name, routing := range variants {
		t.Run(name, func(t *testing.T) {
			cfg := loopbackProbeConfig(t, routing)
			if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
				t.Fatal(err)
			}

			queries := []destQuery{
				{inbound: userInboundTag, isIP: true, value: "127.0.0.1", port: metricPort},
				{inbound: userInboundTag, isIP: true, value: "127.0.0.1", port: apiPort}, {inbound: userInboundTag, isIP: true, value: "127.0.0.53", port: apiPort},
				{inbound: userInboundTag, isIP: false, value: "localhost", port: apiPort},
				{inbound: userInboundTag, isIP: false, value: "localhost", port: metricPort},
			}

			for _, source := range apiRuleSources() {
				ip := net.ParseIP(source)
				if ip == nil || ip.IsLoopback() {
					continue
				}
				queries = append(queries,
					destQuery{inbound: userInboundTag, isIP: true, value: source, port: apiPort},
					destQuery{inbound: userInboundTag, isIP: true, value: source, port: metricPort},
				)
			}

			for _, q := range queries {
				tag, protocol := resolveOutbound(t, cfg, q)
				form := "ip"
				if !q.isIP {
					form = "domain"
				}
				if !destinationIsBlocked(protocol) {
					t.Errorf("a proxy user on %q reached the control port: dest %s %q:%d routed to outbound %q (protocol %q); expected it to be blackholed",
						userInboundTag, form, q.value, q.port, tag, protocol)
				}
			}
		})
	}
}

func TestApplyAPIControlPortGuardSurvivesSerialization(t *testing.T) {
	const (
		apiPort    = 48001
		metricPort = 48002
	)

	cfg := loopbackProbeConfig(t, panelPrivateBlock)
	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	clone, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}

	bytesConfig, err := clone.ToBytes()
	if err != nil {
		t.Fatal(err)
	}

	var serialized Config
	if err := json.Unmarshal(bytesConfig, &serialized); err != nil {
		t.Fatal(err)
	}

	q := destQuery{inbound: userInboundTag, isIP: false, value: "localhost", port: apiPort}
	tag, protocol := resolveOutbound(t, &serialized, q)
	if !destinationIsBlocked(protocol) {
		t.Fatalf("after Clone+ToBytes the control-port guard was gone: localhost:%d routed to %q (%q)", apiPort, tag, protocol)
	}

	const expectedAPIGuardRuleTag = "PG_NODE_API_GUARD"
	guardSeen := false
	for _, raw := range serialized.RouterConfig.RuleList {
		if strings.Contains(string(raw), expectedAPIGuardRuleTag) {
			guardSeen = true
			break
		}
	}
	if !guardSeen {
		t.Fatalf("expected %s rule to survive serialization", expectedAPIGuardRuleTag)
	}
}

func TestApplyAPIControlPortGuardIsSingleAndTargetsANodeBlackhole(t *testing.T) {
	const (
		apiPort    = 48001
		metricPort = 48002
		guardTag   = "PG_NODE_API_GUARD"
		blockTag   = "PG_NODE_API_GUARD_BLOCK"
	)

	for name, routing := range map[string]panelRouting{
		"noPanelRules":        panelNoRules,
		"panelCatchAllDirect": panelCatchAllDirect,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loopbackProbeConfig(t, routing)
			for range 3 {
				if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
					t.Fatal(err)
				}
			}

			guardRules := 0
			for _, raw := range cfg.RouterConfig.RuleList {
				var r resolvedRule
				if err := json.Unmarshal(raw, &r); err != nil {
					t.Fatal(err)
				}
				if r.RuleTag == guardTag {
					guardRules++
					if r.OutboundTag != blockTag {
						t.Errorf("guard rule targets %q, want %q", r.OutboundTag, blockTag)
					}
				}
			}
			if guardRules != 2 {
				t.Errorf("guard rules after three ApplyAPI calls = %d, want 2", guardRules)
			}

			outbounds, _ := cfg.OutboundConfigs.([]any)
			blocks := 0
			for _, outbound := range outbounds {
				obj, _ := outbound.(map[string]any)
				if tag, _ := obj["tag"].(string); tag == blockTag {
					blocks++
					if protocol, _ := obj["protocol"].(string); protocol != "blackhole" {
						t.Errorf("node guard outbound protocol = %q, want blackhole", protocol)
					}
				}
			}
			if blocks != 1 {
				t.Errorf("node guard outbounds = %d, want 1", blocks)
			}
			if first, _ := outbounds[0].(map[string]any); first["tag"] != "DIRECT" {
				t.Errorf("default outbound changed to %v", first["tag"])
			}

			if tag := cfg.blackholeOutboundTag(); tag != "BLOCK" {
				t.Errorf("malformed-domain guard target = %q, want the panel's BLOCK", tag)
			}
		})
	}
}

func TestApplyAPIControlPortGuardLeavesOtherDestinationsAlone(t *testing.T) {
	const (
		apiPort    = 48001
		metricPort = 48002
	)

	cfg := loopbackProbeConfig(t, panelNoRules)
	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	for _, q := range []destQuery{
		{inbound: userInboundTag, isIP: true, value: "198.51.100.7", port: apiPort},
		{inbound: userInboundTag, isIP: false, value: "example.com", port: apiPort},
		{inbound: userInboundTag, isIP: true, value: "127.0.0.1", port: 8080},
		{inbound: userInboundTag, isIP: false, value: "localhost", port: 8080},
		{inbound: userInboundTag, isIP: false, value: "example.com", port: 443},
	} {
		if tag, protocol := resolveOutbound(t, cfg, q); protocol != "freedom" {
			t.Errorf("%q:%d now routes to %q (%s), want the panel's freedom outbound", q.value, q.port, tag, protocol)
		}
	}

	if tag, _ := resolveOutbound(t, cfg, destQuery{inbound: "API_INBOUND", isIP: true, value: "127.0.0.1", port: apiPort}); tag != "API" {
		t.Errorf("API_INBOUND traffic routes to %q, want API", tag)
	}
}
