package xray

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func goroutineIn(frames ...string) bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for stack := range strings.SplitSeq(string(buf), "\n\n") {
		matched := true
		for _, frame := range frames {
			if !strings.Contains(stack, frame) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func waitForGoroutineIn(t *testing.T, timeout time.Duration, frames ...string) bool {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if goroutineIn(frames...) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestAHealthRestartThatWaitedForASyncDoesNotReviveTheCoreAfterShutdown(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	healthCtx, stopHealth := context.WithCancel(context.Background())
	h.x.cancelFunc = stopHealth
	h.stats.hold()

	h.x.syncMu.Lock()
	syncHeld := true
	releaseSync := func() {
		if syncHeld {
			syncHeld = false
			h.x.syncMu.Unlock()
		}
	}

	healthStopped := make(chan struct{})
	go func() {
		defer close(healthStopped)
		h.x.checkXrayHealth(healthCtx)
	}()
	t.Cleanup(func() {
		stopHealth()
		releaseSync()
		select {
		case <-healthStopped:
		case <-time.After(30 * time.Second):
			t.Error("the health loop never stopped")
		}
		h.x.core.Stop()
	})

	if !waitForGoroutineIn(t, 15*time.Second, ".(*Xray).checkXrayHealth", ".(*Xray).lockSync") {
		t.Fatal("the health restart never waited for the running sync")
	}

	h.x.Shutdown()
	releaseSync()

	select {
	case <-healthStopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the health loop never stopped after the node shut down")
	}
	if h.x.core.Started() {
		t.Fatal("a health restart that waited for a sync restarted the core after the node shut down")
	}
}

func TestAFullSyncCutShortByShutdownDoesNotReviveTheCore(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	_, stopHealth := context.WithCancel(context.Background())
	h.x.cancelFunc = stopHealth

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	alice := syncTestUser("1.alice", syncTestTags...)
	bob := syncTestUser("2.bob", syncTestTags...)
	if err := h.x.UpdateUsers(ctx, []*common.User{alice}); err != nil {
		t.Fatal(err)
	}

	probed := h.stats.hold()
	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUsers(ctx, []*common.User{alice, bob}) }()
	select {
	case <-probed:
	case <-time.After(30 * time.Second):
		t.Fatal("the full sync never waited for the restarted core's API")
	}

	h.x.Shutdown()

	select {
	case err := <-synced:
		if err == nil {
			t.Fatal("a full sync cut short by shutdown reported success")
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the full sync never returned after the node shut down")
	}
	if h.x.core.Started() {
		t.Fatal("a full sync cut short by shutdown restarted the core after the node shut down")
	}
}
