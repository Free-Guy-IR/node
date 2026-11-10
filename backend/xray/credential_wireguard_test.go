package xray

import (
	"encoding/base64"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const (
	sharedPeerKey  = "5061737361724775617264576972654775617264506565724b6579310000ffff"
	variantPeerKey = "5061737361724775617264576972654775617264506565724b6579320000ffff"
)

func wireguardPeer(email, publicKey string, allowedIPs ...string) *api.WireguardAccount {
	if len(allowedIPs) == 0 {
		allowedIPs = []string{"10.0.0.2/32"}
	}
	return &api.WireguardAccount{
		BaseAccount: api.BaseAccount{Email: email},
		PublicKey:   publicKey,
		AllowedIPs:  allowedIPs,
	}
}

func wireguardInbound() *Inbound {
	return &Inbound{Tag: "wg", Protocol: Wireguard, Settings: map[string]any{}}
}

func TestTwoWireguardPeersSharingAPublicKeyAreNotBothAdmitted(t *testing.T) {
	inbound := wireguardInbound()
	rejections := &backend.Rejections{}

	admitted, leaving := inbound.admit([]api.Account{
		wireguardPeer("peer.one", sharedPeerKey),
		wireguardPeer("peer.two", sharedPeerKey),
	}, nil, rejections)

	if len(admitted) != 1 {
		t.Fatalf("two peers share the public key %s but %d were admitted, want 1: %v",
			sharedPeerKey, len(admitted), accountEmails(admitted))
	}

	held := admitted[0].GetEmail()
	refused := "peer.two"
	if held == "peer.two" {
		refused = "peer.one"
	}

	if !rejections.Has(refused) {
		t.Fatalf("peer %s shares the public key held by %s but was not rejected", refused, held)
	}
	if !slices.Contains(leaving, refused) {
		t.Fatalf("rejected peer %s was not reported as leaving: %v", refused, leaving)
	}
	if _, waiting := inbound.waiting[refused]; !waiting {
		t.Fatalf("rejected peer %s was not queued as waiting", refused)
	}
}

func TestWireguardPeersWithTheirOwnPublicKeysAreBothAdmitted(t *testing.T) {
	inbound := wireguardInbound()
	rejections := &backend.Rejections{}

	admitted, leaving := inbound.admit([]api.Account{
		wireguardPeer("peer.one", sharedPeerKey),
		wireguardPeer("peer.two", variantPeerKey),
	}, nil, rejections)

	if len(admitted) != 2 {
		t.Fatalf("two peers with their own public keys: %d admitted, want 2: %v",
			len(admitted), accountEmails(admitted))
	}
	if rejections.Len() != 0 {
		t.Fatalf("two peers with their own public keys were rejected: %v", rejections)
	}
	if len(leaving) != 0 {
		t.Fatalf("two peers with their own public keys were reported as leaving: %v", leaving)
	}
	if len(inbound.waiting) != 0 {
		t.Fatalf("two peers with their own public keys were queued as waiting: %v", inbound.waiting)
	}
}

func TestWireguardPeersOverlappingOnlyOnAllowedIPsAreBothAdmitted(t *testing.T) {
	inbound := wireguardInbound()
	rejections := &backend.Rejections{}

	admitted, _ := inbound.admit([]api.Account{
		wireguardPeer("peer.one", sharedPeerKey, "10.0.0.0/24"),
		wireguardPeer("peer.two", variantPeerKey, "10.0.0.7/32"),
	}, nil, rejections)

	if len(admitted) != 2 {
		t.Fatalf("peers whose allowed IPs overlap but whose keys differ: %d admitted, want 2: %v",
			len(admitted), accountEmails(admitted))
	}
	if rejections.Len() != 0 {
		t.Fatalf("an allowed-IP overlap rejected a peer with its own public key: %v", rejections)
	}
}

func TestARotatedWireguardPeerReclaimsItsOwnInboundSlot(t *testing.T) {
	inbound := wireguardInbound()
	rejections := &backend.Rejections{}

	if admitted, _ := inbound.admit([]api.Account{
		wireguardPeer("peer.one", sharedPeerKey),
	}, nil, rejections); len(admitted) != 1 {
		t.Fatalf("the first peer was not admitted: %v", accountEmails(admitted))
	}
	inbound.storeLocked(wireguardPeer("peer.one", sharedPeerKey))

	admitted, _ := inbound.admit([]api.Account{
		wireguardPeer("peer.one", variantPeerKey),
	}, nil, rejections)

	if len(admitted) != 1 {
		t.Fatalf("a peer that rotated its own public key was refused its slot: %v", accountEmails(admitted))
	}
	if rejections.Has("peer.one") {
		t.Fatalf("a peer that rotated its own public key was rejected against itself")
	}
}

func accountEmails(accounts []api.Account) []string {
	emails := make([]string, 0, len(accounts))
	for _, account := range accounts {
		emails = append(emails, account.GetEmail())
	}
	return emails
}

func peerKeyEncodings(t *testing.T) (lower, upper, standardBase64 string) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	return hex.EncodeToString(raw), strings.ToUpper(hex.EncodeToString(raw)), base64.StdEncoding.EncodeToString(raw)
}

func TestEveryEncodingOfOneWireguardKeyNormalisesToTheSamePublicKey(t *testing.T) {
	lower, upper, b64 := peerKeyEncodings(t)

	for _, encoding := range []struct{ name, key string }{
		{"lowercase hex", lower},
		{"uppercase hex", upper},
		{"base64", b64},
	} {
		account, err := api.NewWireguardAccount(wireguardUserWithKey("peer."+encoding.name, encoding.key))
		if err != nil {
			t.Fatalf("%s: NewWireguardAccount: %v", encoding.name, err)
		}
		if account.PublicKey != lower {
			t.Fatalf("%s produced public key %q, want the canonical %q: one key in two encodings claims two slots",
				encoding.name, account.PublicKey, lower)
		}
	}
}

func TestOneWireguardKeyInTwoEncodingsIsNotAdmittedTwice(t *testing.T) {
	lower, upper, _ := peerKeyEncodings(t)

	first, err := api.NewWireguardAccount(wireguardUserWithKey("peer.lower", lower))
	if err != nil {
		t.Fatalf("lowercase hex: NewWireguardAccount: %v", err)
	}
	second, err := api.NewWireguardAccount(wireguardUserWithKey("peer.upper", upper))
	if err != nil {
		t.Fatalf("uppercase hex: NewWireguardAccount: %v", err)
	}

	inbound := wireguardInbound()
	rejections := &backend.Rejections{}
	admitted, leaving := inbound.admit([]api.Account{first, second}, nil, rejections)

	if len(admitted) != 1 {
		t.Fatalf("one key as lowercase and uppercase hex: %d peers admitted, want 1: %v",
			len(admitted), accountEmails(admitted))
	}
	refused := "peer.upper"
	if admitted[0].GetEmail() == "peer.upper" {
		refused = "peer.lower"
	}
	if !rejections.Has(refused) {
		t.Fatalf("peer %s submitted the same key in another encoding but was not rejected", refused)
	}
	if !slices.Contains(leaving, refused) {
		t.Fatalf("rejected peer %s was not reported as leaving: %v", refused, leaving)
	}
	if _, waiting := inbound.waiting[refused]; !waiting {
		t.Fatalf("rejected peer %s was not queued as waiting", refused)
	}
}

func wireguardUserWithKey(email, publicKey string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{"wg"},
		Proxies: &common.Proxy{Wireguard: &common.Wireguard{
			PublicKey: publicKey,
			PeerIps:   []string{"10.0.0.2/32"},
		}},
	}
}
