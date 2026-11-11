package xray

import (
	"encoding/json"
	"testing"

	"github.com/pasarguard/node/common"
)

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
