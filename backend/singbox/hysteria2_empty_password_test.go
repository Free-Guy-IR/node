package singbox

import (
	"slices"
	"testing"

	"github.com/pasarguard/node/common"
)

const emptyPasswordRawConfig = `{"inbounds": [
	{"type": "hysteria2", "tag": "hy2-in", "users": [{"name": "template-empty", "password": ""}, {"name": "template-set", "password": "template-secret"}]},
	{"type": "vless", "tag": "vless-in", "users": []}
], "outbounds": [{"type": "direct"}]}`

func hy2AndVlessUser(email, password string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{"hy2-in", "vless-in"},
		Proxies: &common.Proxy{
			Hysteria2: &common.Hysteria2{Password: password},
			Vless:     &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"},
		},
	}
}

func generatedNames(t *testing.T, cfg *Config) (map[string][]string, []string) {
	t.Helper()

	if err := cfg.ApplyAPI(18090); err != nil {
		t.Fatal(err)
	}
	data, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	root := decode(t, data)
	names := make(map[string][]string, len(root.Inbounds))
	for _, inbound := range root.Inbounds {
		for _, user := range inbound.Users {
			names[inbound.Tag] = append(names[inbound.Tag], user.Name)
		}
	}
	return names, root.Experimental.V2RayAPI.Stats.Users
}

func TestAnEmptyHysteria2PasswordIsNeverInstalled(t *testing.T) {
	apply := map[string]func(*Config, []*common.User){
		"syncUsers":   (*Config).syncUsers,
		"updateUsers": (*Config).updateUsers,
		"upsertUser": func(cfg *Config, users []*common.User) {
			for _, user := range users {
				cfg.upsertUser(user)
			}
		},
	}
	for name, sync := range apply {
		t.Run(name, func(t *testing.T) {
			cfg := mustParseConfig(t, emptyPasswordRawConfig)
			sync(cfg, []*common.User{hy2AndVlessUser("1", ""), hy2AndVlessUser("2", "own-password")})

			names, _ := generatedNames(t, cfg)
			if got := names["hy2-in"]; slices.Contains(got, "1") || !slices.Contains(got, "2") {
				t.Fatalf("hy2-in lists %v, want the user with an empty password left off", got)
			}
			if got := names["vless-in"]; !slices.Equal(got, []string{"1", "2"}) {
				t.Fatalf("an empty hysteria2 password also dropped the user from vless-in: %v", got)
			}
		})
	}
}

func TestATemplateUserWithAnEmptyHysteria2PasswordIsDropped(t *testing.T) {
	cfg := mustParseConfig(t, emptyPasswordRawConfig)

	names, _ := generatedNames(t, cfg)
	if got := names["hy2-in"]; !slices.Equal(got, []string{"template-set"}) {
		t.Fatalf("hy2-in lists %v from the template, want the user with an empty password dropped", got)
	}
}

func TestAUserWhoseHysteria2PasswordBecomesEmptyIsRemoved(t *testing.T) {
	cfg := mustParseConfig(t, emptyPasswordRawConfig)
	cfg.syncUsers([]*common.User{hy2AndVlessUser("1", "own-password")})

	names, _ := generatedNames(t, cfg)
	if got := names["hy2-in"]; !slices.Equal(got, []string{"1"}) {
		t.Fatalf("hy2-in lists %v before the password became empty", got)
	}

	cfg.upsertUser(hy2AndVlessUser("1", ""))
	names, _ = generatedNames(t, cfg)
	if got := names["hy2-in"]; len(got) != 0 {
		t.Fatalf("hy2-in still lists %v after the user's password became empty", got)
	}
	if got := names["vless-in"]; !slices.Equal(got, []string{"1"}) {
		t.Fatalf("the user left vless-in when only the hysteria2 password became empty: %v", got)
	}
}
