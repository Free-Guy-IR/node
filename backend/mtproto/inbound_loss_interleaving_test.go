package mtproto

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func TestScopedRevokeRefusesAHandshakeBegunBeforeIt(t *testing.T) {
	registry := &liveSessions{}
	var cutA, cutB, cutOther atomic.Int32

	registry.Begin("on-a")
	registry.Begin("on-b")
	registry.Begin("other-on-a")
	registry.revokeScoped(nil, []sessionScope{{tag: "mtproto-a", username: "c@x"}})
	registry.authenticatedOn("mtproto-a", "on-a", "c@x", func() { cutA.Add(1) })
	registry.authenticatedOn("mtproto-b", "on-b", "c@x", func() { cutB.Add(1) })
	registry.authenticatedOn("mtproto-a", "other-on-a", "a@x", func() { cutOther.Add(1) })

	if cutA.Load() != 1 {
		t.Fatalf("a handshake on mtproto-a begun before c@x lost mtproto-a must be refused, cut %d times", cutA.Load())
	}
	if cutB.Load() != 0 || cutOther.Load() != 0 {
		t.Fatalf("the scoped revoke must not touch c@x on mtproto-b (%d) or a@x on mtproto-a (%d)", cutB.Load(), cutOther.Load())
	}
	if _, live := registry.byUser["c@x"]["on-a"]; live {
		t.Fatal("the refused handshake was registered as a live session")
	}

	registry.Begin("again-on-a")
	registry.authenticatedOn("mtproto-a", "again-on-a", "c@x", func() { cutA.Add(1) })
	if cutA.Load() != 1 {
		t.Fatal("a handshake begun after the revoke, once the inbound is back, must be allowed")
	}
}

func liveScopedSessions(tag, username string) int {
	liveSessionRegistry.mu.Lock()
	defer liveSessionRegistry.mu.Unlock()
	n := 0
	for _, session := range liveSessionRegistry.byUser[username] {
		if session.tag == tag {
			n++
		}
	}
	return n
}

func TestInboundLossRacingHandshakesLeavesNoSessionOnTheLostInstance(t *testing.T) {
	for round := 0; round < 5; round++ {
		backend, ports := startTwoInstanceRevokeBackend(t)
		keep := openRelaySession(t, ports[1], initialSecretC)
		assertRelays(t, "c@x on mtproto-b", keep)

		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			sessions []net.Conn
		)
		start := make(chan struct{})
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				time.Sleep(time.Duration(i%4) * time.Millisecond)
				session, err := dialRelaySession(ports[0], initialSecretC)
				if err != nil {
					return
				}
				_ = relayRoundTrip(session, []byte("in-flight-probe"))
				mu.Lock()
				sessions = append(sessions, session)
				mu.Unlock()
			}(i)
		}
		close(start)
		time.Sleep(time.Duration(round) * time.Millisecond)
		if err := backend.UpdateUsers(context.Background(), []*common.User{credentialedUser("c@x", initialSecretC, "mtproto-b")}); err != nil {
			t.Fatalf("remove mtproto-a from c@x: %v", err)
		}
		wg.Wait()

		for i, session := range sessions {
			if err := sessionClosedWithin(session, liveRevokeDeadline); err != nil {
				t.Fatalf("round %d: c@x session %d on the lost mtproto-a survived the removal: %v", round, i, err)
			}
			session.Close()
		}
		if n := liveScopedSessions("mtproto-a", "c@x"); n != 0 {
			t.Fatalf("round %d: %d c@x sessions are still registered on the lost mtproto-a", round, n)
		}
		assertRelays(t, "c@x on the kept mtproto-b", keep)
		if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretC); outcome == handshakeAccepted {
			t.Fatalf("round %d: a fresh c@x handshake on the lost mtproto-a was accepted", round)
		}
	}
}
