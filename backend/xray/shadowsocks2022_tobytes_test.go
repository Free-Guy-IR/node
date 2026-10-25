package xray

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"

	"github.com/pasarguard/node/common"
)

const shadowsocks2022Config = `{"log":{"loglevel":"warning"},"inbounds":[
	{"tag":"aes128","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","network":"tcp,udp"}},
	{"tag":"aes256","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-256-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=","network":"tcp,udp"}},
	{"tag":"chacha","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-chacha20-poly1305","password":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=","network":"tcp,udp"}},
	{"tag":"legacy","protocol":"shadowsocks","settings":{"clients":[],"method":"aes-256-gcm","password":"legacy-server-password","network":"tcp,udp"}}
]}`

func serializedInboundSettings(t *testing.T, cfg *Config) map[string]map[string]any {
	t.Helper()
	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	var out struct {
		Inbounds []struct {
			Tag      string         `json:"tag"`
			Settings map[string]any `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode serialized config: %v", err)
	}
	byTag := make(map[string]map[string]any, len(out.Inbounds))
	for _, inbound := range out.Inbounds {
		byTag[inbound.Tag] = inbound.Settings
	}
	return byTag
}

func serializedClientEmails(t *testing.T, settings map[string]any) []string {
	t.Helper()
	clients, _ := settings["clients"].([]any)
	emails := make([]string, 0, len(clients))
	for _, client := range clients {
		entry, ok := client.(map[string]any)
		if !ok {
			t.Fatalf("client %v is not an object", client)
		}
		email, _ := entry["email"].(string)
		emails = append(emails, email)
	}
	return emails
}

func buildShadowsocksInbound(t *testing.T, settings map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var server conf.ShadowsocksServerConfig
	if err := json.Unmarshal(raw, &server); err != nil {
		t.Fatal(err)
	}
	built, err := server.Build()
	if err != nil {
		t.Fatalf("xray rejected the serialized inbound %v: %v", settings, err)
	}
	return built
}

func TestToBytesStartsEmptyShadowsocks2022InboundsMultiUser(t *testing.T) {
	cfg, err := NewConfig(shadowsocks2022Config, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := serializedInboundSettings(t, cfg)

	for _, tag := range []string{"aes128", "aes256"} {
		settings := out[tag]
		built := buildShadowsocksInbound(t, settings)
		if _, ok := built.(*shadowsocks_2022.MultiUserServerConfig); !ok {
			t.Errorf("%s: an inbound without users was built as %T, want a multi-user server", tag, built)
			continue
		}
		clients, _ := settings["clients"].([]any)
		if len(clients) != 1 {
			t.Fatalf("%s: clients = %v, want one placeholder", tag, clients)
		}
		placeholder, _ := clients[0].(map[string]any)
		if placeholder["password"] == settings["password"] {
			t.Fatalf("%s: the placeholder uses the server key", tag)
		}
	}

	if got := serializedClientEmails(t, out["chacha"]); len(got) != 0 {
		t.Fatalf("chacha20 inbound clients = %v, want none", got)
	}
	buildShadowsocksInbound(t, out["chacha"])

	if got := serializedClientEmails(t, out["legacy"]); len(got) != 0 {
		t.Fatalf("legacy inbound clients = %v, want none", got)
	}

	for _, inbound := range cfg.InboundConfigs {
		if len(inbound.clients) != 0 {
			t.Fatalf("ToBytes stored clients on inbound %s: %v", inbound.Tag, inbound.clients)
		}
	}
}

func TestToBytesWritesOnlyRealShadowsocks2022Users(t *testing.T) {
	cfg, err := NewConfig(shadowsocks2022Config, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.syncUsers([]*common.User{{
		Email:    "1",
		Inbounds: []string{"aes128", "aes256"},
		Proxies:  &common.Proxy{Shadowsocks: &common.Shadowsocks{Password: "example-user-one-key"}},
	}})
	out := serializedInboundSettings(t, cfg)

	for _, tag := range []string{"aes128", "aes256"} {
		if got := serializedClientEmails(t, out[tag]); len(got) != 1 || got[0] != "1" {
			t.Fatalf("%s clients = %v, want [1]", tag, got)
		}
	}
}

func TestThePlaceholderLeavesOnceAShadowsocks2022InboundGetsAUserAndIsNeverAUser(t *testing.T) {
	cfg, err := NewConfig(shadowsocks2022Config, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := serializedInboundSettings(t, cfg)
	placeholderEmails := serializedClientEmails(t, first["aes128"])
	if len(placeholderEmails) != 1 {
		t.Fatalf("aes128 clients = %v, want one placeholder", placeholderEmails)
	}

	clone, err := cfg.Clone()
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	for _, inbound := range clone.InboundConfigs {
		if len(inbound.clients) != 0 {
			t.Fatalf("a clone taken after ToBytes holds clients on %s: %v", inbound.Tag, inbound.clients)
		}
	}

	user := &common.User{
		Email:    "1",
		Inbounds: []string{"aes128"},
		Proxies:  &common.Proxy{Shadowsocks: &common.Shadowsocks{Password: "example-user-one-key"}},
	}
	cfg.syncUsers([]*common.User{user})
	if got := serializedClientEmails(t, serializedInboundSettings(t, cfg)["aes128"]); len(got) != 1 || got[0] != "1" {
		t.Fatalf("aes128 clients with a real user = %v, want [1]", got)
	}

	cfg.syncUsers(nil)
	again := serializedClientEmails(t, serializedInboundSettings(t, cfg)["aes128"])
	if len(again) != 1 || again[0] != placeholderEmails[0] {
		t.Fatalf("aes128 clients after its last user left = %v, want the placeholder %v back", again, placeholderEmails)
	}
	if again[0] == user.GetEmail() {
		t.Fatal("the placeholder took a panel user's email")
	}
}
