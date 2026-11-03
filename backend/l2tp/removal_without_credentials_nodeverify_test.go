package l2tp

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
)

func nvL2TPPanelRemoval(t *testing.T) *common.User {
	t.Helper()
	raw, err := hex.DecodeString("0a0234321200")
	if err != nil {
		t.Fatal(err)
	}
	user := &common.User{}
	if err := proto.Unmarshal(raw, user); err != nil {
		t.Fatal(err)
	}
	if user.GetEmail() != "42" || user.GetProxies().GetL2Tp() != nil || len(user.GetInbounds()) != 0 {
		t.Fatalf("decoded removal is not the panel shape: %v", user)
	}
	return user
}

func nvL2TPMember(email, pw string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{"l2tp-in"},
		Proxies:  &common.Proxy{L2Tp: &common.L2TpUser{Username: email, Password: pw}},
	}
}

func TestNodeVerifyL2TPPanelRemovalWithoutCredentialsRevokesTheUser(t *testing.T) {
	s := newUserStore("l2tp-in")
	s.replaceAll([]*common.User{nvL2TPMember("42", "pw-42"), nvL2TPMember("420", "pw-420")})
	username, _, removed := s.applyUser(nvL2TPPanelRemoval(t))
	if username != "42" || !removed {
		t.Fatalf("applyUser(removal) = %q removed=%v, want 42 removed", username, removed)
	}
	if s.has("42") {
		t.Fatal("user 42 still holds a CHAP secret after the panel removed it")
	}
	if !s.has("420") {
		t.Fatal("removing user 42 dropped user 420")
	}
	for _, entry := range s.snapshot() {
		if entry.username == "42" {
			t.Fatal("user 42 is still written to chap-secrets")
		}
	}
}
