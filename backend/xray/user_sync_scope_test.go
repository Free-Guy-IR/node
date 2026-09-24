package xray

import (
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const flowOverrideInbounds = `{"inbounds":[
	{"tag":"vless-forced","protocol":"vless","settings":{"clients":[],"decryption":"none","flow":"xtls-rprx-vision"}},
	{"tag":"vless-free","protocol":"vless","settings":{"clients":[],"decryption":"none"}}
]}`

func rotatedOn(stock, firstRevoke bool, tags ...string) []string {
	var pairs []string
	for i, tag := range tags {
		switch {
		case !stock:
			pairs = append(pairs, tag+":"+revokeOperation)
		case firstRevoke && i == 0:
			pairs = append(pairs, tag+":"+revokeOperation, tag+":"+removeOperation)
		default:
			pairs = append(pairs, tag+":"+removeOperation)
		}
		pairs = append(pairs, tag+":"+addOperation)
	}
	return pairs
}

func addedOn(tags ...string) []string {
	var pairs []string
	for _, tag := range tags {
		pairs = append(pairs, tag+":"+addOperation)
	}
	return pairs
}

func storedAccount(x *Xray, tag, email string) api.Account {
	for _, inbound := range x.config.InboundConfigs {
		if inbound.Tag != tag {
			continue
		}
		inbound.mu.RLock()
		defer inbound.mu.RUnlock()
		return inbound.clients[email]
	}
	return nil
}

func TestSyncUserRevokesTheOldAccountWhenItChanges(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			core := newModelCore(stock)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)
			user := syncTestUser("1.alice", "vless", "trojan")

			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), addedOn("vless", "trojan")...)

			user.Proxies.Trojan.Password = "rotated trojan secret"
			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), rotatedOn(stock, true, "trojan")...)
			requireCoreMatchesLocal(t, x, core)

			user.Proxies.Vless.Id = "9c3e8f5a-1b2d-4c6e-8f0a-3d5b7c9e1f21"
			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), rotatedOn(stock, false, "vless")...)
			requireCoreMatchesLocal(t, x, core)

			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take())
		})
	}
}

func TestUpdateUsersRevokesTheOldAccountWhenItChanges(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			core := newModelCore(stock)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)
			alice := syncTestUser("1.alice", "vless", "trojan")
			bob := syncTestUser("2.bob", "vless", "trojan")

			if err := x.UpdateUsers(ctx, []*common.User{alice, bob}); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), "vless:add", "vless:add", "trojan:add", "trojan:add")

			bob.Proxies.Vless.Id = "9c3e8f5a-1b2d-4c6e-8f0a-3d5b7c9e1f21"
			if err := x.UpdateUsers(ctx, []*common.User{alice, bob}); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), rotatedOn(stock, true, "vless")...)
			requireCoreMatchesLocal(t, x, core)

			if err := x.UpdateUsers(ctx, []*common.User{alice, bob}); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take())
		})
	}
}

func TestAFlowTheInboundOverridesLeavesTheUserAlone(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, flowOverrideInbounds)
	ctx := syncTestContext(t)
	user := &common.User{
		Email:    "1.alice",
		Inbounds: []string{"vless-forced", "vless-free"},
		Proxies:  &common.Proxy{Vless: &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"}},
	}

	if err := x.UpdateUsers(ctx, []*common.User{user}); err != nil {
		t.Fatal(err)
	}
	core.take()

	user.Proxies.Vless.Flow = "xtls-rprx-vision"
	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), rotatedOn(false, false, "vless-free")...)
	requireCoreMatchesLocal(t, x, core)
	stored, ok := storedAccount(x, "vless-forced", "1.alice").(*api.VlessAccount)
	if !ok || stored.Flow != "xtls-rprx-vision" {
		t.Fatalf("the node did not keep the user's new flow for vless-forced: %#v", storedAccount(x, "vless-forced", "1.alice"))
	}

	user.Proxies.Vless.Flow = ""
	if err := x.UpdateUsers(ctx, []*common.User{user}); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), rotatedOn(false, false, "vless-free")...)
	requireCoreMatchesLocal(t, x, core)
}

func thousandUsers() []*common.User {
	users := make([]*common.User, 0, 1000)
	for i := range 1000 {
		users = append(users, versionedUser(fmt.Sprintf("%d.user", i), "stable", "vless", "trojan"))
	}
	return users
}

func TestUnchangedBulkSyncOfAThousandUsersSendsNothing(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	if err := x.config.ApplyAPI(62789, 62790); err != nil {
		t.Fatal(err)
	}
	ctx := syncTestContext(t)
	users := thousandUsers()

	if err := x.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	first := core.calls()
	core.take()

	if err := x.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	second := core.calls() - first
	for _, user := range users[:10] {
		if err := x.SyncUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	third := core.calls() - first - second
	t.Logf("gRPC calls to the core: %d for the first sync of 1000 users, %d for an unchanged resync of all of them, %d for 10 unchanged single-user syncs", first, second, third)
	if first != 2000 {
		t.Fatalf("the first sync of 1000 users on two inbounds sent %d calls, want 2000 adds", first)
	}
	if second != 0 || third != 0 {
		t.Fatalf("unchanged users cost %d calls in a bulk sync and %d in single syncs, want none", second, third)
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRevokesOnlyWhereTheNodeHeldTheUser(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)

	if err := x.SyncUser(ctx, syncTestUser("1.alice", "vless", "trojan")); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), addedOn("vless", "trojan")...)

	if err := x.SyncUser(ctx, syncTestUser("1.alice", "vless")); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn("trojan")...)

	if err := x.UpdateUsers(ctx, []*common.User{syncTestUser("1.alice"), syncTestUser("3.carol")}); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn("vless")...)

	if err := x.SyncUser(ctx, syncTestUser("1.alice")); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take())
	requireCoreMatchesLocal(t, x, core)
	if held := core.state(); len(held) != 0 {
		t.Fatalf("the core still holds %v", held)
	}
}

var errDeadline = status.Error(codes.DeadlineExceeded, "context deadline exceeded")

func faultOnce(op, tag string, applied bool, err error) func(string, string, string) (bool, error) {
	fired := false
	return func(gotOp, gotTag, _ string) (bool, error) {
		if fired || gotOp != op || gotTag != tag {
			return false, nil
		}
		fired = true
		return applied, err
	}
}

func TestAnAddTheCoreAppliedButReportedAsFailedIsRevokedWhenTheUserLeaves(t *testing.T) {
	for name, apply := range map[string]func(*Xray, *common.User) error{
		"SyncUser":    func(x *Xray, user *common.User) error { return x.SyncUser(syncTestContext(t), user) },
		"UpdateUsers": func(x *Xray, user *common.User) error { return x.UpdateUsers(syncTestContext(t), []*common.User{user}) },
	} {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)

			core.setFault(faultOnce(addOperation, "trojan", true, errDeadline))
			if err := apply(x, syncTestUser("1.alice", "trojan")); err == nil {
				t.Fatal("an add that timed out was not reported")
			}
			core.take()
			if held := core.state()["trojan"]; len(held) != 1 {
				t.Fatalf("the fault did not leave the user on the core: %v", held)
			}

			if err := apply(x, syncTestUser("1.alice")); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), revokedOn("trojan")...)
			requireCoreMatchesLocal(t, x, core)
			if held := core.state(); len(held) != 0 {
				t.Fatalf("a user the node lost track of stayed on the core after leaving: %v", held)
			}

			if err := apply(x, syncTestUser("1.alice")); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take())
		})
	}
}

func TestAnAddTheCoreAppliedButReportedAsFailedIsReplacedOnTheNextSync(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", "trojan")

	core.setFault(faultOnce(addOperation, "trojan", true, errDeadline))
	if err := x.SyncUser(ctx, user); err == nil {
		t.Fatal("an add that timed out was not reported")
	}
	core.take()

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), rotatedOn(false, false, "trojan")...)
	requireCoreMatchesLocal(t, x, core)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take())
}

func TestAFailedRevokeOfALeavingUserIsRetried(t *testing.T) {
	for name, apply := range map[string]func(*Xray, *common.User) error{
		"SyncUser":    func(x *Xray, user *common.User) error { return x.SyncUser(syncTestContext(t), user) },
		"UpdateUsers": func(x *Xray, user *common.User) error { return x.UpdateUsers(syncTestContext(t), []*common.User{user}) },
	} {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)

			if err := apply(x, syncTestUser("1.alice", "vless", "trojan")); err != nil {
				t.Fatal(err)
			}
			core.take()

			unavailable := status.Error(codes.Unavailable, "connection reset by peer")
			revokeFailed, removeFailed := false, false
			core.setFault(func(op, tag, _ string) (bool, error) {
				switch {
				case tag != "trojan":
				case op == revokeOperation && !revokeFailed:
					revokeFailed = true
					return false, unavailable
				case op == removeOperation && !removeFailed:
					removeFailed = true
					return false, unavailable
				}
				return false, nil
			})
			if err := apply(x, syncTestUser("1.alice", "vless")); err == nil {
				t.Fatal("a failed revoke was not reported")
			}
			core.take()
			if held := core.state()["trojan"]; len(held) != 1 {
				t.Fatalf("the fault did not leave the user on the core: %v", held)
			}

			if err := apply(x, syncTestUser("1.alice", "vless")); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), revokedOn("trojan")...)
			requireCoreMatchesLocal(t, x, core)

			if err := apply(x, syncTestUser("1.alice", "vless")); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take())
		})
	}
}

func TestAFailedRevokeOfAChangedAccountIsRetried(t *testing.T) {
	cases := map[string]struct {
		removeFails bool
	}{
		"the old account stayed on the core": {removeFails: true},
		"the old account was removed":        {removeFails: false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)
			user := syncTestUser("1.alice", "trojan")

			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			core.take()

			unavailable := status.Error(codes.Unavailable, "connection reset by peer")
			revokeFailed, removeFailed := false, false
			core.setFault(func(op, _, _ string) (bool, error) {
				switch {
				case op == revokeOperation && !revokeFailed:
					revokeFailed = true
					return false, unavailable
				case op == removeOperation && c.removeFails && !removeFailed:
					removeFailed = true
					return false, unavailable
				}
				return false, nil
			})
			user.Proxies.Trojan.Password = "rotated trojan secret"
			if err := x.SyncUser(ctx, user); err == nil {
				t.Fatal("a failed revoke of the old account was not reported")
			}
			core.take()

			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), rotatedOn(false, false, "trojan")...)
			requireCoreMatchesLocal(t, x, core)

			if err := x.SyncUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take())
		})
	}
}
