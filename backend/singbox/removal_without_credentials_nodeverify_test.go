package singbox

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
)

const nvSingboxConfig = `{
	"log": {"level": "warn"},
	"inbounds": [
		{"type": "hysteria2", "tag": "hy2-in", "listen": "::", "listen_port": 8443, "users": []},
		{"type": "vless", "tag": "vless-in", "users": []},
		{"type": "vmess", "tag": "vmess-in", "users": []},
		{"type": "trojan", "tag": "trojan-in", "users": []},
		{"type": "shadowsocks", "tag": "ss-in", "method": "aes-128-gcm", "users": []},
		{"type": "tuic", "tag": "tuic-in", "users": []}
	],
	"outbounds": [{"type": "direct"}]
}`

func nvSingboxPanelRemoval(t *testing.T) *common.User {
	t.Helper()
	raw, err := hex.DecodeString("0a0234321200")
	if err != nil {
		t.Fatal(err)
	}
	user := &common.User{}
	if err := proto.Unmarshal(raw, user); err != nil {
		t.Fatal(err)
	}
	if user.GetEmail() != "42" || user.GetProxies().GetHysteria2() != nil || len(user.GetInbounds()) != 0 {
		t.Fatalf("decoded removal is not the panel shape: %v", user)
	}
	return user
}

func nvSingboxMember(email string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{"hy2-in", "vless-in", "vmess-in", "trojan-in", "ss-in", "tuic-in"},
		Proxies: &common.Proxy{
			Hysteria2:   &common.Hysteria2{Password: "hy2-" + email},
			Vless:       &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"},
			Vmess:       &common.Vmess{Id: "6f1c6c5e-3f59-4a0e-9d8a-2b4f1c9d7e10"},
			Trojan:      &common.Trojan{Password: "trojan-" + email},
			Shadowsocks: &common.Shadowsocks{Password: "ss-" + email, Method: "aes-128-gcm"},
			Tuic:        &common.Tuic{Uuid: "7d0f2a4e-6a54-4a51-9a3a-6f1e2b1f7c12", Password: "tuic-" + email},
		},
	}
}

func TestNodeVerifySingboxPanelRemovalWithoutCredentialsRevokesEveryInbound(t *testing.T) {
	for name, apply := range map[string]func(*Config, *common.User){
		"upsertUser":  func(c *Config, u *common.User) { c.upsertUser(u) },
		"updateUsers": func(c *Config, u *common.User) { c.updateUsers([]*common.User{u}) },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := mustParseConfig(t, nvSingboxConfig)
			cfg.syncUsers([]*common.User{nvSingboxMember("42"), nvSingboxMember("43")})
			for tag, inbound := range cfg.inbounds {
				if _, ok := inbound.users["42"]; !ok {
					t.Fatalf("precondition: user 42 must be on %s", tag)
				}
			}
			apply(cfg, nvSingboxPanelRemoval(t))
			for tag, inbound := range cfg.inbounds {
				if _, ok := inbound.users["42"]; ok {
					t.Fatalf("user 42 still on %s after the panel removed it", tag)
				}
				if _, ok := inbound.users["43"]; !ok {
					t.Fatalf("removing user 42 dropped user 43 from %s", tag)
				}
			}
		})
	}
}
