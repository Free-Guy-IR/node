package xray

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const wireguardInbounds = `{"inbounds":[
	{"tag":"wg","protocol":"wireguard","settings":{"secretKey":"","peers":[]}}
]}`

const mixedWireguardInbounds = `{"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"wg","protocol":"wireguard","settings":{"secretKey":"","peers":[]}}
]}`

func wireguardKey(fill byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string([]byte{fill}), 32)))
}

func wireguardUser(email, publicKey, preSharedKey string, peerIPs ...string) *common.User {
	if len(peerIPs) == 0 {
		peerIPs = []string{"10.0.0.2/32"}
	}
	return &common.User{
		Email:    email,
		Inbounds: []string{"wg"},
		Proxies: &common.Proxy{Wireguard: &common.Wireguard{
			PublicKey:    publicKey,
			PreSharedKey: preSharedKey,
			PeerIps:      peerIPs,
		}},
	}
}

func mixedUser(email string, wireguard *common.Wireguard, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Vless:     &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"},
			Wireguard: wireguard,
		},
	}
}

func newWireguardXray(t *testing.T, inbounds string) (*Xray, *modelCore) {
	t.Helper()

	core := newModelCore(false)
	return newModelXray(t, core, inbounds), core
}

func wireguardPublicKey(t *testing.T, x *Xray, email string) string {
	t.Helper()

	account, ok := storedAccount(x, "wg", email).(*api.WireguardAccount)
	if !ok {
		t.Fatalf("the wg inbound holds no wireguard account for %s", email)
	}
	return account.PublicKey
}

func TestSyncUserWireguardPeerLifecycle(t *testing.T) {
	keyA, keyB, pskA, pskB := wireguardKey(0x11), wireguardKey(0x22), wireguardKey(0x33), wireguardKey(0x44)

	for _, tc := range []struct {
		name string
		from *common.User
		to   *common.User
		want []string
	}{
		{"unchanged peer", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyA, pskA), nil},
		{"public key rotated", wireguardUser("1.alice", keyA, ""), wireguardUser("1.alice", keyB, ""), []string{revokeOperation, addOperation}},
		{"public key rotated with psk", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyB, pskA), []string{revokeOperation, addOperation}},
		{"psk removed", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyA, ""), []string{revokeOperation, addOperation}},
		{"psk changed", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyA, pskB), []string{revokeOperation, addOperation}},
		{"psk added", wireguardUser("1.alice", keyA, ""), wireguardUser("1.alice", keyA, pskA), []string{revokeOperation, addOperation}},
		{"allowed ips changed", wireguardUser("1.alice", keyA, pskA, "10.0.0.2/32"), wireguardUser("1.alice", keyA, pskA, "10.0.0.3/32"), []string{revokeOperation, addOperation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, core := newWireguardXray(t, wireguardInbounds)
			ctx := syncTestContext(t)

			if err := x.SyncUser(ctx, tc.from); err != nil {
				t.Fatal(err)
			}
			if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation}) {
				t.Fatalf("first sync sent %v, want a single add", got)
			}

			if err := x.SyncUser(ctx, tc.to); err != nil {
				t.Fatal(err)
			}
			if got := wireguardCoreOperations(core)[1:]; !slices.Equal(got, tc.want) {
				t.Fatalf("second sync sent %v, want %v", got, tc.want)
			}
			requireCoreMatchesLocal(t, x, core)
		})
	}
}

func TestUpdateUsersWireguardPeerLifecycle(t *testing.T) {
	keyA, keyB, pskA, pskB := wireguardKey(0x11), wireguardKey(0x22), wireguardKey(0x33), wireguardKey(0x44)

	for _, tc := range []struct {
		name string
		from *common.User
		to   *common.User
		want []string
	}{
		{"unchanged peer", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyA, pskA), nil},
		{"public key rotated", wireguardUser("1.alice", keyA, ""), wireguardUser("1.alice", keyB, ""), []string{revokeOperation, addOperation}},
		{"psk removed", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyA, ""), []string{revokeOperation, addOperation}},
		{"psk changed", wireguardUser("1.alice", keyA, pskA), wireguardUser("1.alice", keyA, pskB), []string{revokeOperation, addOperation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, core := newWireguardXray(t, wireguardInbounds)
			ctx := syncTestContext(t)

			if err := x.UpdateUsers(ctx, []*common.User{tc.from}); err != nil {
				t.Fatal(err)
			}
			if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation}) {
				t.Fatalf("first update sent %v, want a single add", got)
			}

			if err := x.UpdateUsers(ctx, []*common.User{tc.to}); err != nil {
				t.Fatal(err)
			}
			if got := wireguardCoreOperations(core)[1:]; !slices.Equal(got, tc.want) {
				t.Fatalf("second update sent %v, want %v", got, tc.want)
			}
			requireCoreMatchesLocal(t, x, core)
		})
	}
}

func TestSyncUserTracksTheRotatedWireguardKey(t *testing.T) {
	x, core := newWireguardXray(t, wireguardInbounds)
	ctx := syncTestContext(t)
	keyA, keyB := wireguardKey(0x11), wireguardKey(0x22)

	for _, key := range []string{keyA, keyB, keyB} {
		if err := x.SyncUser(ctx, wireguardUser("1.alice", key, "")); err != nil {
			t.Fatal(err)
		}
	}
	if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation, revokeOperation, addOperation}) {
		t.Fatalf("operations on the wireguard inbound = %v, want add, revoke, add", got)
	}
	if got := wireguardPublicKey(t, x, "1.alice"); got != strings.Repeat("22", 32) {
		t.Fatalf("stored public key = %s, want the rotated key", got)
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestUpdateUsersHoldsBackARotatedWireguardPeerWhenTheRevokeFails(t *testing.T) {
	x, core := newWireguardXray(t, wireguardInbounds)
	ctx := syncTestContext(t)
	old := wireguardUser("1.alice", wireguardKey(0x11), "")
	rotated := wireguardUser("1.alice", wireguardKey(0x22), "")

	if err := x.UpdateUsers(ctx, []*common.User{old}); err != nil {
		t.Fatal(err)
	}
	live := core.state()["wg"]["1.alice"]
	if live == "" {
		t.Fatal("the core holds no wireguard peer after the first update")
	}

	core.setFault(func(op, tag, _ string) (bool, error) {
		if tag == "wg" && (op == revokeOperation || op == removeOperation) {
			return false, errors.New("the core refused to drop the peer")
		}
		return false, nil
	})
	if err := x.UpdateUsers(ctx, []*common.User{rotated}); err == nil {
		t.Fatal("a failed wireguard revoke was not reported")
	}
	if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation, revokeOperation + ":failed", removeOperation + ":failed"}) {
		t.Fatalf("operations on the wireguard inbound = %v, want add then a failed revoke and removal and no add", got)
	}
	if got := core.state()["wg"]["1.alice"]; got != live {
		t.Fatalf("the core's wireguard peer is %q after a failed revoke, want the old peer %q", got, live)
	}

	core.setFault(nil)
	if err := x.UpdateUsers(ctx, []*common.User{rotated}); err != nil {
		t.Fatal(err)
	}
	if got := core.state()["wg"]["1.alice"]; got == live {
		t.Fatalf("the core still holds the old wireguard peer %q after the retry", got)
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRejectsInvalidWireguardSettingsWithoutDroppingThePeer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invalid *common.User
	}{
		{"invalid key", wireguardUser("1.alice", "invalid-key", "")},
		{"malformed peer IP", wireguardUser("1.alice", wireguardKey(0x11), "", "not-a-cidr")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, core := newWireguardXray(t, wireguardInbounds)
			ctx := syncTestContext(t)
			if err := x.SyncUser(ctx, wireguardUser("1.alice", wireguardKey(0x11), "")); err != nil {
				t.Fatal(err)
			}
			core.take()

			if err := x.SyncUser(ctx, tc.invalid); err == nil {
				t.Fatal("expected invalid WireGuard account error")
			}
			requireOperations(t, core.take())
			if got := wireguardPublicKey(t, x, "1.alice"); got != strings.Repeat("11", 32) {
				t.Fatalf("stored public key after an invalid update = %s, want the live key", got)
			}
			requireCoreMatchesLocal(t, x, core)
		})
	}
}

func TestSyncUserRemovesWireguardPeerWithValidIPOutsideInbound(t *testing.T) {
	x, core := newWireguardXray(t, wireguardInbounds)
	x.config.InboundConfigs[0].Settings["address"] = []any{"10.0.0.1/24"}
	ctx := syncTestContext(t)
	user := wireguardUser("1.alice", wireguardKey(0x11), "", "10.0.0.2/32")
	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	core.take()

	outside := wireguardUser(user.Email, wireguardKey(0x11), "", "10.1.0.2/32")
	if err := x.SyncUser(ctx, outside); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn("wg")...)
	if storedAccount(x, "wg", user.Email) != nil {
		t.Fatal("peer remained cached after leaving the inbound subnet")
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSetupUserAccountTreatsKeylessWireguardAsAbsent(t *testing.T) {
	emptyFromWire := &common.Proxy{}
	if err := proto.Unmarshal([]byte{0x2a, 0x00}, emptyFromWire); err != nil {
		t.Fatal(err)
	}
	if emptyFromWire.GetWireguard() == nil {
		t.Fatal("expected a non-nil empty WireGuard message")
	}

	for _, tc := range []struct {
		name      string
		wireguard *common.Wireguard
	}{
		{"empty message from the wire", emptyFromWire.GetWireguard()},
		{"peer IPs without a key", &common.Wireguard{PeerIps: []string{"10.0.0.2/32"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, err := setupUserAccount(mixedUser("1.alice", tc.wireguard, "vless", "wg"))
			if err != nil {
				t.Fatalf("setupUserAccount() error = %v", err)
			}
			if settings.Vless == nil {
				t.Fatal("expected the VLESS account to be set up")
			}
			if settings.Wireguard != nil {
				t.Fatalf("expected no WireGuard account, got %#v", settings.Wireguard)
			}
		})
	}
}

func TestSyncUserAppliesAUserWithAnEmptyWireguardMessage(t *testing.T) {
	x, core := newWireguardXray(t, mixedWireguardInbounds)
	ctx := syncTestContext(t)

	if err := x.SyncUser(ctx, mixedUser("1.alice", &common.Wireguard{}, "vless", "wg")); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), addedOn("vless")...)
	if storedAccount(x, "vless", "1.alice") == nil {
		t.Fatal("VLESS account was not stored")
	}
	if storedAccount(x, "wg", "1.alice") != nil {
		t.Fatal("a WireGuard peer was stored without keys")
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRemovesAUserWithAnEmptyWireguardMessage(t *testing.T) {
	x, core := newWireguardXray(t, mixedWireguardInbounds)
	ctx := syncTestContext(t)
	valid := &common.Wireguard{PublicKey: wireguardKey(0x11), PeerIps: []string{"10.0.0.2/32"}}

	if err := x.SyncUser(ctx, mixedUser("1.alice", valid, "vless", "wg")); err != nil {
		t.Fatal(err)
	}
	core.take()

	if err := x.SyncUser(ctx, mixedUser("1.alice", &common.Wireguard{})); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn("vless", "wg")...)
	for _, tag := range []string{"vless", "wg"} {
		if storedAccount(x, tag, "1.alice") != nil {
			t.Fatalf("user remained cached on %s after removal", tag)
		}
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserKeepsTheWireguardPeerOfAUserWithValidKeys(t *testing.T) {
	x, core := newWireguardXray(t, mixedWireguardInbounds)
	ctx := syncTestContext(t)
	valid := &common.Wireguard{PublicKey: wireguardKey(0x11), PeerIps: []string{"10.0.0.2/32"}}

	if err := x.SyncUser(ctx, mixedUser("1.alice", valid, "vless", "wg")); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), addedOn("vless", "wg")...)
	if got := wireguardPublicKey(t, x, "1.alice"); got != strings.Repeat("11", 32) {
		t.Fatalf("stored public key = %s, want the user's key", got)
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserAppliesOtherInboundsWhenWireguardSettingsAreInvalid(t *testing.T) {
	x, core := newWireguardXray(t, mixedWireguardInbounds)
	ctx := syncTestContext(t)
	valid := &common.Wireguard{PublicKey: wireguardKey(0x11), PeerIps: []string{"10.0.0.2/32"}}

	if err := x.SyncUser(ctx, mixedUser("1.alice", valid, "wg")); err != nil {
		t.Fatal(err)
	}
	core.take()

	invalid := &common.Wireguard{PublicKey: "invalid-key", PeerIps: []string{"10.0.0.2/32"}}
	if err := x.SyncUser(ctx, mixedUser("1.alice", invalid, "vless", "wg")); err == nil {
		t.Fatal("expected invalid WireGuard account error")
	}
	requireOperations(t, core.take(), addedOn("vless")...)
	if got := wireguardPublicKey(t, x, "1.alice"); got != strings.Repeat("11", 32) {
		t.Fatalf("live WireGuard peer changed after an invalid update: %s", got)
	}

	if err := x.SyncUser(ctx, mixedUser("1.alice", invalid)); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn("vless", "wg")...)
	requireCoreMatchesLocal(t, x, core)
}
