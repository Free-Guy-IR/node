package xray

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/wireguard"

	"github.com/pasarguard/node/common"
)

const wireguardPeerInbounds = `{"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"wg","protocol":"wireguard","settings":{"secretKey":"","address":["10.0.0.1/24"],"peers":[]}}
]}`

func wireguardPeerKey(fill byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string([]byte{fill}), 32)))
}

func wireguardPeerUser(email, publicKey string, peerIPs ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{"vless", "wg"},
		Proxies: &common.Proxy{
			Vless:     &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"},
			Wireguard: &common.Wireguard{PublicKey: publicKey, PeerIps: peerIPs},
		},
	}
}

func wireguardPeerConfig(t *testing.T) *Config {
	t.Helper()

	cfg, err := NewConfig(wireguardPeerInbounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func wireguardInboundSettings(t *testing.T, cfg *Config) map[string]any {
	t.Helper()

	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Inbounds []struct {
			Tag      string         `json:"tag"`
			Settings map[string]any `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, inbound := range out.Inbounds {
		if inbound.Tag == "wg" {
			return inbound.Settings
		}
	}
	t.Fatal("the serialized config carries no wg inbound")
	return nil
}

func wireguardSerializedPeers(t *testing.T, settings map[string]any) []map[string]any {
	t.Helper()

	if _, ok := settings["clients"]; ok {
		t.Fatalf("the wireguard inbound was serialized with a clients key: %v", settings)
	}
	entries, ok := settings["peers"].([]any)
	if !ok {
		t.Fatalf("the wireguard inbound was serialized with peers = %T (%v)", settings["peers"], settings["peers"])
	}
	peers := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("serialized peer = %T, want an object", entry)
		}
		peers = append(peers, fields)
	}
	return peers
}

func wireguardCoreOperations(core *modelCore) []string {
	var operations []string
	for _, entry := range core.allEntries() {
		if entry.tag != "wg" {
			continue
		}
		if entry.failed {
			operations = append(operations, entry.op+":failed")
			continue
		}
		operations = append(operations, entry.op)
	}
	return operations
}

func TestSyncUsersSerializesWireguardPeersInsteadOfClients(t *testing.T) {
	cfg := wireguardPeerConfig(t)
	cfg.syncUsers([]*common.User{wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")})

	peers := wireguardSerializedPeers(t, wireguardInboundSettings(t, cfg))
	if len(peers) != 1 {
		t.Fatalf("serialized peers = %v, want one peer", peers)
	}
	if email, _ := peers[0]["email"].(string); email != "1.alice" {
		t.Fatalf("serialized peer email = %q, want 1.alice", email)
	}
	if key, _ := peers[0]["publicKey"].(string); key != strings.Repeat("11", 32) {
		t.Fatalf("serialized peer publicKey = %q, want the hex of the base64 key", key)
	}
	allowed, _ := peers[0]["allowedIPs"].([]any)
	if len(allowed) != 1 || allowed[0] != "10.0.0.2/32" {
		t.Fatalf("serialized peer allowedIPs = %v, want [10.0.0.2/32]", allowed)
	}
}

func TestTheCoreReadsTheSerializedWireguardPeers(t *testing.T) {
	cfg := wireguardPeerConfig(t)
	cfg.syncUsers([]*common.User{wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")})

	raw, err := json.Marshal(wireguardInboundSettings(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	var wire conf.WireGuardConfig
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Peers) != 1 {
		t.Fatalf("the core reads %d peers from the serialized inbound, want one", len(wire.Peers))
	}
	if wire.Peers[0].Email != "1.alice" {
		t.Fatalf("the core reads the peer's email as %q", wire.Peers[0].Email)
	}
	peer, err := wire.Peers[0].Build()
	if err != nil {
		t.Fatalf("the core refused the serialized peer: %v", err)
	}
	if peer.GetPublicKey() != strings.Repeat("11", 32) {
		t.Fatalf("the core reads the peer's public key as %q", peer.GetPublicKey())
	}
	account, err := peer.AsAccount()
	if err != nil {
		t.Fatalf("the core refused the serialized peer as an account: %v", err)
	}
	memory, ok := account.(*wireguard.MemoryAccount)
	if !ok {
		t.Fatalf("the core built %T from the serialized peer", account)
	}
	if len(memory.AllowedIPs) != 1 || memory.AllowedIPs[0].String() != "10.0.0.2/32" {
		t.Fatalf("the core reads the peer's allowed IPs as %v", memory.AllowedIPs)
	}
}

func TestSyncUsersSerializesAnEmptyWireguardPeerList(t *testing.T) {
	if peers := wireguardSerializedPeers(t, wireguardInboundSettings(t, wireguardPeerConfig(t))); len(peers) != 0 {
		t.Fatalf("serialized peers without users = %v, want none", peers)
	}
}

func TestSyncUsersDropsAWireguardPeerOutsideTheInboundSubnet(t *testing.T) {
	cfg := wireguardPeerConfig(t)
	cfg.syncUsers([]*common.User{wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.9.0.2/32")})

	if peers := wireguardSerializedPeers(t, wireguardInboundSettings(t, cfg)); len(peers) != 0 {
		t.Fatalf("serialized peers = %v, want none for an IP outside the inbound subnet", peers)
	}
}

func TestSyncUserAddsAWireguardPeerToTheCore(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, wireguardPeerInbounds)
	ctx := syncTestContext(t)

	if err := x.SyncUser(ctx, wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), addedOn("vless", "wg")...)
	requireCoreMatchesLocal(t, x, core)
}

func TestUpdateUsersAddsAWireguardPeerToTheCore(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, wireguardPeerInbounds)
	ctx := syncTestContext(t)

	if err := x.UpdateUsers(ctx, []*common.User{wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")}); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), addedOn("vless", "wg")...)
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRevokesTheOldWireguardPeerBeforeAddingARotatedKey(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, wireguardPeerInbounds)
	ctx := syncTestContext(t)

	if err := x.SyncUser(ctx, wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")); err != nil {
		t.Fatal(err)
	}
	if err := x.SyncUser(ctx, wireguardPeerUser("1.alice", wireguardPeerKey(0x22), "10.0.0.2/32")); err != nil {
		t.Fatal(err)
	}

	if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation, revokeOperation, addOperation}) {
		t.Fatalf("operations on the wireguard inbound = %v, want add, revoke, add", got)
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserHoldsBackARotatedWireguardPeerWhenTheRevokeFails(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, wireguardPeerInbounds)
	ctx := syncTestContext(t)
	old := wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")
	rotated := wireguardPeerUser("1.alice", wireguardPeerKey(0x22), "10.0.0.2/32")

	if err := x.SyncUser(ctx, old); err != nil {
		t.Fatal(err)
	}
	live := core.state()["wg"]["1.alice"]
	if live == "" {
		t.Fatal("the core holds no wireguard peer after the first sync")
	}

	core.setFault(func(op, tag, _ string) (bool, error) {
		if tag == "wg" && (op == revokeOperation || op == removeOperation) {
			return false, errors.New("the core refused to drop the peer")
		}
		return false, nil
	})
	if err := x.SyncUser(ctx, rotated); err == nil {
		t.Fatal("a failed wireguard revoke was not reported")
	}
	if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation, revokeOperation + ":failed", removeOperation + ":failed"}) {
		t.Fatalf("operations on the wireguard inbound = %v, want add then a failed revoke and removal and no add", got)
	}
	if got := core.state()["wg"]["1.alice"]; got != live {
		t.Fatalf("the core's wireguard peer is %q after a failed revoke, want the old peer %q", got, live)
	}

	core.setFault(nil)
	if err := x.SyncUser(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	if got := wireguardCoreOperations(core); !slices.Equal(got, []string{addOperation, revokeOperation + ":failed", removeOperation + ":failed", revokeOperation, addOperation}) {
		t.Fatalf("operations on the wireguard inbound = %v, want the retried revoke and add", got)
	}
	if got := core.state()["wg"]["1.alice"]; got == live {
		t.Fatalf("the core still holds the old wireguard peer %q after the retry", got)
	}
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRemovesAWireguardPeerThatLeavesTheInbound(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, wireguardPeerInbounds)
	ctx := syncTestContext(t)
	user := wireguardPeerUser("1.alice", wireguardPeerKey(0x11), "10.0.0.2/32")

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	core.take()

	user.Inbounds = []string{"vless"}
	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn("wg")...)
	if _, held := core.state()["wg"]["1.alice"]; held {
		t.Fatal("the core still holds the wireguard peer of a user that left the inbound")
	}
	requireCoreMatchesLocal(t, x, core)
}
