package mtproto

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const (
	deadContractKeptSecret    = "1a2b3c4d5e6f708192a3b4c5d6e7f801"
	deadContractRemovedSecret = "2b3c4d5e6f708192a3b4c5d6e7f80112"
	deadContractChurnSecret   = "3c4d5e6f708192a3b4c5d6e7f8011223"
)

func instanceByTag(b *Backend, tag string) *proxyInstance {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.instances[tag]
}

func awaitInstanceDead(b *Backend, tag string, want bool, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		dead := b.instances[tag].listenerDead()
		b.mu.RUnlock()
		if dead == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func holdInstanceDead(t *testing.T, b *Backend, tag string, port int) func() {
	t.Helper()
	var blockerMu sync.Mutex
	var blocker net.Listener
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

	var held net.Listener
	for deadline := time.Now().Add(3 * time.Second); held == nil && time.Now().Before(deadline); {
		blockerMu.Lock()
		held = blocker
		blockerMu.Unlock()
		if held == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if held == nil {
		t.Fatal("the test could not occupy the port after the fatal error")
	}
	if !awaitInstanceDead(b, tag, true, 3*time.Second) {
		held.Close()
		t.Fatalf("instance %q never went dead while its port was taken", tag)
	}
	var once sync.Once
	release := func() { once.Do(func() { held.Close() }) }
	t.Cleanup(release)
	return release
}

func freshHandshakeOutcome(t *testing.T, b *Backend, port int, secret string) string {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	drainedLogs(b)
	return plainHandshakeOutcome(t, b, port, secret)
}

func TestOneDeadInstanceDoesNotStopTheBackend(t *testing.T) {
	ports := unusedListenPorts(t, 2)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-dead","port":%d,"mode":"plain"},{"tag":"mtproto-alive","port":%d,"mode":"plain"}]}`, ports[0], ports[1])
	b := startBackend(t, raw, nil)
	t.Cleanup(b.Shutdown)

	release := holdInstanceDead(t, b, "mtproto-dead", ports[0])

	if !b.Started() {
		release()
		t.Fatal("one dead MTProto instance made the whole backend report Started()=false, so the node refuses every guarded call")
	}
	if !awaitServed(ports[1], 5*time.Second) {
		release()
		t.Fatal("the healthy instance stopped serving while the other instance was dead")
	}
	release()
	if !awaitInstanceDead(b, "mtproto-dead", false, 5*time.Second) {
		t.Fatal("the dead instance did not recover once its port was free again")
	}
	if !awaitServed(ports[0], 5*time.Second) {
		t.Fatal("the recovered instance does not serve")
	}
}

func TestRemovalDuringADeadWindowHoldsAfterRecovery(t *testing.T) {
	const tag = "mtproto-tg-plain"
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":%q,"port":%d,"mode":"plain"}]}`, tag, port)
	kept := credentialedUser("kept@x", deadContractKeptSecret, tag)
	removed := credentialedUser("removed@x", deadContractRemovedSecret, tag)
	b := startBackend(t, raw, []*common.User{kept, removed})
	t.Cleanup(b.Shutdown)

	release := holdInstanceDead(t, b, tag, port)
	if !b.Started() {
		release()
		t.Fatal("a single dead MTProto instance made the backend report Started()=false, so the panel's removal would be refused")
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(3)
	go func() {
		defer wg.Done()
		errs <- b.SyncUser(ctx, &common.User{Email: removed.Email})
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			churn := credentialedUser("churn@x", deadContractChurnSecret, tag)
			if i%2 == 1 {
				churn = &common.User{Email: "churn@x"}
			}
			errs <- b.UpdateUsers(ctx, []*common.User{churn})
		}
		errs <- b.UpdateUsers(ctx, []*common.User{credentialedUser("churn@x", deadContractChurnSecret, tag)})
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = b.Started()
			servedOnce(port, 10*time.Millisecond)
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			release()
			t.Fatalf("a user sync during the dead window failed: %v", err)
		}
	}

	if !b.Started() {
		release()
		t.Fatal("the backend reported Started()=false during the dead window")
	}
	release()
	if !awaitInstanceDead(b, tag, false, 5*time.Second) {
		t.Fatal("the instance did not recover once its port was free again")
	}

	if _, ok := authorisedEntries(b)[removed.Email]; ok {
		t.Fatal("the removal made during the dead window was lost")
	}
	if got := freshHandshakeOutcome(t, b, port, deadContractRemovedSecret); got != handshakeRefused {
		t.Fatalf("the user removed during the dead window still completes a new handshake after recovery: %s", got)
	}
	if got := freshHandshakeOutcome(t, b, port, deadContractKeptSecret); got != handshakeAccepted {
		t.Fatalf("a still-present user is refused after recovery: %s", got)
	}
	if got := freshHandshakeOutcome(t, b, port, deadContractChurnSecret); got != handshakeAccepted {
		t.Fatalf("the user re-added during the dead window is refused after recovery: %s", got)
	}
}
