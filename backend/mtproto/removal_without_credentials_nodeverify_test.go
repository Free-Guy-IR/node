package mtproto

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
)

const nvPanelRemovalWireHex = "0a0234321200"

func nvMtprotoBackend(tags ...string) *Backend {
	b := &Backend{instances: map[string]*proxyInstance{}, secretsByID: map[string]secretEntry{}}
	for _, tag := range tags {
		b.instances[tag] = &proxyInstance{domain: tag + ".example"}
	}
	return b
}

func nvPanelRemoval(t *testing.T) *common.User {
	t.Helper()
	raw, err := hex.DecodeString(nvPanelRemovalWireHex)
	if err != nil {
		t.Fatal(err)
	}
	user := &common.User{}
	if err := proto.Unmarshal(raw, user); err != nil {
		t.Fatal(err)
	}
	if user.GetEmail() != "42" || user.GetProxies().GetMtproto() != nil || len(user.GetInbounds()) != 0 {
		t.Fatalf("decoded removal is not the panel shape: %v", user)
	}
	return user
}

func nvMtprotoMember(email, secret string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies:  &common.Proxy{Mtproto: &common.MtprotoUser{Username: email, Secret: secret}},
	}
}

func nvServes(b *Backend, username string) bool {
	for _, inst := range b.instances {
		if _, ok := b.secretsForInstance(inst.domain)[username]; ok {
			return true
		}
	}
	return false
}

const (
	nvSecret42 = "00112233445566778899aabbccddeeff"
	nvSecret43 = "ffeeddccbbaa99887766554433221100"
)

func TestNodeVerifyPanelRemovalWithoutCredentialsRevokesMTProto(t *testing.T) {
	apply := map[string]func(*Backend, *common.User) error{
		"SyncUser": func(b *Backend, u *common.User) error { return b.SyncUser(context.Background(), u) },
		"UpdateUsers": func(b *Backend, u *common.User) error {
			return b.UpdateUsers(context.Background(), []*common.User{u})
		},
		"UpdateUsersAndRestart": func(b *Backend, u *common.User) error {
			return b.UpdateUsersAndRestart(context.Background(), []*common.User{u})
		},
	}
	removals := map[string]func(*testing.T) *common.User{
		"panel wire bytes": nvPanelRemoval,
		"nil proxies":      func(*testing.T) *common.User { return &common.User{Email: "42"} },
	}
	for applyName, call := range apply {
		for removalName, removal := range removals {
			t.Run(applyName+"/"+removalName, func(t *testing.T) {
				b := nvMtprotoBackend("mtproto-tg", "mtproto-tg2")
				if err := call(b, nvMtprotoMember("42", nvSecret42, "mtproto-tg")); err != nil {
					t.Fatal(err)
				}
				if err := call(b, nvMtprotoMember("43", nvSecret43, "mtproto-tg2")); err != nil {
					t.Fatal(err)
				}
				if !nvServes(b, "42") {
					t.Fatal("precondition: user 42 must be served before the removal")
				}
				if err := call(b, removal(t)); err != nil {
					t.Fatal(err)
				}
				if nvServes(b, "42") {
					t.Fatal("user 42 still holds an MTProto secret after the panel removed it")
				}
				if !nvServes(b, "43") {
					t.Fatal("removing user 42 must not revoke user 43")
				}
			})
		}
	}
}

func TestNodeVerifyMTProtoRemovalDoesNotTouchAUserWhoseEmailOnlySharesAPrefix(t *testing.T) {
	b := nvMtprotoBackend("mtproto-tg")
	for _, u := range []*common.User{nvMtprotoMember("42", nvSecret42, "mtproto-tg"), nvMtprotoMember("420", nvSecret43, "mtproto-tg"), nvMtprotoMember("4", nvSecret43, "mtproto-tg")} {
		if err := b.SyncUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SyncUser(context.Background(), nvPanelRemoval(t)); err != nil {
		t.Fatal(err)
	}
	if nvServes(b, "42") {
		t.Fatal("user 42 still served after its removal")
	}
	if !nvServes(b, "420") || !nvServes(b, "4") {
		t.Fatal("the removal of 42 revoked a different user")
	}
}

func TestNodeVerifyMTProtoUserRemovedWithoutCredentialsCanBeAddedBack(t *testing.T) {
	b := nvMtprotoBackend("mtproto-tg")
	ctx := context.Background()
	if err := b.SyncUser(ctx, nvMtprotoMember("42", nvSecret42, "mtproto-tg")); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncUser(ctx, nvPanelRemoval(t)); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncUser(ctx, nvMtprotoMember("42", nvSecret43, "mtproto-tg")); err != nil {
		t.Fatal(err)
	}
	b.mu.RLock()
	entry, ok := b.secretsByID["42"]
	b.mu.RUnlock()
	if !ok {
		t.Fatal("a user re-added after a credential-less removal must be served again")
	}
	if hex.EncodeToString(entry.keyBytes[:]) != nvSecret43 {
		t.Fatal("the re-added user must carry its new secret")
	}
}

func TestNodeVerifyMTProtoRemovalInterleavedWithAddsAndFullSyncsNeverLeavesTheRemovedUser(t *testing.T) {
	const rounds = 200
	for round := 0; round < 20; round++ {
		b := nvMtprotoBackend("mtproto-tg", "mtproto-tg2")
		ctx := context.Background()
		if err := b.SyncUsers(ctx, []*common.User{nvMtprotoMember("42", nvSecret42, "mtproto-tg"), nvMtprotoMember("7", nvSecret43, "mtproto-tg2")}); err != nil {
			t.Fatal(err)
		}
		removal := nvPanelRemoval(t)
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = b.SyncUser(ctx, nvMtprotoMember("42", nvSecret42, "mtproto-tg"))
				_ = b.SyncUser(ctx, removal)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = b.UpdateUsers(ctx, []*common.User{nvMtprotoMember(fmt.Sprintf("u%d", i), nvSecret43, "mtproto-tg2"), removal})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = b.SyncUsers(ctx, []*common.User{nvMtprotoMember("7", nvSecret43, "mtproto-tg2")})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				nvServes(b, "42")
				nvServes(b, "7")
			}
		}()
		wg.Wait()
		if nvServes(b, "42") {
			t.Fatalf("round %d: user 42 still served after removals raced with adds and full syncs", round)
		}
		if !nvServes(b, "7") {
			t.Fatalf("round %d: user 7 lost access though every full sync carried it", round)
		}
	}
}
