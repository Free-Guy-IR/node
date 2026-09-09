package mtproto

import (
	"context"
	"testing"

	"github.com/pasarguard/node/common"
)

func scopedBackend(tags ...string) *Backend {
	b := &Backend{instances: map[string]*proxyInstance{}, secretsByID: map[string]secretEntry{}}
	for _, tag := range tags {
		b.instances[tag] = &proxyInstance{}
	}
	return b
}

func mtprotoUser(email string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Mtproto: &common.MtprotoUser{
				Username: email,
				Secret:   "00112233445566778899aabbccddeeff",
			},
		},
	}
}

func TestUserOnThisNodesInboundIsAccepted(t *testing.T) {
	b := scopedBackend("mtproto-tg", "mtproto-tg2")
	b.applyInitialUsers([]*common.User{mtprotoUser("ok@x", "mtproto-tg")})

	if _, present := b.secretsByID["ok@x"]; !present {
		t.Fatal("a user on this backends own inbound must keep mtproto access")
	}
}

func TestUserWithCredentialsButNotOnThisNodesInboundIsRefused(t *testing.T) {
	b := scopedBackend("mtproto-tg")
	b.applyInitialUsers([]*common.User{mtprotoUser("leak@x", "Shadowsocks TCP", "hy2-in")})

	if _, present := b.secretsByID["leak@x"]; present {
		t.Fatal("a user carrying mtproto credentials but not on this nodes mtproto inbound must NOT get access")
	}
}

func TestSingleCoreMtprotoNodeIsUnaffected(t *testing.T) {
	b := scopedBackend("mtproto-tg", "mtproto-tg2", "mtproto-tg-plain", "mtproto-tg443")
	users := []*common.User{
		mtprotoUser("a@x", "mtproto-tg"),
		mtprotoUser("b@x", "mtproto-tg2"),
		mtprotoUser("c@x", "mtproto-tg443"),
	}
	b.applyInitialUsers(users)

	if len(b.secretsByID) != len(users) {
		t.Fatalf("every user the panel sends to a mtproto-only node must be kept, got %d of %d", len(b.secretsByID), len(users))
	}
}

func TestRevokingTheInboundRemovesAccess(t *testing.T) {
	b := scopedBackend("mtproto-tg")
	b.applyInitialUsers([]*common.User{mtprotoUser("rev@x", "mtproto-tg")})
	if _, present := b.secretsByID["rev@x"]; !present {
		t.Fatal("precondition: user should start with access")
	}

	b.updateUsers([]*common.User{mtprotoUser("rev@x", "hy2-in")})
	if _, present := b.secretsByID["rev@x"]; present {
		t.Fatal("dropping the mtproto inbound must revoke access")
	}
}

func TestSyncUserRefusesAnOffInboundUser(t *testing.T) {
	b := scopedBackend("mtproto-tg")
	b.applyInitialUsers([]*common.User{mtprotoUser("x@x", "mtproto-tg")})

	if err := b.SyncUser(context.Background(), mtprotoUser("x@x", "hy2-in")); err != nil {
		t.Fatalf("SyncUser: %v", err)
	}
	if _, present := b.secretsByID["x@x"]; present {
		t.Fatal("SyncUser must revoke a user that is no longer on an mtproto inbound")
	}
}
