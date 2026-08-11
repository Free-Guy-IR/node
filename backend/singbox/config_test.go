package singbox

import (
	"encoding/json"
	"testing"

	"github.com/pasarguard/node/common"
)

const testRawConfig = `{
	"log": {"level": "debug"},
	"inbounds": [{
		"type": "hysteria2",
		"tag": "hy2-in",
		"listen": "::",
		"listen_port": 8443,
		"users": [],
		"tls": {"enabled": true, "certificate_path": "/certs/cert.pem", "key_path": "/certs/key.pem"}
	}, {
		"type": "hysteria2",
		"tag": "hy2-in-2",
		"listen": "::",
		"listen_port": 8444,
		"users": []
	}],
	"outbounds": [{"type": "direct"}]
}`

func hy2User(email, password string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Hysteria2: &common.Hysteria2{Password: password},
		},
	}
}

func mustParseConfig(t *testing.T, raw string) *Config {
	t.Helper()
	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	return cfg
}

// decodedConfig unmarshal helpers to inspect ToBytes() output.

type decodedInbound struct {
	Tag   string      `json:"tag"`
	Type  string      `json:"type"`
	Users []userEntry `json:"users"`
}

type decodedV2RayAPI struct {
	Listen string `json:"listen"`
	Stats  struct {
		Enabled bool     `json:"enabled"`
		Users   []string `json:"users"`
	} `json:"stats"`
}

type decodedRoot struct {
	Inbounds     []decodedInbound `json:"inbounds"`
	Experimental struct {
		V2RayAPI decodedV2RayAPI `json:"v2ray_api"`
	} `json:"experimental"`
}

func decode(t *testing.T, data []byte) decodedRoot {
	t.Helper()
	var out decodedRoot
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("failed to unmarshal generated config: %v\n%s", err, string(data))
	}
	return out
}

func inboundByTag(root decodedRoot, tag string) *decodedInbound {
	for i := range root.Inbounds {
		if root.Inbounds[i].Tag == tag {
			return &root.Inbounds[i]
		}
	}
	return nil
}

func TestNewConfig_IndexesHysteria2Inbounds(t *testing.T) {
	cfg := mustParseConfig(t, testRawConfig)

	if !cfg.HasInbounds() {
		t.Fatal("expected config to report supported inbounds present")
	}
	if len(cfg.inbounds) != 2 {
		t.Fatalf("expected 2 hysteria2 inbounds indexed, got %d", len(cfg.inbounds))
	}
	if _, ok := cfg.inbounds["hy2-in"]; !ok {
		t.Error("expected hy2-in to be indexed")
	}
	if _, ok := cfg.inbounds["hy2-in-2"]; !ok {
		t.Error("expected hy2-in-2 to be indexed")
	}
}

func TestNewConfig_MissingTagErrors(t *testing.T) {
	raw := `{"inbounds": [{"type": "hysteria2", "listen": "::", "users": []}]}`
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for a hysteria2 inbound missing a tag")
	}
}

func TestNewConfig_UnsupportedInboundsIgnored(t *testing.T) {
	// wireguard is a real sing-box inbound type that this backend does not
	// manage per-user credentials for, so it must be passed through untouched
	// and never indexed.
	raw := `{"inbounds": [{"type": "wireguard", "tag": "wg-in"}]}`
	cfg := mustParseConfig(t, raw)
	if cfg.HasInbounds() {
		t.Fatal("expected no supported inbounds to be indexed for a wireguard-only config")
	}
}

func TestNewConfig_IndexesAllSupportedTypes(t *testing.T) {
	raw := `{"inbounds": [
		{"type": "vless", "tag": "vless-in", "users": []},
		{"type": "vmess", "tag": "vmess-in", "users": []},
		{"type": "trojan", "tag": "trojan-in", "users": []},
		{"type": "shadowsocks", "tag": "ss-in", "method": "chacha20-ietf-poly1305", "users": []},
		{"type": "hysteria2", "tag": "hy2-in", "users": []},
		{"type": "wireguard", "tag": "wg-in"}
	]}`
	cfg := mustParseConfig(t, raw)
	wantTypes := map[string]string{
		"vless-in":   "vless",
		"vmess-in":   "vmess",
		"trojan-in":  "trojan",
		"ss-in":      "shadowsocks",
		"hy2-in":     "hysteria2",
	}
	if len(cfg.inbounds) != len(wantTypes) {
		t.Fatalf("expected %d supported inbounds indexed, got %d", len(wantTypes), len(cfg.inbounds))
	}
	for tag, wantType := range wantTypes {
		in, ok := cfg.inbounds[tag]
		if !ok {
			t.Errorf("expected %q to be indexed", tag)
			continue
		}
		if in.typ != wantType {
			t.Errorf("inbound %q: expected type %q, got %q", tag, wantType, in.typ)
		}
	}
	if _, ok := cfg.inbounds["wg-in"]; ok {
		t.Error("wireguard inbound must not be indexed")
	}
}

// TestSyncUsers_MultiProtocol proves each inbound type serializes the exact
// per-protocol user object shape sing-box expects, and that a user is only
// added to an inbound it both lists in Inbounds and carries a matching proxy
// for.
func TestSyncUsers_MultiProtocol(t *testing.T) {
	raw := `{"inbounds": [
		{"type": "vless", "tag": "vless-in", "users": []},
		{"type": "vmess", "tag": "vmess-in", "users": []},
		{"type": "trojan", "tag": "trojan-in", "users": []},
		{"type": "shadowsocks", "tag": "ss-in", "method": "chacha20-ietf-poly1305", "users": []},
		{"type": "hysteria2", "tag": "hy2-in", "users": []}
	], "outbounds": [{"type": "direct"}]}`
	cfg := mustParseConfig(t, raw)

	user := &common.User{
		Email:    "multi@example.com",
		Inbounds: []string{"vless-in", "vmess-in", "trojan-in", "ss-in", "hy2-in"},
		Proxies: &common.Proxy{
			Vless:       &common.Vless{Id: "vless-uuid", Flow: "xtls-rprx-vision"},
			Vmess:       &common.Vmess{Id: "vmess-uuid"},
			Trojan:      &common.Trojan{Password: "trojan-pass"},
			Shadowsocks: &common.Shadowsocks{Password: "ss-pass", Method: "chacha20-ietf-poly1305"},
			Hysteria2:   &common.Hysteria2{Password: "hy2-pass"},
		},
	}
	// A vless-only user that lists the trojan inbound must NOT land on it
	// (no trojan proxy), but must land on the vless inbound.
	vlessOnly := &common.User{
		Email:    "vlessonly@example.com",
		Inbounds: []string{"vless-in", "trojan-in"},
		Proxies:  &common.Proxy{Vless: &common.Vless{Id: "vo-uuid"}},
	}

	cfg.syncUsers([]*common.User{user, vlessOnly})

	if err := cfg.ApplyAPI(18090); err != nil {
		t.Fatalf("ApplyAPI() error = %v", err)
	}
	data, err := cfg.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes() error = %v", err)
	}

	var root struct {
		Inbounds []struct {
			Tag   string           `json:"tag"`
			Users []map[string]any `json:"users"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal generated config: %v\n%s", err, string(data))
	}
	byTag := make(map[string][]map[string]any)
	for _, in := range root.Inbounds {
		byTag[in.Tag] = in.Users
	}

	// vless: {name, uuid, flow} for the full user; {name, uuid} (no flow) for
	// the vless-only user whose flow is empty.
	vless := byTag["vless-in"]
	if len(vless) != 2 {
		t.Fatalf("expected 2 users on vless-in, got %d (%+v)", len(vless), vless)
	}
	// sorted by name: multi@ then vlessonly@
	if vless[0]["name"] != "multi@example.com" || vless[0]["uuid"] != "vless-uuid" || vless[0]["flow"] != "xtls-rprx-vision" {
		t.Errorf("unexpected vless user[0]: %+v", vless[0])
	}
	if _, hasPassword := vless[0]["password"]; hasPassword {
		t.Errorf("vless user must not carry a password field: %+v", vless[0])
	}
	if vless[1]["name"] != "vlessonly@example.com" || vless[1]["uuid"] != "vo-uuid" {
		t.Errorf("unexpected vless user[1]: %+v", vless[1])
	}
	if _, hasFlow := vless[1]["flow"]; hasFlow {
		t.Errorf("vless user with empty flow must omit the flow field: %+v", vless[1])
	}

	// vmess: {name, uuid, alterId:0}
	vmess := byTag["vmess-in"]
	if len(vmess) != 1 {
		t.Fatalf("expected 1 user on vmess-in, got %d (%+v)", len(vmess), vmess)
	}
	if vmess[0]["name"] != "multi@example.com" || vmess[0]["uuid"] != "vmess-uuid" {
		t.Errorf("unexpected vmess user: %+v", vmess[0])
	}
	if alterId, ok := vmess[0]["alterId"]; !ok || alterId.(float64) != 0 {
		t.Errorf("vmess user must carry alterId:0, got %+v", vmess[0])
	}

	// trojan: {name, password} - only the full user (vlessOnly has no trojan proxy).
	trojan := byTag["trojan-in"]
	if len(trojan) != 1 || trojan[0]["name"] != "multi@example.com" || trojan[0]["password"] != "trojan-pass" {
		t.Fatalf("expected only multi@ with trojan-pass on trojan-in, got %+v", trojan)
	}

	// shadowsocks: {name, password}
	ss := byTag["ss-in"]
	if len(ss) != 1 || ss[0]["name"] != "multi@example.com" || ss[0]["password"] != "ss-pass" {
		t.Fatalf("expected only multi@ with ss-pass on ss-in, got %+v", ss)
	}

	// hysteria2: {name, password}
	hy2 := byTag["hy2-in"]
	if len(hy2) != 1 || hy2[0]["name"] != "multi@example.com" || hy2[0]["password"] != "hy2-pass" {
		t.Fatalf("expected only multi@ with hy2-pass on hy2-in, got %+v", hy2)
	}
}

func TestSyncUsers_FullReplace(t *testing.T) {
	cfg := mustParseConfig(t, testRawConfig)

	alice := hy2User("alice@example.com", "alicepass", "hy2-in")
	bob := hy2User("bob@example.com", "bobpass", "hy2-in", "hy2-in-2")
	// carol has no hysteria2 proxy configured; should never show up anywhere.
	carol := &common.User{Email: "carol@example.com", Inbounds: []string{"hy2-in"}}

	cfg.syncUsers([]*common.User{alice, bob, carol})

	if err := cfg.ApplyAPI(18080); err != nil {
		t.Fatalf("ApplyAPI() error = %v", err)
	}

	data, err := cfg.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes() error = %v", err)
	}
	root := decode(t, data)

	hy2in := inboundByTag(root, "hy2-in")
	if hy2in == nil {
		t.Fatal("hy2-in inbound missing from generated config")
	}
	if len(hy2in.Users) != 2 {
		t.Fatalf("expected 2 users on hy2-in, got %d (%+v)", len(hy2in.Users), hy2in.Users)
	}
	// sortedUsers() sorts by email, so alice then bob.
	if hy2in.Users[0].Name != "alice@example.com" || hy2in.Users[0].Password != "alicepass" {
		t.Errorf("unexpected first user on hy2-in: %+v", hy2in.Users[0])
	}
	if hy2in.Users[1].Name != "bob@example.com" || hy2in.Users[1].Password != "bobpass" {
		t.Errorf("unexpected second user on hy2-in: %+v", hy2in.Users[1])
	}

	hy2in2 := inboundByTag(root, "hy2-in-2")
	if hy2in2 == nil {
		t.Fatal("hy2-in-2 inbound missing from generated config")
	}
	if len(hy2in2.Users) != 1 || hy2in2.Users[0].Name != "bob@example.com" {
		t.Fatalf("expected only bob on hy2-in-2, got %+v", hy2in2.Users)
	}

	// experimental.v2ray_api.stats.users must list every synced user up front
	// (per-user stats counters only get created for names present at process
	// start - see config.go's refreshAPILocked doc comment).
	if !root.Experimental.V2RayAPI.Stats.Enabled {
		t.Error("expected v2ray_api stats to be enabled")
	}
	if root.Experimental.V2RayAPI.Listen != "127.0.0.1:18080" {
		t.Errorf("unexpected v2ray_api listen address: %q", root.Experimental.V2RayAPI.Listen)
	}
	wantUsers := map[string]bool{"alice@example.com": true, "bob@example.com": true}
	if len(root.Experimental.V2RayAPI.Stats.Users) != len(wantUsers) {
		t.Fatalf("expected %d stats users, got %v", len(wantUsers), root.Experimental.V2RayAPI.Stats.Users)
	}
	for _, u := range root.Experimental.V2RayAPI.Stats.Users {
		if !wantUsers[u] {
			t.Errorf("unexpected stats user %q", u)
		}
	}
}

func TestUpdateUsers_IncrementalMergeAndRemoval(t *testing.T) {
	cfg := mustParseConfig(t, testRawConfig)

	alice := hy2User("alice@example.com", "alicepass", "hy2-in")
	bob := hy2User("bob@example.com", "bobpass", "hy2-in")
	cfg.syncUsers([]*common.User{alice, bob})

	// Now update just alice's password, and remove bob (by no longer listing the
	// inbound tag - simulating a user losing access to hy2-in).
	aliceUpdated := hy2User("alice@example.com", "newpass", "hy2-in")
	bobRemoved := &common.User{Email: "bob@example.com", Proxies: &common.Proxy{Hysteria2: &common.Hysteria2{Password: "bobpass"}}} // no Inbounds -> inactive everywhere

	cfg.updateUsers([]*common.User{aliceUpdated, bobRemoved})

	inbound := cfg.inbounds["hy2-in"]
	if len(inbound.users) != 1 {
		t.Fatalf("expected 1 user left on hy2-in after update, got %d (%+v)", len(inbound.users), inbound.users)
	}
	got, ok := inbound.users["alice@example.com"]
	if !ok {
		t.Fatal("expected alice to remain on hy2-in")
	}
	if got.Password != "newpass" {
		t.Errorf("expected alice's password to be updated to 'newpass', got %q", got.Password)
	}
	if _, stillThere := inbound.users["bob@example.com"]; stillThere {
		t.Error("expected bob to be removed from hy2-in")
	}
}

func TestUpsertUser_SingleUserAddAndRemove(t *testing.T) {
	cfg := mustParseConfig(t, testRawConfig)

	alice := hy2User("alice@example.com", "alicepass", "hy2-in")
	cfg.upsertUser(alice)

	if _, ok := cfg.inbounds["hy2-in"].users["alice@example.com"]; !ok {
		t.Fatal("expected alice to be added to hy2-in via upsertUser")
	}

	// Now "remove" alice by syncing a version of her with no matching inbound.
	aliceGone := &common.User{Email: "alice@example.com", Proxies: &common.Proxy{Hysteria2: &common.Hysteria2{Password: "alicepass"}}}
	cfg.upsertUser(aliceGone)

	if _, ok := cfg.inbounds["hy2-in"].users["alice@example.com"]; ok {
		t.Fatal("expected alice to be removed from hy2-in via upsertUser")
	}
}

func TestClone_IsIndependent(t *testing.T) {
	cfg := mustParseConfig(t, testRawConfig)
	cfg.syncUsers([]*common.User{hy2User("alice@example.com", "alicepass", "hy2-in")})
	if err := cfg.ApplyAPI(18080); err != nil {
		t.Fatalf("ApplyAPI() error = %v", err)
	}

	clone, err := cfg.Clone()
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	// Mutate the clone; the original must not observe the change.
	clone.syncUsers([]*common.User{hy2User("bob@example.com", "bobpass", "hy2-in")})

	if _, ok := cfg.inbounds["hy2-in"].users["alice@example.com"]; !ok {
		t.Error("mutating the clone affected the original config's user set")
	}
	if _, ok := cfg.inbounds["hy2-in"].users["bob@example.com"]; ok {
		t.Error("mutating the clone leaked bob into the original config")
	}
	if _, ok := clone.inbounds["hy2-in"].users["bob@example.com"]; !ok {
		t.Error("expected bob to be present on the clone after syncing it")
	}

	// apiPort must have carried over to the clone.
	data, err := clone.ToBytes()
	if err != nil {
		t.Fatalf("clone.ToBytes() error = %v", err)
	}
	root := decode(t, data)
	if root.Experimental.V2RayAPI.Listen != "127.0.0.1:18080" {
		t.Errorf("expected clone to retain apiPort in v2ray_api.listen, got %q", root.Experimental.V2RayAPI.Listen)
	}
}

func TestToBytes_PreservesUnrelatedTopLevelKeys(t *testing.T) {
	cfg := mustParseConfig(t, testRawConfig)
	data, err := cfg.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes() error = %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, ok := root["log"]; !ok {
		t.Error("expected pass-through \"log\" key to survive ToBytes()")
	}
	if _, ok := root["outbounds"]; !ok {
		t.Error("expected pass-through \"outbounds\" key to survive ToBytes()")
	}
}
