package xray

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"

	"github.com/pasarguard/node/common"
)

const toBytesRaceConfig = `{
	"log":{"access":"/var/log/xray/access.log","error":"/var/log/xray/error.log","loglevel":"warning"},
	"inbounds":[
		{"tag":"static","protocol":"vless","settings":{"decryption":"none","clients":[{"id":"6b1d1f0e-5c1d-4c3a-9f6e-111111111111","email":"hand-written"}]}},
		{"tag":"managed","protocol":"vless","settings":{"decryption":"none","clients":[]}},
		{"tag":"aes128","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","network":"tcp,udp"}}
	]
}`

func newToBytesRaceConfig(t *testing.T) *Config {
	t.Helper()

	cfg, err := NewConfig(toBytesRaceConfig, []string{"static"})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func toBytesRaceUsers() []*common.User {
	return []*common.User{
		{
			Email:    "1",
			Inbounds: []string{"static", "managed"},
			Proxies:  &common.Proxy{Vless: &common.Vless{Id: "6b1d1f0e-5c1d-4c3a-9f6e-222222222222"}},
		},
		{
			Email:    "2",
			Inbounds: []string{"aes128"},
			Proxies:  &common.Proxy{Shadowsocks: &common.Shadowsocks{Password: "example-user-two-key"}},
		},
	}
}

func TestToBytesConcurrentCalls(t *testing.T) {
	cfg := newToBytesRaceConfig(t)
	cfg.syncUsers(toBytesRaceUsers())

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := cfg.ToBytes(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for round := range 20 {
			if round%2 == 0 {
				cfg.syncUsers(toBytesRaceUsers())
			} else {
				cfg.syncUsers(nil)
			}
		}
	})
	wg.Wait()
}

func TestToBytesWhileCloning(t *testing.T) {
	cfg := newToBytesRaceConfig(t)
	cfg.syncUsers(toBytesRaceUsers())

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			if _, err := cfg.ToBytes(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 50 {
			if _, err := cfg.Clone(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
}

const shadowsocks2022Config = `{"inbounds":[
	{"tag":"aes128","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","network":"tcp,udp"}},
	{"tag":"aes256","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-256-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=","network":"tcp,udp"}},
	{"tag":"chacha","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-chacha20-poly1305","password":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=","network":"tcp,udp"}},
	{"tag":"legacy","protocol":"shadowsocks","settings":{"clients":[],"method":"aes-256-gcm","password":"legacy-server-password","network":"tcp,udp"}}
]}`

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
	out := serializeConfig(t, cfg)

	for _, tag := range []string{"aes128", "aes256"} {
		settings := out.settings(t, tag)
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

	if got := emails(t, out.settings(t, "chacha"), "clients"); len(got) != 0 {
		t.Fatalf("chacha20 inbound clients = %v, want none", got)
	}
	buildShadowsocksInbound(t, out.settings(t, "chacha"))

	if got := emails(t, out.settings(t, "legacy"), "clients"); len(got) != 0 {
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
	out := serializeConfig(t, cfg)

	for _, tag := range []string{"aes128", "aes256"} {
		if got := emails(t, out.settings(t, tag), "clients"); len(got) != 1 || got[0] != "1" {
			t.Fatalf("%s clients = %v, want [1]", tag, got)
		}
	}
}
