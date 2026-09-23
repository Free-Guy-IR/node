package xray

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/app/proxyman/command"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

func xrayBinariesUnderTest() map[string]string {
	binaries := map[string]string{}
	if _, err := os.Stat(executablePath); err == nil {
		binaries["installed"] = executablePath
	}
	if stock := os.Getenv("XRAY_STOCK_EXECUTABLE_PATH"); stock != "" {
		binaries["stock"] = stock
	}
	return binaries
}

func TestRemovingUsersTheCoreDoesNotHoldIsSilent(t *testing.T) {
	binaries := xrayBinariesUnderTest()
	if len(binaries) == 0 {
		t.Skip("no xray binary found")
	}

	for name, binary := range binaries {
		t.Run(name, func(t *testing.T) {
			removeUsersTheCoreDoesNotHold(t, binary)
		})
	}
}

func removeUsersTheCoreDoesNotHold(t *testing.T, binary string) {
	const (
		tag            = "absent-vless"
		shadowsocksTag = "absent-shadowsocks"
	)
	serverConfig := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag":            tag,
				"listen":         "127.0.0.1",
				"port":           netutil.FindFreePort(),
				"protocol":       Vless,
				"settings":       map[string]any{"clients": []any{}, "decryption": "none"},
				"streamSettings": map[string]any{"network": "tcp"},
			},
			map[string]any{
				"tag":      shadowsocksTag,
				"listen":   "127.0.0.1",
				"port":     netutil.FindFreePort(),
				"protocol": Shadowsocks,
				"settings": map[string]any{"clients": []any{}, "network": "tcp"},
			},
		},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	}
	raw, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	xrayConfig, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}

	victim := &common.User{
		Email:    "absent-victim",
		Inbounds: []string{tag},
		Proxies:  &common.Proxy{Vless: &common.Vless{Id: uuid.New().String()}},
	}
	stranger := &common.User{
		Email:   "absent-stranger",
		Proxies: &common.Proxy{Vless: &common.Vless{Id: uuid.New().String()}},
	}
	shadowsocksMember := &common.User{
		Email:    "absent-shadowsocks-member",
		Inbounds: []string{shadowsocksTag},
		Proxies:  &common.Proxy{Shadowsocks: &common.Shadowsocks{Password: uuid.New().String(), Method: "aes-128-gcm"}},
	}
	mixedCase := &common.User{
		Email:    "7.MixedCase",
		Inbounds: []string{tag},
		Proxies: &common.Proxy{
			Vless:       &common.Vless{Id: uuid.New().String()},
			Shadowsocks: &common.Shadowsocks{Password: uuid.New().String(), Method: "aes-128-gcm"},
		},
	}

	cfg := &config.Config{
		XrayExecutablePath:  binary,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	}
	backend, err := New(context.Background(), xrayConfig, []*common.User{victim, shadowsocksMember, mixedCase}, netutil.FindFreePort(), netutil.FindFreePort(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)
	t.Logf("core under test: %s", backend.Version())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := backend.SyncUser(ctx, withoutInbounds(victim)); err != nil {
		t.Fatalf("removing a user the core holds was reported: %v", err)
	}

	client := *backend.handler.HandlerServiceClient
	users, err := client.GetInboundUsers(ctx, &command.GetInboundUserRequest{Tag: tag})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users.GetUsers() {
		if user.GetEmail() == victim.GetEmail() {
			t.Fatal("the removed user is still on the inbound")
		}
	}

	if err := backend.SyncUser(ctx, withoutInbounds(victim)); err != nil {
		t.Fatalf("removing a user the core no longer holds was reported: %v", err)
	}
	if err := backend.UpdateUsers(ctx, []*common.User{stranger}); err != nil {
		t.Fatalf("removing a user the core never held was reported: %v", err)
	}

	if err := backend.SyncUser(ctx, mixedCase); err != nil {
		t.Fatalf("syncing a mixed-case user who is not on a shadowsocks inbound was reported: %v", err)
	}
	if err := backend.UpdateUsers(ctx, []*common.User{mixedCase}); err != nil {
		t.Fatalf("updating a mixed-case user who is not on a shadowsocks inbound was reported: %v", err)
	}
	if err := backend.SyncUser(ctx, withoutInbounds(mixedCase)); err != nil {
		t.Fatalf("removing a mixed-case user was reported: %v", err)
	}

	members, err := client.GetInboundUsers(ctx, &command.GetInboundUserRequest{Tag: shadowsocksTag})
	if err != nil {
		t.Fatal(err)
	}
	if len(members.GetUsers()) != 1 || members.GetUsers()[0].GetEmail() != shadowsocksMember.GetEmail() {
		t.Fatalf("the shadowsocks member was disturbed: %v", members.GetUsers())
	}
}
