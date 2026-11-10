package api

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/xtls/xray-core/proxy/wireguard"
	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
)

func TestWireguardKeyToHex(t *testing.T) {
	raw := strings.Repeat("\x11", 32)
	want := strings.Repeat("11", 32)
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))

	for _, tc := range []struct {
		name string
		key  string
		want string
	}{
		{"base64", encoded, want},
		{"hex", want, want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WireguardKeyToHex(tc.key)
			if err != nil || got != tc.want {
				t.Fatalf("WireguardKeyToHex() = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}

func TestWireguardKeyToHexRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "not-a-key", base64.StdEncoding.EncodeToString([]byte("short")), strings.Repeat("z", 64)} {
		if got, err := WireguardKeyToHex(key); err == nil || got != "" {
			t.Fatalf("WireguardKeyToHex(%q) = %q, %v; want empty result and error", key, got, err)
		}
	}
}

func TestNewWireguardAccountNormalizesPSKAndMessage(t *testing.T) {
	publicKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("P", 32)))
	psk := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("S", 32)))
	account, err := NewWireguardAccount(&common.User{
		Email: "user@example.test",
		Proxies: &common.Proxy{Wireguard: &common.Wireguard{
			PublicKey: publicKey, PreSharedKey: psk, PeerIps: []string{"10.0.0.2/32"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.GetEmail() != "user@example.test" || account.PreSharedKey != strings.Repeat("53", 32) {
		t.Fatalf("account = %#v", account)
	}
	message, err := account.Message()
	if err != nil {
		t.Fatal(err)
	}
	peer := new(wireguard.PeerConfig)
	if err := proto.Unmarshal(message.Value, peer); err != nil {
		t.Fatal(err)
	}
	if peer.PreSharedKey != account.PreSharedKey {
		t.Fatalf("peer message = %#v, want PSK %q", peer, account.PreSharedKey)
	}
	if peer.PublicKey != strings.Repeat("50", 32) {
		t.Fatalf("peer message = %#v, want the hex public key", peer)
	}
	if len(peer.AllowedIps) != 1 || peer.AllowedIps[0] != "10.0.0.2/32" {
		t.Fatalf("peer message = %#v, want the peer IP", peer)
	}
}

func TestTheCoreAcceptsTheWireguardPeerMessageOnlyInHex(t *testing.T) {
	publicKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("P", 32)))
	psk := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("S", 32)))
	account, err := NewWireguardAccount(&common.User{
		Email: "user@example.test",
		Proxies: &common.Proxy{Wireguard: &common.Wireguard{
			PublicKey: publicKey, PreSharedKey: psk, PeerIps: []string{"10.0.0.2/32", "fd10::2/128"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := account.Message()
	if err != nil {
		t.Fatal(err)
	}
	peer := new(wireguard.PeerConfig)
	if err := proto.Unmarshal(message.Value, peer); err != nil {
		t.Fatal(err)
	}

	built, err := peer.AsAccount()
	if err != nil {
		t.Fatalf("xray refused the peer the node builds: %v", err)
	}
	memory, ok := built.(*wireguard.MemoryAccount)
	if !ok {
		t.Fatalf("xray built %T from the peer, want a wireguard.MemoryAccount", built)
	}
	if hex.EncodeToString(memory.Pub[:]) != strings.Repeat("50", 32) {
		t.Fatalf("xray read the public key as %s", hex.EncodeToString(memory.Pub[:]))
	}
	if memory.PreSharedKey != strings.Repeat("53", 32) {
		t.Fatalf("xray read the pre-shared key as %s", memory.PreSharedKey)
	}
	if len(memory.AllowedIPs) != 2 {
		t.Fatalf("xray read %d allowed IPs, want 2", len(memory.AllowedIPs))
	}

	raw := &wireguard.PeerConfig{PublicKey: publicKey, AllowedIps: []string{"10.0.0.2/32"}}
	if _, err := raw.AsAccount(); err == nil {
		t.Fatal("xray accepted a base64 public key, so WireguardKeyToHex would not be load-bearing")
	}
}

func TestNewWireguardAccountRejectsMalformedPeerIPs(t *testing.T) {
	publicKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("P", 32)))
	for _, peerIPs := range [][]string{
		{"not-a-cidr"},
		{"10.0.0.2"},
		{"10.0.0.2/32", "bad"},
	} {
		user := &common.User{Proxies: &common.Proxy{Wireguard: &common.Wireguard{
			PublicKey: publicKey,
			PeerIps:   peerIPs,
		}}}
		if account, err := NewWireguardAccount(user); err == nil || account != nil {
			t.Fatalf("NewWireguardAccount(%v) = %#v, %v; want an error", peerIPs, account, err)
		}
	}
}

func TestNewWireguardAccountRequiresKeyAndPeerIPs(t *testing.T) {
	publicKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("P", 32)))
	for _, tc := range []struct {
		name string
		user *common.User
	}{
		{"no proxies", &common.User{}},
		{"no wireguard", &common.User{Proxies: &common.Proxy{}}},
		{"no public key", &common.User{Proxies: &common.Proxy{Wireguard: &common.Wireguard{PeerIps: []string{"10.0.0.2/32"}}}}},
		{"no peer IPs", &common.User{Proxies: &common.Proxy{Wireguard: &common.Wireguard{PublicKey: publicKey}}}},
		{"invalid PSK", &common.User{Proxies: &common.Proxy{Wireguard: &common.Wireguard{
			PublicKey: publicKey, PreSharedKey: "not-a-key", PeerIps: []string{"10.0.0.2/32"},
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if account, err := NewWireguardAccount(tc.user); err == nil || account != nil {
				t.Fatalf("NewWireguardAccount() = %#v, %v; want an error", account, err)
			}
		})
	}
}
