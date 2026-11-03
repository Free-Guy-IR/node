package mtproto

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const (
	removalTag    = "mtproto-tg-plain"
	removalAlice  = "101"
	removalBob    = "102"
	removalSecret = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

type removalShape struct {
	name  string
	apply func(ctx context.Context, b *Backend, email string) error
}

func panelMainMtprotoRemovalShapes() []removalShape {
	payloads := []struct {
		name string
		user func(email string) *common.User
	}{
		{"empty-secret-block", func(email string) *common.User {
			return &common.User{Email: email, Proxies: &common.Proxy{Mtproto: &common.MtprotoUser{Username: email}}, Inbounds: []string{}}
		}},
		{"empty-proxies", func(email string) *common.User {
			return &common.User{Email: email, Proxies: &common.Proxy{}, Inbounds: []string{}}
		}},
		{"email-only", func(email string) *common.User {
			return &common.User{Email: email}
		}},
	}
	var shapes []removalShape
	for _, payload := range payloads {
		shapes = append(shapes,
			removalShape{"SyncUser-" + payload.name, func(ctx context.Context, b *Backend, email string) error {
				return b.SyncUser(ctx, payload.user(email))
			}},
			removalShape{"UpdateUsers-" + payload.name, func(ctx context.Context, b *Backend, email string) error {
				return b.UpdateUsers(ctx, []*common.User{payload.user(email)})
			}},
		)
	}
	return shapes
}

func startRemovalBackend(t *testing.T) (*Backend, int) {
	t.Helper()
	ports := unusedListenPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":%q,"port":%d,"mode":"plain"}]}`, removalTag, ports[0])
	backend := startBackend(t, raw, []*common.User{
		credentialedUser(removalAlice, initialSecretA, removalTag),
		credentialedUser(removalBob, removalSecret, removalTag),
	})
	t.Cleanup(backend.Shutdown)
	return backend, ports[0]
}

func TestAPanelMainRemovalWithoutAnMtprotoSecretRevokesTheUser(t *testing.T) {
	for _, shape := range panelMainMtprotoRemovalShapes() {
		t.Run(shape.name, func(t *testing.T) {
			backend, port := startRemovalBackend(t)
			if outcome := plainHandshakeOutcome(t, backend, port, initialSecretA); outcome != handshakeAccepted {
				t.Fatalf("precondition: alice must connect before the removal, got %s", outcome)
			}

			if err := shape.apply(context.Background(), backend, removalAlice); err != nil {
				t.Fatalf("panel removal payload: %v", err)
			}

			if _, kept := authorisedEntries(backend)[removalAlice]; kept {
				t.Errorf("the removal left alice's MTProto secret on the node")
			}
			if outcome := plainHandshakeOutcome(t, backend, port, initialSecretA); outcome != handshakeRefused {
				t.Errorf("a user the panel removed must no longer connect, got %s", outcome)
			}
			if outcome := plainHandshakeOutcome(t, backend, port, removalSecret); outcome != handshakeAccepted {
				t.Errorf("bob was not removed and must still connect, got %s", outcome)
			}
		})
	}
}

func TestAPanelMainRemovalRevokesEveryUsernameTheEmailOwns(t *testing.T) {
	b := scopedBackend(removalTag)
	b.applyInitialUsers([]*common.User{
		{Email: removalAlice, Inbounds: []string{removalTag}, Proxies: &common.Proxy{Mtproto: &common.MtprotoUser{Username: "alice-old", Secret: initialSecretA}}},
		{Email: removalAlice, Inbounds: []string{removalTag}, Proxies: &common.Proxy{Mtproto: &common.MtprotoUser{Username: "alice-new", Secret: initialSecretC}}},
		credentialedUser(removalBob, removalSecret, removalTag),
	})
	if err := b.UpdateUsers(context.Background(), []*common.User{{Email: removalAlice}}); err != nil {
		t.Fatalf("removal: %v", err)
	}
	entries := authorisedEntries(b)
	for _, name := range []string{"alice-old", "alice-new"} {
		if _, kept := entries[name]; kept {
			t.Errorf("the removal left %s", name)
		}
	}
	if _, kept := entries[removalBob]; !kept {
		t.Errorf("the removal of alice dropped bob")
	}
}

func TestAnMtprotoUpdateWithoutAnyIdentityRevokesNobody(t *testing.T) {
	b := scopedBackend(removalTag)
	b.applyInitialUsers([]*common.User{
		{Inbounds: []string{removalTag}, Proxies: &common.Proxy{Mtproto: &common.MtprotoUser{Username: "first", Secret: initialSecretA}}},
		{Inbounds: []string{removalTag}, Proxies: &common.Proxy{Mtproto: &common.MtprotoUser{Username: "second", Secret: initialSecretB}}},
	})
	if err := b.UpdateUsers(context.Background(), []*common.User{{Proxies: &common.Proxy{}}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := b.SyncUser(context.Background(), &common.User{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := len(authorisedEntries(b)); got != 2 {
		t.Fatalf("a payload without an email or a username must revoke nobody, %d of 2 left", got)
	}
}

func TestPanelMainMtprotoRemovalsRacingReaddsAndFullSyncsLeaveNoStaleUserAndLoseNoRemoval(t *testing.T) {
	ctx := context.Background()
	shapes := panelMainMtprotoRemovalShapes()
	backend, port := startRemovalBackend(t)
	alice := credentialedUser(removalAlice, initialSecretA, removalTag)
	bob := credentialedUser(removalBob, removalSecret, removalTag)

	for round := range 6 {
		stop := make(chan struct{})
		var storm sync.WaitGroup
		running := func() bool {
			select {
			case <-stop:
				return false
			default:
				return true
			}
		}
		for range 3 {
			storm.Go(func() {
				for running() {
					switch rand.IntN(5) {
					case 0, 1:
						_ = shapes[rand.IntN(len(shapes))].apply(ctx, backend, removalAlice)
					case 2:
						_ = backend.UpdateUsers(ctx, []*common.User{alice})
					case 3:
						_ = backend.SyncUser(ctx, alice)
					case 4:
						users := []*common.User{bob}
						if rand.IntN(2) == 0 {
							users = append(users, alice)
						}
						_ = backend.SyncUsers(ctx, users)
					}
					time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond)
				}
			})
		}
		storm.Go(func() {
			for running() {
				_ = backend.secretsForInstance("example.com")
				_, _ = backend.GetUserOnlineStats(ctx, removalAlice)
			}
		})
		time.Sleep(400 * time.Millisecond)
		close(stop)
		storm.Wait()

		if err := backend.UpdateUsers(ctx, []*common.User{alice}); err != nil {
			t.Fatalf("round %d: re-add: %v", round, err)
		}
		if _, ok := authorisedEntries(backend)[removalAlice]; !ok {
			t.Fatalf("round %d: a re-add that returned must authorise alice", round)
		}

		var final sync.WaitGroup
		final.Go(func() { _ = shapes[round%len(shapes)].apply(ctx, backend, removalAlice) })
		final.Go(func() { _ = backend.UpdateUsers(ctx, []*common.User{bob}) })
		final.Wait()

		entries := authorisedEntries(backend)
		if _, kept := entries[removalAlice]; kept {
			t.Fatalf("round %d (%s): alice's secret survived a removal that returned after every re-add", round, shapes[round%len(shapes)].name)
		}
		if _, kept := entries[removalBob]; !kept {
			t.Fatalf("round %d: bob was never removed, yet he lost his secret", round)
		}
	}

	if outcome := plainHandshakeOutcome(t, backend, port, initialSecretA); outcome != handshakeRefused {
		t.Fatalf("removed alice must not connect after the races, got %s", outcome)
	}
	if outcome := plainHandshakeOutcome(t, backend, port, removalSecret); outcome != handshakeAccepted {
		t.Fatalf("bob must still connect after the races, got %s", outcome)
	}
}
