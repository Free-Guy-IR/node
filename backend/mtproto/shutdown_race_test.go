package mtproto

import (
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
)

const shutdownRaceRounds = 50

func faketlsConfigOnFreshPort(t *testing.T) string {
	t.Helper()
	port := unusedLoopbackPorts(t, 1)[0]
	return fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"}]}`, port)
}

func TestShutdownRightAfterNewDoesNotRaceServe(t *testing.T) {
	for range shutdownRaceRounds {
		backend := startBackend(t, faketlsConfigOnFreshPort(t), nil)
		backend.Shutdown()
	}
}

func TestShutdownAfterRestartDoesNotRaceServe(t *testing.T) {
	for range shutdownRaceRounds {
		backend := startBackend(t, faketlsConfigOnFreshPort(t), nil)
		if err := backend.Restart(); err != nil {
			backend.Shutdown()
			t.Fatalf("Restart: %v", err)
		}
		backend.Shutdown()
	}
}

func TestRestartKeepsServingAndShutdownStopsListening(t *testing.T) {
	ports := unusedLoopbackPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"}]}`, ports[0])

	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, "mtproto-tg-plain"),
	})
	stopped := false
	defer func() {
		if !stopped {
			backend.Shutdown()
		}
	}()

	if err := backend.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !backend.Started() {
		t.Fatal("the backend must report started after a restart")
	}
	if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretA); outcome != handshakeAccepted {
		t.Fatalf("a restarted instance must keep accepting a@x, got %s", outcome)
	}

	backend.Shutdown()
	stopped = true

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[0])), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("the instance port must stop accepting after Shutdown")
	}
}

func authenticatingConn(t *testing.T, port int, secret string, entered <-chan struct{}) net.Conn {
	t.Helper()
	key, err := hex.DecodeString(secret)
	if err != nil {
		t.Fatalf("bad test secret %q: %v", secret, err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if _, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(conn.(*net.TCPConn), unknownDC); err != nil {
			conn.Close()
			t.Fatalf("cannot send the client handshake: %v", err)
		}
		select {
		case <-entered:
			return conn
		case <-time.After(500 * time.Millisecond):
			conn.Close()
		}
	}
	t.Fatal("no stream authenticated within the deadline")
	return nil
}

func TestShutdownDoesNotDeadlockWithAStreamThatJustAuthenticated(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseAuthLookup := sync.OnceFunc(func() { close(release) })
	previous := beforeAuthLookup
	beforeAuthLookup = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	defer func() { beforeAuthLookup = previous }()
	defer releaseAuthLookup()

	ports := unusedLoopbackPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"}]}`, ports[0])
	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, "mtproto-tg-plain"),
	})

	conn := authenticatingConn(t, ports[0], initialSecretA, entered)
	defer conn.Close()

	stopped := make(chan struct{})
	go func() {
		backend.Shutdown()
		close(stopped)
	}()

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("Shutdown never stopped the proxy holding the authenticating stream: %v", err)
	}
	releaseAuthLookup()

	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return within 3s: it deadlocked with a stream that authenticated while the proxy was stopping")
	}
}
