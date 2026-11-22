package xray

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	privateGuardAPIPort    = 30001
	privateGuardMetricPort = 30002
)

func privateGuardVless(tag string, port int) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "vless",
		"settings": map[string]any{"clients": []any{}, "decryption": "none"},
	}
}

func privateGuardRule(fields map[string]any) map[string]any {
	rule := map[string]any{"type": "field"}
	maps.Copy(rule, fields)
	return rule
}

func coreOneShape(extraRules ...map[string]any) map[string]any {
	rules := []any{
		privateGuardRule(map[string]any{"port": "25", "outboundTag": "BLOCK"}),
		privateGuardRule(map[string]any{"network": "udp", "port": "443", "ip": []any{"1.1.1.1/32"}, "outboundTag": "BLOCK"}),
		privateGuardRule(map[string]any{"ip": []any{"100.64.0.0/10"}, "outboundTag": "DIRECT"}),
		privateGuardRule(map[string]any{"ip": []any{"geoip:private"}, "outboundTag": "BLOCK"}),
		privateGuardRule(map[string]any{"inboundTag": []any{"egress-in"}, "outboundTag": "egress_wireguard"}),
		privateGuardRule(map[string]any{"inboundTag": []any{"direct-in"}, "outboundTag": "DIRECT"}),
	}
	for _, rule := range extraRules {
		rules = append(rules, rule)
	}
	return map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			privateGuardVless("VLESS TCP SESSION", 21001),
			privateGuardVless("egress-in", 21002),
			privateGuardVless("direct-in", 21003),
			map[string]any{"tag": "PANEL TUNNEL", "listen": "127.0.0.1", "port": 21004, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1", "port": 39000, "network": "tcp"}},
		},
		"outbounds": []any{
			map[string]any{"tag": "DIRECT", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIPv4"}},
			map[string]any{"tag": "egress_wireguard", "protocol": "freedom", "settings": map[string]any{}, "streamSettings": map[string]any{"sockopt": map[string]any{"interface": "wg-egress"}}},
			map[string]any{"tag": "vless_canada", "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "relay.example.com", "port": 443, "users": []any{map[string]any{"id": "11111111-2222-3333-4444-555555555555", "encryption": "none"}}}}}},
			map[string]any{"tag": "BLOCK", "protocol": "blackhole", "settings": map[string]any{}},
		},
		"routing": map[string]any{"domainStrategy": "IPIfNonMatch", "rules": rules},
	}
}

func privateGuardConfig(t *testing.T, shape map[string]any) *Config {
	t.Helper()
	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyAPI(privateGuardAPIPort, privateGuardMetricPort); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func renderedOutbounds(t *testing.T, cfg *Config) map[string]map[string]any {
	t.Helper()
	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &rendered); err != nil {
		t.Fatal(err)
	}
	byTag := make(map[string]map[string]any, len(rendered.Outbounds))
	for _, outbound := range rendered.Outbounds {
		tag, _ := outbound["tag"].(string)
		byTag[tag] = outbound
	}
	return byTag
}

func privateAllowOf(outbound map[string]any) any {
	settings, _ := outbound["settings"].(map[string]any)
	return settings["privateAllow"]
}

func jsonValue(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func renderedRules(t *testing.T, cfg *Config) []map[string]any {
	t.Helper()
	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var rendered struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &rendered); err != nil {
		t.Fatal(err)
	}
	return rendered.Routing.Rules
}

func ruleSendingTo(rules []map[string]any, cidr, outbound string) map[string]any {
	for _, rule := range rules {
		if rule["outboundTag"] != outbound {
			continue
		}
		if ips, ok := rule["ip"].([]any); ok && len(ips) > 0 && ips[0] == cidr {
			return rule
		}
	}
	return nil
}

func TestTheNodeRendersCoreOnesCGNATRouteAsAPrivateAllowOnDIRECTOnly(t *testing.T) {
	cfg := privateGuardConfig(t, coreOneShape())
	outbounds := renderedOutbounds(t, cfg)
	if got, want := privateAllowOf(outbounds["DIRECT"]), jsonValue(t, `[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}]`); !reflect.DeepEqual(got, want) {
		t.Fatalf("DIRECT renders privateAllow %v, want %v", got, want)
	}
	settings, _ := outbounds["DIRECT"]["settings"].(map[string]any)
	if settings["domainStrategy"] != "UseIPv4" {
		t.Errorf("DIRECT lost its own settings: %v", settings)
	}
	for _, tag := range []string{"egress_wireguard", "vless_canada", "BLOCK", apiGuardOutboundTag} {
		if got := privateAllowOf(outbounds[tag]); got != nil {
			t.Errorf("%s renders privateAllow %v, want none", tag, got)
		}
	}
	cgnat := ruleSendingTo(renderedRules(t, cfg), "100.64.0.0/10", "DIRECT")
	if cgnat == nil || cgnat["ruleTag"] != "PG_NODE_PRIVATE_ALLOW_1" {
		t.Fatalf("the rendered CGNAT rule is %v, want it tagged PG_NODE_PRIVATE_ALLOW_1", cgnat)
	}
	for _, rule := range renderedRules(t, cfg) {
		if tag, _ := rule["ruleTag"].(string); strings.HasPrefix(tag, "PG_NODE_PRIVATE_ALLOW_") && rule["outboundTag"] != "DIRECT" {
			t.Errorf("a rule that allows nothing was tagged: %v", rule)
		}
	}
	for _, raw := range cfg.RouterConfig.RuleList {
		if strings.Contains(string(raw), "PG_NODE_PRIVATE_ALLOW_") {
			t.Errorf("rendering tagged the stored rule %s", raw)
		}
	}
}

func TestOnlyExplicitPrivateRoutesToAFreedomOutboundBecomeAllows(t *testing.T) {
	cases := []struct {
		name  string
		rules []map[string]any
		want  string
	}{
		{"a rule's own conditions stay in routing", []map[string]any{{"inboundTag": []any{"relay-in"}, "network": "tcp", "port": "22,443", "user": []any{"admin"}, "ip": []any{"10.8.0.0/16"}, "outboundTag": "egress_wireguard"}},
			`[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_2", "ip": ["10.8.0.0/16"]}]`},
		{"several literals", []map[string]any{{"ip": []any{"fd00::/8", "10.9.0.1", "1.1.1.1"}, "outboundTag": "egress_wireguard"}},
			`[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_2", "ip": ["fd00::/8", "10.9.0.1"]}]`},
		{"a comma-separated list", []map[string]any{{"ip": "10.10.0.0/16,1.1.1.1", "outboundTag": "egress_wireguard"}},
			`[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_2", "ip": ["10.10.0.0/16"]}]`},
		{"the rule's own tag in any letter case", []map[string]any{{"IP": []any{"10.11.0.0/16"}, "OutboundTag": "egress_wireguard", "RuleTag": "upper"}},
			`[{"ruleTag": "upper", "ip": ["10.11.0.0/16"]}]`},
		{"a source, local address or protocol condition", []map[string]any{{"source": []any{"10.0.0.0/8"}, "localIP": []any{"10.0.0.1"}, "protocol": []any{"tls"}, "ip": []any{"10.12.0.0/16"}, "outboundTag": "egress_wireguard"}},
			`[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_2", "ip": ["10.12.0.0/16"]}]`},
		{"a generated tag skips one the panel uses", []map[string]any{{"ruleTag": "PG_NODE_PRIVATE_ALLOW_2", "port": "1", "outboundTag": "BLOCK"}, {"ip": []any{"10.13.0.0/16"}, "outboundTag": "egress_wireguard"}},
			`[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_3", "ip": ["10.13.0.0/16"]}]`},
		{"a ruleTag other rules share", []map[string]any{{"ruleTag": "shared", "port": "1", "outboundTag": "BLOCK"}, {"ruleTag": "shared", "ip": []any{"10.14.0.0/16"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a domain condition", []map[string]any{{"domain": []any{"full:intranet.example.com"}, "ip": []any{"10.15.0.0/16"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a capitalised domain condition", []map[string]any{{"Domain": []any{"full:intranet.example.com"}, "ip": []any{"10.15.0.0/16"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a catch-all", []map[string]any{{"ip": []any{"0.0.0.0/0", "::/0"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a range wider than a private prefix", []map[string]any{{"ip": []any{"100.64.0.0/9", "fc00::/6"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a negation", []map[string]any{{"ip": []any{"!10.0.0.0/8"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a country list", []map[string]any{{"ip": []any{"geoip:ir"}, "outboundTag": "egress_wireguard"}}, ``},
		{"an external list", []map[string]any{{"ip": []any{"ext:private.dat:lan"}, "outboundTag": "egress_wireguard"}}, ``},
		{"link-local, kept off by the node's own guard", []map[string]any{{"ip": []any{"169.254.0.0/16", "fe80::/10"}, "outboundTag": "egress_wireguard"}}, ``},
		{"the local host", []map[string]any{{"ip": []any{"127.0.0.1/8", "::1"}, "outboundTag": "egress_wireguard"}}, ``},
		{"a balancer", []map[string]any{{"ip": []any{"10.16.0.0/16"}, "balancerTag": "egress"}}, ``},
		{"a block outbound", []map[string]any{{"ip": []any{"10.17.0.0/16"}, "outboundTag": "BLOCK"}}, ``},
		{"a proxy outbound", []map[string]any{{"ip": []any{"10.18.0.0/16"}, "outboundTag": "vless_canada"}}, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			extra := make([]map[string]any, 0, len(c.rules))
			for _, rule := range c.rules {
				extra = append(extra, privateGuardRule(rule))
			}
			cfg := privateGuardConfig(t, coreOneShape(extra...))
			got := privateAllowOf(renderedOutbounds(t, cfg)["egress_wireguard"])
			if c.want == "" {
				if got != nil {
					t.Fatalf("egress_wireguard renders privateAllow %v, want none", got)
				}
				return
			}
			if want := jsonValue(t, c.want); !reflect.DeepEqual(got, want) {
				t.Fatalf("egress_wireguard renders privateAllow %v, want %v", got, want)
			}
			entry := got.([]any)[0].(map[string]any)
			tagged := 0
			for _, rule := range renderedRules(t, cfg) {
				if rule["ruleTag"] == entry["ruleTag"] || rule["RuleTag"] == entry["ruleTag"] {
					tagged++
					if c.name == "a rule's own conditions stay in routing" && (rule["inboundTag"] == nil || rule["port"] != "22,443" || rule["user"] == nil) {
						t.Errorf("the tagged rule lost its conditions: %v", rule)
					}
				}
			}
			if tagged != 1 {
				t.Errorf("%d rendered rules carry the tag %v, want exactly one", tagged, entry["ruleTag"])
			}
		})
	}
}

func TestARoutedGeoIPPrivateBecomesTheNodesPrivateListWithoutTheGuardedRanges(t *testing.T) {
	outbounds := renderedOutbounds(t, privateGuardConfig(t, coreOneShape(privateGuardRule(map[string]any{"inboundTag": []any{"lan-in"}, "ip": []any{"geoip:private"}, "outboundTag": "egress_wireguard"}))))
	entries, _ := privateAllowOf(outbounds["egress_wireguard"]).([]any)
	if len(entries) != 1 {
		t.Fatalf("egress_wireguard renders privateAllow %v, want one entry", entries)
	}
	entry, _ := entries[0].(map[string]any)
	var ips []string
	for _, ip := range entry["ip"].([]any) {
		ips = append(ips, ip.(string))
	}
	want := []string{"10.0.0.0/8", "100.64.0.0/10", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "fc00::/7"}
	if strings.Join(ips, " ") != strings.Join(want, " ") || entry["ruleTag"] != "PG_NODE_PRIVATE_ALLOW_2" {
		t.Fatalf("a routed geoip:private renders %v under %v, want %v under PG_NODE_PRIVATE_ALLOW_2", ips, entry["ruleTag"], want)
	}
}

func TestTheNodeOwnsPrivateAllow(t *testing.T) {
	shape := coreOneShape()
	outbounds := shape["outbounds"].([]any)
	outbounds[0] = map[string]any{"tag": "DIRECT", "protocol": "Freedom", "settings": map[string]any{
		"domainStrategy": "UseIPv4",
		"privateAllow":   []any{map[string]any{"ruleTag": "x", "ip": []any{"10.0.0.0/8"}}},
		"PrivateAllow":   []any{map[string]any{"ruleTag": "x", "ip": []any{"172.16.0.0/12"}}},
		"privateallow":   []any{map[string]any{"ruleTag": "x", "ip": []any{"192.168.0.0/16"}}},
	}}
	outbounds[1] = map[string]any{"tag": "egress_wireguard", "protocol": "direct", "settings": map[string]any{"PRIVATEALLOW": []any{map[string]any{"ruleTag": "x", "ip": []any{"10.0.0.0/8"}}}}}
	cfg := privateGuardConfig(t, shape)
	rendered := renderedOutbounds(t, cfg)
	settings, _ := rendered["DIRECT"]["settings"].(map[string]any)
	for key := range settings {
		if strings.EqualFold(key, "privateAllow") && key != "privateAllow" {
			t.Errorf("DIRECT still carries the panel's %q", key)
		}
	}
	if got, want := settings["privateAllow"], jsonValue(t, `[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}]`); !reflect.DeepEqual(got, want) {
		t.Errorf("DIRECT renders privateAllow %v, want %v", got, want)
	}
	if got, _ := rendered["egress_wireguard"]["settings"].(map[string]any); len(got) != 0 {
		t.Errorf("egress_wireguard keeps the panel's settings %v, want none", got)
	}
	stored, _ := cfg.OutboundConfigs.([]any)[0].(map[string]any)["settings"].(map[string]any)
	if len(stored) != 4 {
		t.Errorf("rendering changed the stored panel settings to %v", stored)
	}
}

func TestEveryRenderDerivesTheAllowFromItsOwnRules(t *testing.T) {
	filterRule := privateGuardRule(map[string]any{"ip": []any{"10.20.0.0/16"}, "outboundTag": "DIRECT", "ruleTag": "pgcf-7-allow"})
	cfg := privateGuardConfig(t, coreOneShape(filterRule))
	want := jsonValue(t, `[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}, {"ruleTag": "pgcf-7-allow", "ip": ["10.20.0.0/16"]}]`)
	if got := privateAllowOf(renderedOutbounds(t, cfg)["DIRECT"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("DIRECT renders privateAllow %v, want %v", got, want)
	}
	clone, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if stored := privateAllowOf(clone.OutboundConfigs.([]any)[0].(map[string]any)); stored != nil {
		t.Errorf("a clone stores privateAllow %v, want the panel's outbound", stored)
	}
	if got := privateAllowOf(renderedOutbounds(t, clone)["DIRECT"]); !reflect.DeepEqual(got, want) {
		t.Errorf("a clone renders privateAllow %v, want %v", got, want)
	}
	if removed := stripContentFilterRules(clone); len(removed) != 1 {
		t.Fatalf("stripContentFilterRules removed %v", removed)
	}
	if got, want := privateAllowOf(renderedOutbounds(t, clone)["DIRECT"]), jsonValue(t, `[{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}]`); !reflect.DeepEqual(got, want) {
		t.Errorf("after the content-filter rules are stripped DIRECT renders privateAllow %v, want %v", got, want)
	}
	reparsed, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewConfig(string(reparsed), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := privateAllowOf(renderedOutbounds(t, again)["DIRECT"]); !reflect.DeepEqual(got, want) {
		t.Errorf("a config read back from its own render renders privateAllow %v, want %v", got, want)
	}
}

func TestAStockCoreLoadsTheRenderedConfig(t *testing.T) {
	binary := os.Getenv("XRAY_STOCK_EXECUTABLE_PATH")
	if binary == "" {
		t.Skip("XRAY_STOCK_EXECUTABLE_PATH names no stock xray to load the rendered config")
	}
	raw, err := privateGuardConfig(t, coreOneShape()).ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"privateAllow"`) {
		t.Fatal("the rendered config carries no privateAllow")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(binary, "run", "-test", "-c", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Configuration OK") {
		t.Fatalf("%s refuses the rendered config: %v\n%s", binary, err, out)
	}
}
