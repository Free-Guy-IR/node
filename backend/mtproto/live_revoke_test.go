package mtproto

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/essentials"
	"github.com/9seconds/mtg/v2/mtglib"
	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
)

const (
	liveRevokeDC       = 2
	liveRevokeDeadline = 2 * time.Second
	liveRevokeTag      = "mtproto-tg-plain"
)

type redirectNetwork struct {
	mtglib.Network
	target string
}

func (n redirectNetwork) Dial(network, address string) (essentials.Conn, error) {
	return n.DialContext(context.Background(), network, address)
}

func (n redirectNetwork) DialContext(ctx context.Context, _, _ string) (essentials.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", n.target)
	if err != nil {
		return nil, err
	}
	return conn.(*net.TCPConn), nil
}

func startEchoDC(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake DC listen: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	conns := map[net.Conn]struct{}{}
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for conn := range conns {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[conn] = struct{}{}
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				header := make([]byte, 64)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func redirectTelegramTo(t *testing.T, target string) {
	t.Helper()
	original := buildNetwork
	buildNetwork = func() (mtglib.Network, error) {
		ntw, err := original()
		if err != nil {
			return nil, err
		}
		return redirectNetwork{Network: ntw, target: target}, nil
	}
	t.Cleanup(func() { buildNetwork = original })
}

func openRelaySession(t *testing.T, port int, secret string) essentials.Conn {
	t.Helper()
	key, err := hex.DecodeString(secret)
	if err != nil {
		t.Fatalf("bad test secret %q: %v", secret, err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	session, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(conn.(*net.TCPConn), liveRevokeDC)
	if err != nil {
		conn.Close()
		t.Fatalf("client handshake: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func relayRoundTrip(session net.Conn, payload []byte) error {
	if err := session.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer session.SetDeadline(time.Time{})
	if _, err := session.Write(payload); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(session, reply); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	return nil
}

func assertRelays(t *testing.T, who string, session net.Conn) {
	t.Helper()
	if err := relayRoundTrip(session, []byte("relay-probe-0123456789abcdef")); err != nil {
		t.Fatalf("%s's open session must relay through to the DC: %v", who, err)
	}
}

func sessionClosedWithin(session net.Conn, within time.Duration) error {
	deadline := time.Now().Add(within)
	if err := session.SetReadDeadline(deadline); err != nil {
		return nil
	}
	buf := make([]byte, 256)
	for {
		session.SetWriteDeadline(deadline)
		if _, err := session.Write([]byte("still-here?")); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			return nil
		}
		_, err := session.Read(buf)
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("session still open %s after the revoke", within)
		}
		return nil
	}
}

func assertSessionCut(t *testing.T, who string, session net.Conn) {
	t.Helper()
	if err := sessionClosedWithin(session, liveRevokeDeadline); err != nil {
		t.Fatalf("%s was revoked but the already-open MTProto session kept relaying: %v", who, err)
	}
}

func startLiveRevokeBackend(t *testing.T) (*Backend, int) {
	t.Helper()
	redirectTelegramTo(t, startEchoDC(t))
	ports := unusedListenPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":%q,"port":%d,"mode":"plain"}]}`, liveRevokeTag, ports[0])
	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, liveRevokeTag),
		credentialedUser("b@x", initialSecretB, liveRevokeTag),
	})
	t.Cleanup(backend.Shutdown)
	awaitServing(t, backend, liveRevokeTag, ports[0])
	return backend, ports[0]
}

func TestRevokingAnMtprotoUserCutsTheirOpenPlainSession(t *testing.T) {
	removals := map[string]func(*Backend) error{
		"SyncUser without the instance tag": func(b *Backend) error {
			return b.SyncUser(context.Background(), credentialedUser("a@x", initialSecretA))
		},
		"UpdateUsers without the instance tag": func(b *Backend) error {
			return b.UpdateUsers(context.Background(), []*common.User{credentialedUser("a@x", initialSecretA)})
		},
		"credential-less removal payload": func(b *Backend) error {
			return b.UpdateUsers(context.Background(), []*common.User{{Email: "a@x"}})
		},
		"re-key to a new secret": func(b *Backend) error {
			return b.UpdateUsers(context.Background(), []*common.User{credentialedUser("a@x", initialSecretC, liveRevokeTag)})
		},
		"full sync without the user": func(b *Backend) error {
			return b.SyncUsers(context.Background(), []*common.User{credentialedUser("b@x", initialSecretB, liveRevokeTag)})
		},
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			backend, port := startLiveRevokeBackend(t)

			a := openRelaySession(t, port, initialSecretA)
			b := openRelaySession(t, port, initialSecretB)
			assertRelays(t, "a@x", a)
			assertRelays(t, "b@x", b)

			if err := remove(backend); err != nil {
				t.Fatalf("removal: %v", err)
			}

			assertSessionCut(t, "a@x", a)
			assertRelays(t, "b@x", b)
		})
	}
}
