package mtproto

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
)

const shutdownRaceRounds = 50

func faketlsConfigOnFreshPort(t *testing.T) string {
	t.Helper()
	port := unusedListenPorts(t, 1)[0]
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
	ports := unusedListenPorts(t, 1)
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

	ports := unusedListenPorts(t, 1)
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

func TestConcurrentShutdownCallsHandOverAccountingOnce(t *testing.T) {
	for range shutdownRaceRounds {
		ports := unusedListenPorts(t, 2)
		raw := fmt.Sprintf(`{"instances":[`+
			`{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"},`+
			`{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, ports[0], ports[1], testAdTag)
		backend := startBackend(t, raw, nil)
		takeHandOver()
		logs := backend.Logs()

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(backend.Shutdown)
		}
		wg.Wait()

		if takeHandOver() == nil {
			t.Fatal("eight concurrent Shutdown calls deposited no accounting hand-over")
		}
		for drained := false; !drained; {
			select {
			case line := <-logs:
				if strings.Contains(line, "hand-over that no backend took was replaced") {
					t.Fatalf("a concurrent Shutdown deposited a second hand-over: %s", line)
				}
			default:
				drained = true
			}
		}
	}
}

func awaitSyncProgress(t *testing.T, done *atomic.Int64, target int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for done.Load() < target {
		if time.Now().After(deadline) {
			t.Fatalf("user syncs stalled at %d of %d", done.Load(), target)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUserSyncsDuringAndAfterShutdownAreSafe(t *testing.T) {
	ports := unusedListenPorts(t, 3)
	raw := fmt.Sprintf(`{"instances":[`+
		`{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"},`+
		`{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"},`+
		`{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, ports[0], ports[1], ports[2], testAdTag)

	onInstances := []*common.User{
		credentialedUser("a@x", initialSecretA, "mtproto-tg"),
		credentialedUser("c@x", initialSecretC, "mtproto-tg-plain", "mtproto-tg-ad"),
		credentialedUser("b@x", initialSecretB, "hy2-in"),
	}
	offInstances := []*common.User{
		credentialedUser("a@x", initialSecretA, "hy2-in"),
		credentialedUser("c@x", initialSecretC, "hy2-in"),
		credentialedUser("b@x", initialSecretB, "hy2-in"),
	}

	backend := startBackend(t, raw, onInstances)

	ctx := context.Background()
	stop := make(chan struct{})
	stopSyncs := sync.OnceFunc(func() { close(stop) })
	defer stopSyncs()
	var done atomic.Int64
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for n := worker; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				users := onInstances
				if n%2 == 1 {
					users = offInstances
				}
				var err error
				switch n % 3 {
				case 0:
					err = backend.SyncUser(ctx, users[n%len(users)])
				case 1:
					err = backend.UpdateUsers(ctx, users)
				case 2:
					err = backend.SyncUsers(ctx, users)
				}
				if err != nil {
					t.Errorf("user sync: %v", err)
				}
				done.Add(1)
			}
		})
	}

	awaitSyncProgress(t, &done, 200)

	stopped := make(chan struct{})
	go func() {
		backend.Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return while users were being synced")
	}

	awaitSyncProgress(t, &done, done.Load()+200)
	stopSyncs()

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("user syncs did not finish after Shutdown")
	}

	if err := backend.SyncUsers(ctx, onInstances); err != nil {
		t.Fatalf("SyncUsers after Shutdown: %v", err)
	}
	assertAuthorised(t, backend, "a@x", initialSecretA)
	assertAuthorised(t, backend, "c@x", initialSecretC)
	assertRefused(t, backend, "b@x")
}
