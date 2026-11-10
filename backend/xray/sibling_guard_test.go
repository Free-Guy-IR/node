package xray

import (
	"encoding/json"
	"testing"
)

func siblingProbeConfig(t *testing.T, panelHasSiblingPort bool, siblingPort int) *Config {
	t.Helper()

	rules := []any{}
	if panelHasSiblingPort {
		rules = append(rules, map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "DIRECT"})
	}

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag": userInboundTag, "listen": "0.0.0.0", "port": 12345,
				"protocol": "vless", "settings": map[string]any{"clients": []any{}, "decryption": "none"},
			},
			map[string]any{
				"tag": "PANEL TUNNEL", "listen": "0.0.0.0", "port": 12346,
				"protocol": "dokodemo-door",
				"settings": map[string]any{"address": "127.0.0.1", "port": siblingPort, "network": "tcp"},
			},
		},
		"outbounds": []any{
			map[string]any{"tag": "DIRECT", "protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []any{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}}}}},
			map[string]any{"tag": "BLOCK", "protocol": "blackhole"},
		},
		"routing": map[string]any{"rules": rules},
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

func TestApplyAPIBlocksUserInboundFromSiblingLoopbackPort(t *testing.T) {
	const (
		apiPort     = 48001
		metricPort  = 48002
		siblingPort = 39000
	)

	cfg := siblingProbeConfig(t, true, siblingPort)
	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	for _, q := range []destQuery{
		{inbound: userInboundTag, isIP: true, value: "127.0.0.1", port: siblingPort},
		{inbound: userInboundTag, isIP: true, value: "127.0.0.1", port: 9090},
		{inbound: userInboundTag, isIP: true, value: "0.0.0.0", port: siblingPort},
		{inbound: userInboundTag, isIP: true, value: "169.254.1.2", port: siblingPort},
		{inbound: userInboundTag, isIP: false, value: "localhost", port: siblingPort},
		{inbound: userInboundTag, isIP: false, value: "sub.localhost", port: siblingPort},
	} {
		tag, protocol := resolveOutbound(t, cfg, q)
		form := "ip"
		if !q.isIP {
			form = "domain"
		}
		if !destinationIsBlocked(protocol) {
			t.Errorf("a user on %q reached a sibling loopback service: dest %s %q:%d routed to %q (%s); want blackholed",
				userInboundTag, form, q.value, q.port, tag, protocol)
		}
	}
}

func TestSiblingGuardLeavesPanelTunnelAndRealDestinationsAlone(t *testing.T) {
	const (
		apiPort     = 48001
		metricPort  = 48002
		siblingPort = 39000
	)

	cfg := siblingProbeConfig(t, true, siblingPort)
	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	if tag, protocol := resolveOutbound(t, cfg, destQuery{inbound: "PANEL TUNNEL", isIP: true, value: "127.0.0.1", port: siblingPort}); protocol != "freedom" {
		t.Errorf("the panel's dokodemo-door tunnel to loopback now routes to %q (%s), want the freedom outbound", tag, protocol)
	}

	for _, q := range []destQuery{
		{inbound: userInboundTag, isIP: true, value: "198.51.100.9", port: siblingPort},
		{inbound: userInboundTag, isIP: false, value: "example.com", port: 443},
		{inbound: userInboundTag, isIP: true, value: "8.8.8.8", port: 53},
	} {
		if tag, protocol := resolveOutbound(t, cfg, q); protocol != "freedom" {
			t.Errorf("a real destination %q:%d now routes to %q (%s), want freedom", q.value, q.port, tag, protocol)
		}
	}
}

func TestSiblingGuardIsIdempotentAndSurvivesSerialization(t *testing.T) {
	const (
		apiPort     = 48001
		metricPort  = 48002
		siblingPort = 39000
	)

	cfg := siblingProbeConfig(t, false, siblingPort)
	for range 3 {
		if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
			t.Fatal(err)
		}
	}

	clone, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := clone.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var serialized Config
	if err := json.Unmarshal(raw, &serialized); err != nil {
		t.Fatal(err)
	}

	siblingRules := 0
	for _, rawRule := range serialized.RouterConfig.RuleList {
		var r resolvedRule
		if err := json.Unmarshal(rawRule, &r); err != nil {
			t.Fatal(err)
		}
		if r.RuleTag == siblingGuardIPRuleTag || r.RuleTag == siblingGuardDomainRuleTag {
			siblingRules++
		}
	}
	if siblingRules != 2 {
		t.Fatalf("sibling guard rules after three ApplyAPI calls and a clone = %d, want 2", siblingRules)
	}

	q := destQuery{inbound: userInboundTag, isIP: false, value: "localhost", port: siblingPort}
	if tag, protocol, err := routeFor(&serialized, q); err != nil || !destinationIsBlocked(protocol) {
		t.Fatalf("after clone+ToBytes a user to localhost:%d routed to %q (%s) err %v", siblingPort, tag, protocol, err)
	}
}
