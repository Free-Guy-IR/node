package mtproto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
)

type faultyListener struct {
	net.Listener
	accepts   atomic.Int64
	transient atomic.Int64
	permanent atomic.Bool
	fatal     atomic.Bool
	onFatal   func()
}

func (l *faultyListener) takeTransient() bool {
	for {
		n := l.transient.Load()
		if n <= 0 {
			return false
		}
		if l.transient.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

func (l *faultyListener) transientError() error {
	return &net.OpError{Op: "accept", Net: "tcp", Addr: l.Addr(), Err: os.NewSyscallError("accept", syscall.EMFILE)}
}

func (l *faultyListener) Accept() (net.Conn, error) {
	l.accepts.Add(1)
	if l.permanent.Load() || l.takeTransient() {
		return nil, l.transientError()
	}
	if l.fatal.CompareAndSwap(true, false) {
		_ = l.Listener.Close()
		if l.onFatal != nil {
			l.onFatal()
		}
		_, err := l.Listener.Accept()
		if err == nil {
			err = errors.New("example-fatal accept error")
		}
		return nil, err
	}
	return l.Listener.Accept()
}

func instancePort(inst *proxyInstance) int {
	return inst.listener.Addr().(*net.TCPAddr).Port
}

func swapInFaultyListener(b *Backend, tag string, arm func(*faultyListener)) (*faultyListener, error) {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()

	b.mu.RLock()
	shut := b.cancelFunc == nil
	inst := b.instances[tag]
	b.mu.RUnlock()
	if shut {
		return nil, errBackendShutDown
	}

	inst.listener.Close()
	if !inst.awaitServeExit() {
		return nil, fmt.Errorf("instance %q: serve did not exit before the swap", tag)
	}
	raw, err := net.ListenTCP("tcp", &net.TCPAddr{Port: instancePort(inst)})
	if err != nil {
		return nil, err
	}
	faulty := &faultyListener{Listener: raw}
	if arm != nil {
		arm(faulty)
	}
	b.mu.Lock()
	inst.serve(faulty)
	b.mu.Unlock()
	return faulty, nil
}

func servedOnce(port int, wait time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(0xa0 + i)
	}
	if _, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(conn.(*net.TCPConn), unknownDC); err != nil {
		return true
	}
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	_, err = io.Copy(io.Discard, conn)
	var netErr net.Error
	return err == nil || !(errors.As(err, &netErr) && netErr.Timeout())
}

func awaitServed(port int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if servedOnce(port, time.Second) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func awaitStarted(b *Backend, want bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if b.Started() == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return b.Started() == want
}

func plainInstanceBackend(t *testing.T) (*Backend, int) {
	t.Helper()
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"}]}`, port)
	backend := startBackend(t, raw, []*common.User{credentialedUser("a@x", initialSecretA, "mtproto-tg-plain")})
	t.Cleanup(backend.Shutdown)
	return backend, port
}

func adTagInstanceBackend(t *testing.T) (*Backend, int) {
	t.Helper()
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, port, testAdTag)
	backend := startBackend(t, raw, nil)
	t.Cleanup(backend.Shutdown)
	return backend, port
}

func failOnPanicLines(t *testing.T, b *Backend) {
	t.Helper()
	logs := b.Logs()
	for {
		select {
		case line := <-logs:
			if strings.Contains(strings.ToLower(line), "panic") {
				t.Fatalf("the serve path panicked: %s", line)
			}
		default:
			return
		}
	}
}

func assertServesAfterTransientError(t *testing.T, b *Backend, tag string, port int) {
	t.Helper()
	faulty, err := swapInFaultyListener(b, tag, nil)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !awaitServed(port, 5*time.Second) {
		t.Fatalf("instance %q does not serve before any fault", tag)
	}
	faulty.transient.Store(3)
	servedOnce(port, time.Second)

	if !awaitServed(port, 5*time.Second) {
		t.Fatalf("instance %q stopped serving after a transient accept error (EMFILE): later clients hang in the backlog while Started()=%v", tag, b.Started())
	}
}

func assertNoZombieAfterFatalError(t *testing.T, b *Backend, tag string, port int) {
	t.Helper()
	faulty, err := swapInFaultyListener(b, tag, nil)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !awaitServed(port, 5*time.Second) {
		t.Fatalf("instance %q does not serve before any fault", tag)
	}
	faulty.fatal.Store(true)
	servedOnce(port, time.Second)

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !b.Started() || servedOnce(port, time.Second) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("instance %q is a zombie after a fatal listener error: Started()=%v while nothing serves its port", tag, b.Started())
}

func assertReportsDeadThenRecovers(t *testing.T, b *Backend, tag string, port int) {
	t.Helper()
	var blocker net.Listener
	var blockerMu sync.Mutex
	faulty, err := swapInFaultyListener(b, tag, func(l *faultyListener) {
		l.onFatal = func() {
			held, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
			if err == nil {
				blockerMu.Lock()
				blocker = held
				blockerMu.Unlock()
			}
		}
	})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !awaitServed(port, 5*time.Second) {
		t.Fatalf("instance %q does not serve before any fault", tag)
	}
	faulty.fatal.Store(true)
	servedOnce(port, 200*time.Millisecond)

	blockerMu.Lock()
	held := blocker
	blockerMu.Unlock()
	if held == nil {
		t.Fatal("the test could not occupy the port after the fatal error")
	}

	if !awaitStarted(b, false, 3*time.Second) {
		held.Close()
		t.Fatalf("instance %q lost its listener and cannot re-listen (port taken) but Started() still reports it running", tag)
	}
	held.Close()

	if !awaitStarted(b, true, 5*time.Second) {
		t.Fatalf("instance %q did not recover once its port was free again", tag)
	}
	if !awaitServed(port, 5*time.Second) {
		t.Fatalf("instance %q reports started again but does not serve", tag)
	}
}

func TestMTProtoKeepsServingAfterATransientAcceptError(t *testing.T) {
	b, port := plainInstanceBackend(t)
	assertServesAfterTransientError(t, b, "mtproto-tg-plain", port)
	if outcome := plainHandshakeOutcome(t, b, port, initialSecretA); outcome != handshakeAccepted {
		t.Fatalf("an authorised client must still pass the handshake after the transient error, got %s", outcome)
	}
	failOnPanicLines(t, b)
}

func TestMTProtoIsNoZombieAfterAFatalListenerError(t *testing.T) {
	b, port := plainInstanceBackend(t)
	assertNoZombieAfterFatalError(t, b, "mtproto-tg-plain", port)
	failOnPanicLines(t, b)
}

func TestMTProtoReportsDeadWhileItCannotRelistenAndRecovers(t *testing.T) {
	b, port := plainInstanceBackend(t)
	assertReportsDeadThenRecovers(t, b, "mtproto-tg-plain", port)
	if outcome := plainHandshakeOutcome(t, b, port, initialSecretA); outcome != handshakeAccepted {
		t.Fatalf("an authorised client must pass the handshake after the recovery, got %s", outcome)
	}
	failOnPanicLines(t, b)
}

func TestMiddleProxyKeepsServingAfterATransientAcceptError(t *testing.T) {
	b, port := adTagInstanceBackend(t)
	assertServesAfterTransientError(t, b, "mtproto-tg-ad", port)
	failOnPanicLines(t, b)
}

func TestMiddleProxyIsNoZombieAfterAFatalListenerError(t *testing.T) {
	b, port := adTagInstanceBackend(t)
	assertNoZombieAfterFatalError(t, b, "mtproto-tg-ad", port)
	failOnPanicLines(t, b)
}

func TestMiddleProxyReportsDeadWhileItCannotRelistenAndRecovers(t *testing.T) {
	b, port := adTagInstanceBackend(t)
	assertReportsDeadThenRecovers(t, b, "mtproto-tg-ad", port)
	failOnPanicLines(t, b)
}

func TestAPermanentlyFailingAcceptNeitherSpinsNorKillsTheInstance(t *testing.T) {
	for _, kind := range []string{"plain", "ad_tag"} {
		t.Run(kind, func(t *testing.T) {
			var (
				b    *Backend
				port int
				tag  string
			)
			if kind == "plain" {
				b, port = plainInstanceBackend(t)
				tag = "mtproto-tg-plain"
			} else {
				b, port = adTagInstanceBackend(t)
				tag = "mtproto-tg-ad"
			}
			faulty, err := swapInFaultyListener(b, tag, func(l *faultyListener) { l.permanent.Store(true) })
			if err != nil {
				t.Fatalf("swap: %v", err)
			}
			time.Sleep(time.Second)
			if calls := faulty.accepts.Load(); calls > 40 {
				t.Fatalf("a permanently failing accept was retried %d times in one second: the serve loop spins", calls)
			}
			faulty.permanent.Store(false)
			if !awaitServed(port, 5*time.Second) {
				t.Fatalf("instance %q never served again after its accept errors stopped (accept calls: %d)", tag, faulty.accepts.Load())
			}
			failOnPanicLines(t, b)
		})
	}
}

func TestAcceptFaultsInterleavedWithRestartsSyncsAndDialsLoseNothing(t *testing.T) {
	ports := unusedListenPorts(t, 2)
	raw := fmt.Sprintf(`{"instances":[`+
		`{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"},`+
		`{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, ports[0], ports[1], testAdTag)
	tags := []string{"mtproto-tg-plain", "mtproto-tg-ad"}
	portOf := map[string]int{tags[0]: ports[0], tags[1]: ports[1]}

	warm := startBackend(t, raw, nil)
	warm.Shutdown()
	before := settledGoroutines(0)

	pool := overlapPool(6, tags...)
	b := startBackend(t, raw, pool[:1])
	shutdownOnce := sync.OnceFunc(b.Shutdown)
	defer shutdownOnce()

	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed>>7))

	var faultyMu sync.Mutex
	var faulties []*faultyListener
	keep := func(l *faultyListener) {
		faultyMu.Lock()
		faulties = append(faulties, l)
		faultyMu.Unlock()
	}
	arm := func(l *faultyListener, fatal bool, n int64) {
		if fatal {
			l.fatal.Store(true)
			return
		}
		l.transient.Store(n)
	}

	ctx := context.Background()
	for round := range 12 {
		subset := pool[:1+rng.IntN(len(pool))]
		tag := tags[rng.IntN(len(tags))]
		fatal := rng.IntN(2) == 0
		transient := int64(1 + rng.IntN(3))
		delays := [4]time.Duration{}
		for i := range delays {
			delays[i] = time.Duration(rng.IntN(3000)) * time.Microsecond
		}

		var wg sync.WaitGroup
		errs := make(chan error, 8)
		wg.Add(4)
		go func() {
			defer wg.Done()
			time.Sleep(delays[0])
			if err := b.Restart(); err != nil {
				errs <- fmt.Errorf("restart: %w", err)
			}
		}()
		go func() {
			defer wg.Done()
			time.Sleep(delays[1])
			if err := b.SyncUsers(ctx, subset); err != nil {
				errs <- fmt.Errorf("sync: %w", err)
			}
		}()
		go func() {
			defer wg.Done()
			time.Sleep(delays[2])
			faulty, err := swapInFaultyListener(b, tag, nil)
			if err != nil {
				errs <- fmt.Errorf("swap: %w", err)
				return
			}
			keep(faulty)
			arm(faulty, fatal, transient)
			servedOnce(portOf[tag], 200*time.Millisecond)
		}()
		go func() {
			defer wg.Done()
			time.Sleep(delays[3])
			for _, p := range ports {
				servedOnce(p, 200*time.Millisecond)
			}
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}

		faulty, err := swapInFaultyListener(b, tag, nil)
		if err != nil {
			t.Fatalf("round %d: swap: %v", round, err)
		}
		keep(faulty)
		if !awaitServed(portOf[tag], 5*time.Second) {
			t.Fatalf("round %d: instance %q does not serve before the closing fault", round, tag)
		}
		arm(faulty, fatal, transient)
		servedOnce(portOf[tag], 200*time.Millisecond)

		for _, p := range ports {
			if !awaitServed(p, 6*time.Second) {
				t.Fatalf("round %d: port %d stopped serving after accept faults (fatal=%v) interleaved with restarts and syncs, Started()=%v", round, p, fatal, b.Started())
			}
		}
		if !awaitStarted(b, true, 3*time.Second) {
			t.Fatalf("round %d: the backend serves but reports not started", round)
		}
		failOnPanicLines(t, b)
	}

	shutdownOnce()
	faultyMu.Lock()
	snapshot := make([]int64, len(faulties))
	for i, l := range faulties {
		snapshot[i] = l.accepts.Load()
	}
	faultyMu.Unlock()

	for _, p := range ports {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), 300*time.Millisecond); err == nil {
			conn.Close()
			t.Fatalf("port %d still accepts after Shutdown", p)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	for _, p := range ports {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), 300*time.Millisecond); err == nil {
			conn.Close()
			t.Fatalf("port %d listens again after Shutdown: a serve loop outlived the stop", p)
		}
	}
	faultyMu.Lock()
	for i, l := range faulties {
		if got := l.accepts.Load(); got != snapshot[i] {
			faultyMu.Unlock()
			t.Fatalf("a listener was accepted on after Shutdown: %d calls became %d", snapshot[i], got)
		}
	}
	faultyMu.Unlock()

	assertNoGoroutineGrowth(t, before, "accept faults interleaved with restarts, syncs and dials")
}
