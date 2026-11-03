package xray

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
)

func nvXrayPanelRemoval(t *testing.T) *common.User {
	t.Helper()
	raw, err := hex.DecodeString("0a0234321200")
	if err != nil {
		t.Fatal(err)
	}
	user := &common.User{}
	if err := proto.Unmarshal(raw, user); err != nil {
		t.Fatal(err)
	}
	if user.GetEmail() != "42" || user.GetProxies() == nil || user.GetProxies().GetVless() != nil || len(user.GetInbounds()) != 0 {
		t.Fatalf("decoded removal is not the panel shape: %v", user)
	}
	return user
}

func TestNodeVerifyXrayPanelRemovalWithoutCredentialsRevokesEveryInbound(t *testing.T) {
	apply := map[string]func(*Xray, context.Context, *common.User) error{
		"SyncUser": func(x *Xray, ctx context.Context, u *common.User) error { return x.SyncUser(ctx, u) },
		"UpdateUsers": func(x *Xray, ctx context.Context, u *common.User) error {
			return x.UpdateUsers(ctx, []*common.User{u})
		},
	}
	for name, call := range apply {
		for _, stock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stock=%v", name, stock), func(t *testing.T) {
				core := newModelCore(stock)
				x := newModelXray(t, core, syncTestInbounds)
				ctx := syncTestContext(t)
				if err := x.SyncUser(ctx, syncTestUser("42", syncTestTags...)); err != nil {
					t.Fatal(err)
				}
				if err := x.SyncUser(ctx, syncTestUser("43", syncTestTags...)); err != nil {
					t.Fatal(err)
				}
				if err := call(x, ctx, nvXrayPanelRemoval(t)); err != nil {
					t.Fatal(err)
				}
				requireCoreMatchesLocal(t, x, core)
				for tag, users := range core.state() {
					if _, held := users["42"]; held {
						t.Fatalf("the core still holds user 42 on %s after the panel removed it", tag)
					}
					if _, held := users["43"]; !held {
						t.Fatalf("removing user 42 dropped user 43 from %s", tag)
					}
				}
			})
		}
	}
}
