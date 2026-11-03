package xray

import (
	"strings"
	"testing"

	"github.com/pasarguard/node/common"
)

const nvInboundsWithoutUsers = `{"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"socks-in","protocol":"socks","settings":{"auth":"noauth"}},
	{"tag":"mixed-in","protocol":"mixed","settings":{"auth":"noauth"}},
	{"tag":"http-in","protocol":"http","settings":{}},
	{"tag":"tunnel-in","protocol":"dokodemo-door","settings":{"address":"127.0.0.1","port":1,"network":"tcp"}}
]}`

func TestNodeVerifyUserSyncsSendNothingToInboundsThatCannotHoldUsers(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, nvInboundsWithoutUsers)
	if err := x.config.ApplyAPI(10085, 10086); err != nil {
		t.Fatal(err)
	}
	ctx := syncTestContext(t)

	alice := syncTestUser("1.alice", "vless")
	bob := &common.User{Email: "2.bob", Proxies: &common.Proxy{}}
	if err := x.UpdateUsers(ctx, []*common.User{alice, bob}); err != nil {
		t.Fatal(err)
	}
	if err := x.SyncUser(ctx, bob); err != nil {
		t.Fatal(err)
	}
	if err := x.SyncUser(ctx, syncTestUser("1.alice")); err != nil {
		t.Fatal(err)
	}
	ops := core.take()
	if len(ops) < 2 {
		t.Fatalf("expected the add and the removal of alice on vless, got %v", ops)
	}
	for _, op := range ops {
		if !strings.HasPrefix(op, "vless:") {
			t.Fatalf("an inbound that cannot hold users received %q", op)
		}
	}
}
