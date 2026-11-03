package mtproto

import (
	"fmt"
	"testing"

	"github.com/pasarguard/node/common"
)

func TestUserOnlyOnSecondInstanceIsRefusedOnTheFirst(t *testing.T) {
	ports := unusedListenPorts(t, 2)
	raw := fmt.Sprintf(`{"instances":[`+
		`{"tag":"mtproto-a","port":%d,"mode":"plain"},`+
		`{"tag":"mtproto-b","port":%d,"mode":"plain"}]}`, ports[0], ports[1])

	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, "mtproto-a"),
		credentialedUser("c@x", initialSecretC, "mtproto-b"),
	})
	defer backend.Shutdown()
	defer awaitServing(t, backend, "mtproto-b", ports[1])
	defer awaitServing(t, backend, "mtproto-a", ports[0])

	if outcome := plainHandshakeOutcome(t, backend, ports[1], initialSecretC); outcome != handshakeAccepted {
		t.Fatalf("control: c@x must pass mtproto-b, got %s", outcome)
	}
	if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretA); outcome != handshakeAccepted {
		t.Fatalf("control: a@x must pass mtproto-a, got %s", outcome)
	}
	if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretC); outcome == handshakeAccepted {
		t.Fatalf("c@x holds only mtproto-b but its secret passed the handshake on mtproto-a")
	}
}
