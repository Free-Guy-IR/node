package xray

import (
	"context"
	"testing"

	"github.com/pasarguard/node/common"
)

func withHysteriaAuth(user *common.User, auth string) *common.User {
	user.Proxies.Hysteria = &common.Hysteria{Auth: auth}
	return user
}

type userApplier func(ctx context.Context, x *Xray, user *common.User) error

func userAppliers() map[string]userApplier {
	return map[string]userApplier{
		"SyncUser": func(ctx context.Context, x *Xray, user *common.User) error { return x.SyncUser(ctx, user) },
		"UpdateUsers": func(ctx context.Context, x *Xray, user *common.User) error {
			return x.UpdateUsers(ctx, []*common.User{user})
		},
	}
}

func TestAnEmptyHysteriaAuthIsNeverAdded(t *testing.T) {
	for name, apply := range userAppliers() {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)

			if err := apply(ctx, x, withHysteriaAuth(syncTestUser("1.alice", syncTestTags...), "")); err != nil {
				t.Fatalf("a user with an empty hysteria auth failed the sync: %v", err)
			}
			requireOperations(t, core.take(), addedOn("vless", "vmess", "trojan", "ss", "ss2022")...)
			if emails := clientEmails(x, "hysteria"); len(emails) != 0 {
				t.Fatalf("the node holds %v on hysteria with an empty auth", emails)
			}
			requireCoreMatchesLocal(t, x, core)
		})
	}
}

func TestAnEmptyHysteriaAuthIsNeverLoadedAtStartup(t *testing.T) {
	cfg, err := NewConfig(orderTestInbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.syncUsers([]*common.User{
		withHysteriaAuth(syncTestUser("1.alice", syncTestTags...), ""),
		withHysteriaAuth(syncTestUser("2.bob", syncTestTags...), "bob hysteria secret"),
	})

	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	clients := emittedClients(t, raw)
	if got := clients["hysteria"]; len(got) != 1 || got[0] != "2.bob" {
		t.Fatalf("the startup config lists %v on hysteria, want only the user with an auth", got)
	}
	if got := clients["vless"]; len(got) != 2 {
		t.Fatalf("an empty hysteria auth also dropped the user from vless: %v", got)
	}
}

func TestAUserWhoseHysteriaAuthBecomesEmptyIsRevoked(t *testing.T) {
	for name, apply := range userAppliers() {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)

			if err := apply(ctx, x, syncTestUser("1.alice", "vless", "hysteria")); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), addedOn("vless", "hysteria")...)

			if err := apply(ctx, x, withHysteriaAuth(syncTestUser("1.alice", "vless", "hysteria"), "")); err != nil {
				t.Fatalf("emptying the hysteria auth failed the sync: %v", err)
			}
			requireOperations(t, core.take(), revokedOn("hysteria")...)
			requireCoreMatchesLocal(t, x, core)
			if held := core.state()["hysteria"]; len(held) != 0 {
				t.Fatalf("the core still holds %v on hysteria after the auth became empty", held)
			}

			if err := apply(ctx, x, withHysteriaAuth(syncTestUser("1.alice", "vless", "hysteria"), "")); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take())
		})
	}
}
