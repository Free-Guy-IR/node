package mtproto

import (
	"context"
	"fmt"
	"testing"

	"github.com/pasarguard/node/common"
)

func startTwoInstanceRevokeBackend(t *testing.T) (*Backend, []int) {
	t.Helper()
	redirectTelegramTo(t, startEchoDC(t))
	ports := unusedListenPorts(t, 2)
	raw := fmt.Sprintf(`{"instances":[`+
		`{"tag":"mtproto-a","port":%d,"mode":"plain"},`+
		`{"tag":"mtproto-b","port":%d,"mode":"plain"}]}`, ports[0], ports[1])
	backend := startBackend(t, raw, []*common.User{
		credentialedUser("c@x", initialSecretC, "mtproto-a", "mtproto-b"),
		credentialedUser("a@x", initialSecretA, "mtproto-a"),
	})
	t.Cleanup(backend.Shutdown)
	awaitServing(t, backend, "mtproto-a", ports[0])
	awaitServing(t, backend, "mtproto-b", ports[1])
	return backend, ports
}

func TestLosingOneInboundCutsOnlyThatInstancesSessions(t *testing.T) {
	removals := map[string]func(*Backend) error{
		"SyncUser": func(b *Backend) error {
			return b.SyncUser(context.Background(), credentialedUser("c@x", initialSecretC, "mtproto-b"))
		},
		"UpdateUsers": func(b *Backend) error {
			return b.UpdateUsers(context.Background(), []*common.User{credentialedUser("c@x", initialSecretC, "mtproto-b")})
		},
		"SyncUsers": func(b *Backend) error {
			return b.SyncUsers(context.Background(), []*common.User{
				credentialedUser("c@x", initialSecretC, "mtproto-b"),
				credentialedUser("a@x", initialSecretA, "mtproto-a"),
			})
		},
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			backend, ports := startTwoInstanceRevokeBackend(t)

			onA := openRelaySession(t, ports[0], initialSecretC)
			onB := openRelaySession(t, ports[1], initialSecretC)
			other := openRelaySession(t, ports[0], initialSecretA)
			assertRelays(t, "c@x on mtproto-a", onA)
			assertRelays(t, "c@x on mtproto-b", onB)
			assertRelays(t, "a@x on mtproto-a", other)

			if err := remove(backend); err != nil {
				t.Fatalf("remove mtproto-a from c@x: %v", err)
			}

			assertSessionCut(t, "c@x on the lost mtproto-a", onA)
			assertRelays(t, "c@x on the kept mtproto-b", onB)
			assertRelays(t, "a@x on mtproto-a", other)
			if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretC); outcome == handshakeAccepted {
				t.Fatalf("c@x lost mtproto-a but a fresh handshake there was accepted")
			}
		})
	}
}
