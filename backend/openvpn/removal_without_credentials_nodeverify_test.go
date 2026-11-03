package openvpn

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

const nvPanelRemovalWireHex = "0a0234321200"

func nvRemovalBackend(t *testing.T, tags ...string) *Backend {
	t.Helper()
	instances := make(map[string]*instanceProcess, len(tags))
	for i, tag := range tags {
		instances[tag] = newInstanceProcess(&InstanceConfig{Tag: tag}, PKI{}, i, t.TempDir(), "openvpn", nil, 0)
	}
	return &Backend{instances: instances, order: tags, statsTracker: stats.New()}
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
	if user.GetEmail() != "42" || user.GetProxies().GetOpenVpn() != nil || len(user.GetInbounds()) != 0 {
		t.Fatalf("decoded removal is not the panel shape: %v", user)
	}
	return user
}

func nvOpenVPNMember(email, pw string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies:  &common.Proxy{OpenVpn: &common.OpenVpnUser{Username: email, Password: pw}},
	}
}

func nvAuthenticates(b *Backend, tag, username, pw string) bool {
	_, _, ok := b.instances[tag].auth.authenticate(username, pw)
	return ok
}

func TestNodeVerifyPanelRemovalWithoutCredentialsRevokesOpenVPN(t *testing.T) {
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
				b := nvRemovalBackend(t, "ovpn-a", "ovpn-b")
				if err := call(b, nvOpenVPNMember("42", "pw-42", "ovpn-a", "ovpn-b")); err != nil {
					t.Fatal(err)
				}
				if err := call(b, nvOpenVPNMember("43", "pw-43", "ovpn-a")); err != nil {
					t.Fatal(err)
				}
				if !nvAuthenticates(b, "ovpn-a", "42", "pw-42") || !nvAuthenticates(b, "ovpn-b", "42", "pw-42") {
					t.Fatal("precondition: user 42 must authenticate before the removal")
				}
				if err := call(b, removal(t)); err != nil {
					t.Fatal(err)
				}
				for _, tag := range []string{"ovpn-a", "ovpn-b"} {
					if nvAuthenticates(b, tag, "42", "pw-42") {
						t.Fatalf("user 42 still authenticates on %s after the panel removed it", tag)
					}
				}
				if !nvAuthenticates(b, "ovpn-a", "43", "pw-43") {
					t.Fatal("removing user 42 must not revoke user 43")
				}
			})
		}
	}
}

func TestNodeVerifyRemovalDoesNotTouchAUserWhoseEmailOnlySharesAPrefix(t *testing.T) {
	b := nvRemovalBackend(t, "ovpn-a")
	for _, u := range []*common.User{nvOpenVPNMember("42", "pw-42", "ovpn-a"), nvOpenVPNMember("420", "pw-420", "ovpn-a"), nvOpenVPNMember("4", "pw-4", "ovpn-a")} {
		if err := b.SyncUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SyncUser(context.Background(), nvPanelRemoval(t)); err != nil {
		t.Fatal(err)
	}
	if nvAuthenticates(b, "ovpn-a", "42", "pw-42") {
		t.Fatal("user 42 still authenticates after its removal")
	}
	if !nvAuthenticates(b, "ovpn-a", "420", "pw-420") || !nvAuthenticates(b, "ovpn-a", "4", "pw-4") {
		t.Fatal("the removal of 42 revoked a different user")
	}
}

func TestNodeVerifyAUserRemovedWithoutCredentialsCanBeAddedBack(t *testing.T) {
	b := nvRemovalBackend(t, "ovpn-a")
	ctx := context.Background()
	if err := b.SyncUser(ctx, nvOpenVPNMember("42", "pw-42", "ovpn-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncUser(ctx, nvPanelRemoval(t)); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncUser(ctx, nvOpenVPNMember("42", "pw-new", "ovpn-a")); err != nil {
		t.Fatal(err)
	}
	if !nvAuthenticates(b, "ovpn-a", "42", "pw-new") {
		t.Fatal("a user re-added after a credential-less removal must authenticate with its new password")
	}
	if nvAuthenticates(b, "ovpn-a", "42", "pw-42") {
		t.Fatal("the old password must not authenticate")
	}
}

func TestNodeVerifyRemovalInterleavedWithAddsAndFullSyncsNeverLeavesTheRemovedUser(t *testing.T) {
	const rounds = 200
	for round := 0; round < 20; round++ {
		b := nvRemovalBackend(t, "ovpn-a", "ovpn-b")
		ctx := context.Background()
		if err := b.SyncUsers(ctx, []*common.User{nvOpenVPNMember("42", "pw-42", "ovpn-a", "ovpn-b"), nvOpenVPNMember("7", "pw-7", "ovpn-a")}); err != nil {
			t.Fatal(err)
		}
		removal := nvPanelRemoval(t)
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = b.SyncUser(ctx, nvOpenVPNMember("42", "pw-42", "ovpn-a", "ovpn-b"))
				_ = b.SyncUser(ctx, removal)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = b.UpdateUsers(ctx, []*common.User{nvOpenVPNMember(fmt.Sprintf("u%d", i), "pw", "ovpn-b"), removal})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = b.SyncUsers(ctx, []*common.User{nvOpenVPNMember("7", "pw-7", "ovpn-a")})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				nvAuthenticates(b, "ovpn-a", "42", "pw-42")
				nvAuthenticates(b, "ovpn-b", "7", "pw-7")
			}
		}()
		wg.Wait()
		for _, tag := range []string{"ovpn-a", "ovpn-b"} {
			if nvAuthenticates(b, tag, "42", "pw-42") {
				t.Fatalf("round %d: user 42 authenticates on %s after removals raced with adds and full syncs", round, tag)
			}
		}
		if !nvAuthenticates(b, "ovpn-a", "7", "pw-7") {
			t.Fatalf("round %d: user 7 lost access though every full sync carried it", round)
		}
	}
}

func TestNodeVerifyABulkUpdateOfTwentyThousandUsersStaysLinear(t *testing.T) {
	const n = 20000
	b := nvRemovalBackend(t, "ovpn-a", "ovpn-b")
	users := make([]*common.User, 0, n)
	for i := 0; i < n; i++ {
		users = append(users, nvOpenVPNMember(fmt.Sprintf("%d", i), "pw", "ovpn-a", "ovpn-b"))
	}
	ctx := context.Background()
	if err := b.SyncUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := b.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("UpdateUsers of %d users on 2 instances took %v; the base did it in about 20ms", n, took)
	}
}
