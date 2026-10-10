//go:build linux

package singbox

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNoHotReloadCallReachesTheCoreAfterShutdownReturns(t *testing.T) {
	alice := restartModelUser("1.alice")
	bob := restartModelUser("2.bob")
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), alice)

	var mu sync.Mutex
	var arrivals []time.Time
	entered := make(chan struct{}, len(restartModelTags))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		entered <- struct{}{}
		time.Sleep(time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	cfg := m.sb.activeConfig()
	cfg.mu.Lock()
	cfg.clashPort = server.Listener.Addr().(*net.TCPAddr).Port
	cfg.mu.Unlock()

	batch := m.sb.enqueue(func(c *Config) { c.upsertUser(bob) })
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the batch never reached clash_api")
	}

	m.sb.Shutdown()
	returned := time.Now()

	select {
	case <-batch.done:
	case <-time.After(60 * time.Second):
		t.Fatal("the batch never finished")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(arrivals) < 2 {
		t.Fatalf("the hot reload made %d call(s); the test needs one per inbound to observe the window", len(arrivals))
	}
	for i, at := range arrivals {
		if at.After(returned) {
			t.Fatalf("hot reload call %d of %d reached clash_api %s after Shutdown returned", i+1, len(arrivals), at.Sub(returned).Round(time.Millisecond))
		}
	}
}

func TestARestartWaitingOnTheWrapperLockIsRefusedWhenShutdownTookItFirst(t *testing.T) {
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), restartModelUser("1.alice"))

	m.sb.mu.RLock()
	held := true
	release := func() {
		if held {
			held = false
			m.sb.mu.RUnlock()
		}
	}
	t.Cleanup(release)

	restarted := make(chan error, 1)
	go func() { restarted <- m.sb.Restart() }()

	if !waitForGoroutineIn(15*time.Second, ".(*SingBox).restartCoreWithConfig", "sync.(*RWMutex).Lock") {
		release()
		<-restarted
		t.Fatal("the restart did not wait for the wrapper lock before deciding")
	}

	m.sb.shutDown = true
	m.sb.cancelFunc()
	m.sb.core.Stop()
	release()

	select {
	case err := <-restarted:
		if !errors.Is(err, errShutDown) {
			t.Fatalf("a restart that waited while the backend shut down returned %v, want errShutDown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the restart never returned")
	}
	if m.sb.core.Started() {
		t.Fatal("a restart that waited while the backend shut down started the core again")
	}
}
