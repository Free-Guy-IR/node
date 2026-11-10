package xray

import (
	"slices"
	"testing"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/xray/api"
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
