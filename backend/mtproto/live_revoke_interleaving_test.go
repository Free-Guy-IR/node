package mtproto

import (
	"context"
	"encoding/hex"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
)

func dialRelaySession(port int, secret string) (net.Conn, error) {
	key, err := hex.DecodeString(secret)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return nil, err
	}
	session, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(conn.(*net.TCPConn), liveRevokeDC)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return session, nil
}

func removeA(b *Backend) {
	b.SyncUser(context.Background(), credentialedUser("a@x", initialSecretA))
}

func readdA(b *Backend) {
	b.UpdateUsers(context.Background(), []*common.User{credentialedUser("a@x", initialSecretA, liveRevokeTag)})
}

func TestRevokeInterleavesWithLiveMtprotoTraffic(t *testing.T) {
	t.Run("handshakes racing a removal never outlive it", func(t *testing.T) {
		backend, port := startLiveRevokeBackend(t)
		b := openRelaySession(t, port, initialSecretB)
		assertRelays(t, "b@x", b)

		const racers = 24
		sessions := make(chan net.Conn, racers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				session, err := dialRelaySession(port, initialSecretA)
				if err != nil {
					return
				}
				session.SetDeadline(time.Now().Add(time.Second))
				session.Write([]byte("racing-handshake-probe"))
				session.SetDeadline(time.Time{})
				sessions <- session
			}()
		}
		close(start)
		time.Sleep(time.Millisecond)
		removeA(backend)
		wg.Wait()
		close(sessions)

		for session := range sessions {
			t.Cleanup(func() { session.Close() })
			assertSessionCut(t, "a@x racing handshake", session)
		}
		assertRelays(t, "b@x", b)
	})

	t.Run("a session opened right after a re-add survives", func(t *testing.T) {
		backend, port := startLiveRevokeBackend(t)
		old := openRelaySession(t, port, initialSecretA)
		assertRelays(t, "a@x", old)

		removeA(backend)
		readdA(backend)

		fresh := openRelaySession(t, port, initialSecretA)
		assertRelays(t, "a@x after re-add", fresh)
		assertSessionCut(t, "a@x before re-add", old)
		time.Sleep(300 * time.Millisecond)
		assertRelays(t, "a@x after re-add", fresh)
	})

	t.Run("remove and re-add rounds while another user relays continuously", func(t *testing.T) {
		backend, port := startLiveRevokeBackend(t)
		b := openRelaySession(t, port, initialSecretB)
		assertRelays(t, "b@x", b)

		var stop atomic.Bool
		relayErr := make(chan error, 1)
		go func() {
			for !stop.Load() {
				if err := relayRoundTrip(b, []byte("continuous-b-traffic")); err != nil {
					relayErr <- err
					return
				}
			}
			relayErr <- nil
		}()

		for round := range 5 {
			a := openRelaySession(t, port, initialSecretA)
			assertRelays(t, "a@x round "+strconv.Itoa(round), a)
			removeA(backend)
			assertSessionCut(t, "a@x round "+strconv.Itoa(round), a)
			readdA(backend)
		}

		stop.Store(true)
		if err := <-relayErr; err != nil {
			t.Fatalf("b@x's session broke while a@x was removed and re-added: %v", err)
		}
		assertRelays(t, "b@x", b)
	})
}
