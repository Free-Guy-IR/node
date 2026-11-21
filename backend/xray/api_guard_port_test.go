package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

const (
	portGuardAPIPort      = 48201
	portGuardMetricPort   = 48202
	portGuardBlackhole    = "PG_NODE_API_GUARD_BLOCK"
	portGuardLoopbackName = "apiguard-lab.test"
	portGuardNodeIP       = "203.0.113.13"
	portGuardClientIP     = "198.51.100.30"
)

type portGuardCore struct {
	name     string
	userTags []string
	config   map[string]any
}

type portGuardProbe struct {
	inbound  string
	localIP  string
	sourceIP string
	ip       string
	domain   string
	port     int
}

func (p portGuardProbe) target() string {
	if p.domain != "" {
		return fmt.Sprintf("%s:%d", p.domain, p.port)
	}
	return fmt.Sprintf("%s:%d", p.ip, p.port)
}

type portGuardDecision struct {
	outboundTag string
	protocol    string
	pass        int
}

func portGuardVless(tag string, port int) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "0.0.0.0", "port": port, "protocol": "vless",
		"settings":       map[string]any{"clients": []any{}, "decryption": "none"},
		"streamSettings": map[string]any{"network": "tcp", "security": "none"},
	}
}

func portGuardVmess(tag string, port int) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "0.0.0.0", "port": port, "protocol": "vmess",
		"settings":       map[string]any{"clients": []any{}},
		"streamSettings": map[string]any{"network": "ws", "wsSettings": map[string]any{"path": "/vm"}},
	}
}

func portGuardTrojan(tag string, port int) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "0.0.0.0", "port": port, "protocol": "trojan",
		"settings":       map[string]any{"clients": []any{}},
		"streamSettings": map[string]any{"network": "tcp", "security": "none"},
	}
}

func portGuardTunnel(tag string, port, target int) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "0.0.0.0", "port": port, "protocol": "dokodemo-door",
		"settings": map[string]any{"address": "127.0.0.1", "port": target, "network": "tcp"},
	}
}

func portGuardFreedom(tag string) map[string]any {
	return map[string]any{"tag": tag, "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIPv4"}}
}

func portGuardRule(fields map[string]any) map[string]any {
	rule := map[string]any{"type": "field"}
	maps.Copy(rule, fields)
	return rule
}

func portGuardCores() []portGuardCore {
	mainUsers := []any{"VLESS TCP NOTLS", "VMESS WS NOTLS", "TROJAN TCP NOTLS"}
	commonInbounds := func(extra ...any) []any {
		inbounds := []any{
			portGuardVless("VLESS TCP NOTLS", 20001),
			portGuardVmess("VMESS WS NOTLS", 20002),
			portGuardTrojan("TROJAN TCP NOTLS", 20003),
		}
		inbounds = append(inbounds, extra...)
		return append(inbounds, portGuardTunnel("PANEL TUNNEL", 20009, 39000))
	}

	return []portGuardCore{
		{
			name:     "inboundTagAndLocalIPFreedomRules",
			userTags: []string{"VLESS TCP NOTLS", "VMESS WS NOTLS", "TROJAN TCP NOTLS", "yt-clean-in", "gemini-direct"},
			config: map[string]any{
				"log": map[string]any{"loglevel": "warning"},
				"inbounds": commonInbounds(
					portGuardVless("yt-clean-in", 20004),
					portGuardVless("gemini-direct", 20005),
				),
				"outbounds": []any{
					portGuardFreedom("DIRECT"),
					map[string]any{
						"tag": "usa_wireguard", "protocol": "freedom",
						"settings":       map[string]any{"domainStrategy": "UseIPv4"},
						"streamSettings": map[string]any{"sockopt": map[string]any{"interface": "wg-usa"}},
					},
					map[string]any{"tag": "BLOCK", "protocol": "blackhole"},
				},
				"routing": map[string]any{
					"domainStrategy": "IPIfNonMatch",
					"rules": []any{
						portGuardRule(map[string]any{"inboundTag": []any{"yt-clean-in"}, "localIP": []any{portGuardNodeIP}, "outboundTag": "DIRECT"}),
						portGuardRule(map[string]any{"inboundTag": []any{"yt-clean-in"}, "outboundTag": "BLOCK"}),
						portGuardRule(map[string]any{"inboundTag": []any{"gemini-direct"}, "outboundTag": "usa_wireguard"}),
						portGuardRule(map[string]any{"ip": []any{"geoip:private"}, "outboundTag": "BLOCK"}),
						portGuardRule(map[string]any{"inboundTag": mainUsers, "outboundTag": "DIRECT"}),
					},
				},
			},
		},
		{
			name:     "ipIfNonMatchCatchAll",
			userTags: []string{"VLESS TCP NOTLS", "VMESS WS NOTLS", "TROJAN TCP NOTLS"},
			config: map[string]any{
				"log":       map[string]any{"loglevel": "warning"},
				"inbounds":  commonInbounds(),
				"outbounds": []any{portGuardFreedom("DIRECT"), map[string]any{"tag": "BLOCK", "protocol": "blackhole"}},
				"routing": map[string]any{
					"domainStrategy": "IPIfNonMatch",
					"rules": []any{
						portGuardRule(map[string]any{"ip": []any{"geoip:private"}, "outboundTag": "BLOCK"}),
						portGuardRule(map[string]any{"network": "tcp,udp", "outboundTag": "DIRECT"}),
					},
				},
			},
		},
		{
			name:     "asIsDefaultFreedom",
			userTags: []string{"VLESS TCP NOTLS", "VMESS WS NOTLS", "TROJAN TCP NOTLS"},
			config: map[string]any{
				"log":       map[string]any{"loglevel": "warning"},
				"inbounds":  commonInbounds(),
				"outbounds": []any{portGuardFreedom("DIRECT"), map[string]any{"tag": "BLOCK", "protocol": "blackhole"}},
				"routing": map[string]any{
					"domainStrategy": "AsIs",
					"rules": []any{
						portGuardRule(map[string]any{"ip": []any{"geoip:private"}, "outboundTag": "BLOCK"}),
					},
				},
			},
		},
	}
}

func portGuardConfig(t *testing.T, shape map[string]any) *Config {
	t.Helper()

	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func portGuardRendered(t *testing.T, core portGuardCore) *Config {
	t.Helper()

	cfg := portGuardConfig(t, core.config)
	if err := cfg.ApplyAPI(portGuardAPIPort, portGuardMetricPort); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func portGuardRules(t *testing.T, cfg *Config) []map[string]any {
	t.Helper()

	if cfg.RouterConfig == nil {
		return nil
	}
	rules := make([]map[string]any, 0, len(cfg.RouterConfig.RuleList))
	for _, raw := range cfg.RouterConfig.RuleList {
		var rule map[string]any
		if err := json.Unmarshal(raw, &rule); err != nil {
			t.Fatal(err)
		}
		rules = append(rules, rule)
	}
	return rules
}

func portGuardFirstRule(t *testing.T, cfg *Config) map[string]any {
	t.Helper()

	rules := portGuardRules(t, cfg)
	if len(rules) == 0 {
		t.Fatal("the rendered config has no routing rules")
	}
	return rules[0]
}

func portGuardStrings(t *testing.T, value any) []string {
	t.Helper()

	switch v := value.(type) {
	case string:
		return strings.Split(v, ",")
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				t.Fatalf("non-string list entry %#v", item)
			}
			out = append(out, s)
		}
		return out
	default:
		t.Fatalf("unsupported list value %#v", value)
		return nil
	}
}

func portGuardAnyIP(list, ips []string) bool {
	for _, ip := range ips {
		if ipMatchesList(list, ip) {
			return true
		}
	}
	return false
}

var portGuardModelledFields = map[string]bool{
	"type": false, "ruleTag": false, "outboundTag": false,
	"inboundTag": true, "network": true, "port": true, "ip": true,
	"domain": true, "domains": true, "localIP": true, "source": true, "sourceIP": true,
}

func portGuardMatches(t *testing.T, rule map[string]any, probe portGuardProbe, targetIPs []string) bool {
	t.Helper()

	for key := range rule {
		if _, known := portGuardModelledFields[key]; !known {
			t.Fatalf("the routing model does not evaluate rule field %q in %v", key, rule)
		}
	}

	for key, value := range rule {
		if !portGuardModelledFields[key] {
			continue
		}
		if key == "network" {
			if !networkMatches(value, "tcp") {
				return false
			}
			continue
		}
		if key == "port" {
			ports, _ := parsePortSpec(value)
			if _, ok := ports[probe.port]; !ok {
				return false
			}
			continue
		}
		list := portGuardStrings(t, value)
		if len(list) == 0 {
			continue
		}
		switch key {
		case "inboundTag":
			if probe.inbound == "" || !slices.Contains(list, probe.inbound) {
				return false
			}
		case "ip":
			if !portGuardAnyIP(list, targetIPs) {
				return false
			}
		case "domain", "domains":
			if probe.domain == "" || !domainMatchesList(list, probe.domain) {
				return false
			}
		case "localIP":
			if !portGuardAnyIP(list, []string{probe.localIP}) {
				return false
			}
		case "source", "sourceIP":
			if !portGuardAnyIP(list, []string{probe.sourceIP}) {
				return false
			}
		}
	}
	return true
}

func portGuardRoute(t *testing.T, cfg *Config, probe portGuardProbe, resolve map[string][]string) portGuardDecision {
	t.Helper()

	strategy := ""
	if cfg.RouterConfig != nil && cfg.RouterConfig.DomainStrategy != nil {
		strategy = strings.ToLower(*cfg.RouterConfig.DomainStrategy)
	}

	var literal []string
	if probe.ip != "" {
		literal = []string{probe.ip}
	}
	resolved := literal
	if probe.domain != "" {
		resolved = resolve[probe.domain]
	}

	passes := [][]string{literal}
	switch strategy {
	case "ipondemand":
		passes = [][]string{resolved}
	case "ipifnonmatch":
		if probe.domain != "" {
			passes = append(passes, resolved)
		}
	}

	protocols := cfg.outboundProtocolByTag()
	rules := portGuardRules(t, cfg)
	for i, targetIPs := range passes {
		for _, rule := range rules {
			if portGuardMatches(t, rule, probe, targetIPs) {
				tag, _ := rule["outboundTag"].(string)
				return portGuardDecision{outboundTag: tag, protocol: protocols[tag], pass: i + 1}
			}
		}
	}

	defaultTag := ""
	if outbounds, ok := cfg.OutboundConfigs.([]any); ok && len(outbounds) > 0 {
		if first, ok := outbounds[0].(map[string]any); ok {
			defaultTag, _ = first["tag"].(string)
		}
	}
	return portGuardDecision{outboundTag: defaultTag, protocol: protocols[defaultTag]}
}

func portGuardBlackholes(t *testing.T, cfg *Config) int {
	t.Helper()

	outbounds, _ := cfg.OutboundConfigs.([]any)
	count := 0
	for _, outbound := range outbounds {
		obj, _ := outbound.(map[string]any)
		if tag, _ := obj["tag"].(string); tag == portGuardBlackhole {
			if protocol, _ := obj["protocol"].(string); protocol != "blackhole" {
				t.Errorf("%s has protocol %q, want blackhole", portGuardBlackhole, protocol)
			}
			count++
		}
	}
	return count
}

func requirePortGuardFirst(t *testing.T, cfg *Config) map[string]any {
	t.Helper()

	first := portGuardFirstRule(t, cfg)
	keys := slices.Sorted(maps.Keys(first))
	if want := []string{"inboundTag", "outboundTag", "port", "ruleTag", "type"}; !slices.Equal(keys, want) {
		t.Fatalf("the first routing rule has fields %v, want only %v so it decides before any name is resolved: %v", keys, want, first)
	}
	if tag, _ := first["ruleTag"].(string); !nodeGuardRuleTag(tag) {
		t.Fatalf("the first routing rule carries ruleTag %q, which is not a node guard tag, so a re-render would keep a stale copy", tag)
	}
	if first["outboundTag"] != portGuardBlackhole {
		t.Fatalf("the first routing rule targets %v, want %s", first["outboundTag"], portGuardBlackhole)
	}
	return first
}

func TestPortGuardIsTheFirstRuleOfEveryProductionShapedCore(t *testing.T) {
	for _, core := range portGuardCores() {
		t.Run(core.name, func(t *testing.T) {
			cfg := portGuardRendered(t, core)
			first := requirePortGuardFirst(t, cfg)

			covered := slices.Sorted(slices.Values(portGuardStrings(t, first["inboundTag"])))
			want := slices.Sorted(slices.Values(core.userTags))
			if !slices.Equal(covered, want) {
				t.Errorf("the port guard covers inbounds %q, want every user inbound %q and nothing else", covered, want)
			}
			if !slices.Equal(covered, cfg.userInboundTags()) {
				t.Errorf("the port guard covers %q, but the user inbounds of this config are %q", covered, cfg.userInboundTags())
			}

			ports, _ := parsePortSpec(first["port"])
			if want := map[int]struct{}{portGuardAPIPort: {}, portGuardMetricPort: {}}; !maps.Equal(ports, want) {
				t.Errorf("the port guard covers ports %v, want the api port %d and the metric port %d", slices.Sorted(maps.Keys(ports)), portGuardAPIPort, portGuardMetricPort)
			}

			if n := portGuardBlackholes(t, cfg); n != 1 {
				t.Errorf("%s outbounds = %d, want exactly 1", portGuardBlackhole, n)
			}

			t.Run("real xray accepts it", func(t *testing.T) { assertRealXrayAccepts(t, cfg) })
		})
	}
}

func TestPortGuardRefusesALoopbackNameInTheFirstRoutingPass(t *testing.T) {
	resolve := map[string][]string{portGuardLoopbackName: {"127.0.0.1"}}

	for _, core := range portGuardCores() {
		t.Run(core.name, func(t *testing.T) {
			cfg := portGuardRendered(t, core)

			for _, inbound := range core.userTags {
				for _, port := range []int{portGuardAPIPort, portGuardMetricPort} {
					for _, probe := range []portGuardProbe{
						{inbound: inbound, localIP: portGuardNodeIP, sourceIP: portGuardClientIP, domain: portGuardLoopbackName, port: port},
						{inbound: inbound, localIP: portGuardNodeIP, sourceIP: portGuardClientIP, ip: "127.0.0.1", port: port},
					} {
						got := portGuardRoute(t, cfg, probe, resolve)
						if got.outboundTag != portGuardBlackhole || got.pass != 1 {
							t.Errorf("a user on %q to %s routes to %q (%s) in pass %d; want %s in the first pass",
								inbound, probe.target(), got.outboundTag, got.protocol, got.pass, portGuardBlackhole)
						}
					}
				}
			}
		})
	}
}

func TestPortGuardLeavesTheAPIInboundRoutedToTheAPI(t *testing.T) {
	apiProbe := func(port int) portGuardProbe {
		return portGuardProbe{inbound: "API_INBOUND", localIP: "127.0.0.1", sourceIP: "127.0.0.1", ip: "127.0.0.1", port: port}
	}

	for _, core := range portGuardCores() {
		t.Run(core.name, func(t *testing.T) {
			cfg := portGuardRendered(t, core)
			first := requirePortGuardFirst(t, cfg)
			if covered := portGuardStrings(t, first["inboundTag"]); slices.Contains(covered, "API_INBOUND") {
				t.Fatalf("the port guard covers API_INBOUND: %q", covered)
			}
			for _, port := range []int{portGuardAPIPort, portGuardMetricPort} {
				if got := portGuardRoute(t, cfg, apiProbe(port), nil); got.outboundTag != "API" || got.pass != 1 {
					t.Errorf("the node's own API connection to port %d routes to %q in pass %d, want API in the first pass", port, got.outboundTag, got.pass)
				}
			}
		})
	}

	t.Run("noUserInbound", func(t *testing.T) {
		cfg := portGuardConfig(t, map[string]any{
			"log":       map[string]any{"loglevel": "warning"},
			"inbounds":  []any{portGuardTunnel("PANEL TUNNEL", 20009, 39000)},
			"outbounds": []any{portGuardFreedom("DIRECT")},
		})
		if err := cfg.ApplyAPI(portGuardAPIPort, portGuardMetricPort); err != nil {
			t.Fatal(err)
		}
		for _, rule := range portGuardRules(t, cfg) {
			if rule["outboundTag"] != portGuardBlackhole {
				continue
			}
			scoped := false
			if tags, ok := rule["inboundTag"]; ok {
				scoped = len(slices.DeleteFunc(portGuardStrings(t, tags), func(tag string) bool { return tag == "" })) > 0
			}
			_, byAddress := rule["ip"]
			_, byName := rule["domain"]
			if !scoped && !byAddress && !byName {
				t.Errorf("a node guard rule with no inbound scope and no destination condition would refuse the API itself: %v", rule)
			}
		}
		if got := portGuardRoute(t, cfg, apiProbe(portGuardAPIPort), nil); got.outboundTag != "API" {
			t.Errorf("with no user inbound the node's own API connection routes to %q, want API", got.outboundTag)
		}
	})
}

func TestPortGuardRenderIsIdempotent(t *testing.T) {
	for _, core := range portGuardCores() {
		t.Run(core.name, func(t *testing.T) {
			cfg := portGuardRendered(t, core)
			once, err := cfg.ToBytes()
			if err != nil {
				t.Fatal(err)
			}

			if err := cfg.ApplyAPI(portGuardAPIPort, portGuardMetricPort); err != nil {
				t.Fatal(err)
			}
			twice, err := cfg.ToBytes()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(once, twice) {
				t.Fatalf("a second ApplyAPI changed the rendered config:\nonce:  %s\ntwice: %s", once, twice)
			}

			reparsed, err := NewConfig(string(once), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := reparsed.ApplyAPI(portGuardAPIPort, portGuardMetricPort); err != nil {
				t.Fatal(err)
			}
			again, err := reparsed.ToBytes()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(once, again) {
				t.Fatalf("ApplyAPI over an already rendered config changed it:\nonce:  %s\nagain: %s", once, again)
			}

			first := requirePortGuardFirst(t, reparsed)
			tag, _ := first["ruleTag"].(string)
			copies := 0
			for _, rule := range portGuardRules(t, reparsed) {
				if rule["ruleTag"] == tag {
					copies++
				}
			}
			if copies != 1 {
				t.Fatalf("ruleTag %q appears %d times after re-rendering, want 1", tag, copies)
			}
			if n := portGuardBlackholes(t, reparsed); n != 1 {
				t.Fatalf("%s outbounds after re-rendering = %d, want 1", portGuardBlackhole, n)
			}
		})
	}
}

func TestPortGuardRefusesOnlyTheControlPortsAndOnlyOnUserInbounds(t *testing.T) {
	cfg := portGuardRendered(t, portGuardCores()[1])

	for _, probe := range []portGuardProbe{
		{inbound: "VLESS TCP NOTLS", sourceIP: portGuardClientIP, ip: "198.51.100.7", port: portGuardAPIPort},
		{inbound: "TROJAN TCP NOTLS", sourceIP: portGuardClientIP, domain: "example.com", port: portGuardMetricPort},
	} {
		if got := portGuardRoute(t, cfg, probe, nil); got.outboundTag != portGuardBlackhole || got.pass != 1 {
			t.Errorf("a user on %q to the remote %s routes to %q in pass %d; the control port numbers are refused for user inbounds, want %s",
				probe.inbound, probe.target(), got.outboundTag, got.pass, portGuardBlackhole)
		}
	}

	for _, probe := range []portGuardProbe{
		{inbound: "VLESS TCP NOTLS", sourceIP: portGuardClientIP, ip: "198.51.100.7", port: 443},
		{inbound: "VMESS WS NOTLS", sourceIP: portGuardClientIP, domain: "example.com", port: portGuardMetricPort + 1},
		{inbound: "PANEL TUNNEL", sourceIP: portGuardClientIP, ip: "198.51.100.7", port: portGuardAPIPort},
		{inbound: "PANEL TUNNEL", sourceIP: portGuardClientIP, domain: "example.com", port: portGuardMetricPort},
	} {
		if got := portGuardRoute(t, cfg, probe, nil); got.outboundTag != "DIRECT" {
			t.Errorf("%q to %s routes to %q (%s), want the panel's DIRECT", probe.inbound, probe.target(), got.outboundTag, got.protocol)
		}
	}
}
