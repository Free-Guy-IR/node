package xray

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const advRestartBound = 150 * time.Second

func runningCorePID(c *Core) int {
	state := c.state.Load()
	if !startedFromState(state) {
		return 0
	}
	return state.cmd.Process.Pid
}

func TestAHealthRestartRecoversACoreThatAUserSyncIsStuckOn(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)

	unstick := make(chan struct{})
	stuck := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		once.Do(func() { close(stuck) })
		<-unstick
		return false, nil
	})

	synced := make(chan error, 1)
	go func() {
		synced <- h.x.SyncUser(context.Background(), syncTestUser("1.alice", syncTestTags...))
	}()
	t.Cleanup(func() {
		close(unstick)
		select {
		case <-synced:
		case <-time.After(advRestartBound):
			t.Error("the stuck user sync never returned after the core answered")
		}
		h.x.core.Stop()
	})

	select {
	case <-stuck:
	case <-time.After(30 * time.Second):
		t.Fatal("the user sync never reached the core")
	}

	restarted := make(chan error, 1)
	go func() { restarted <- h.x.Restart() }()
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(advRestartBound):
		t.Fatalf("a health restart could not recover a core whose API hangs, because a user sync waiting on that same core held the sync lock for %s", advRestartBound)
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart did not leave a running core")
	}
}

func TestAHealthRestartQueuedBehindAFullSyncDoesNotRestartTheCoreTheSyncJustBroughtUp(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	healthCtx, stopHealth := context.WithCancel(context.Background())
	h.x.cancelFunc = stopHealth
	h.stats.hold()

	gate := make(chan struct{})
	gated := make(chan struct{})
	var first sync.Once
	h.x.syncEntered = func(context.Context) {
		first.Do(func() {
			close(gated)
			<-gate
		})
	}

	syncCtx, cancelSync := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelSync()
	synced := make(chan error, 1)
	go func() {
		synced <- h.x.SyncUsers(syncCtx, []*common.User{syncTestUser("1.alice", syncTestTags...)})
	}()
	select {
	case <-gated:
	case <-time.After(30 * time.Second):
		t.Fatal("the full sync never took the sync lock")
	}

	healthStopped := make(chan struct{})
	go func() {
		defer close(healthStopped)
		h.x.checkXrayHealth(healthCtx)
	}()
	t.Cleanup(func() {
		stopHealth()
		select {
		case <-healthStopped:
		case <-time.After(advRestartBound):
			t.Error("the health loop never stopped")
		}
		h.x.core.Stop()
	})

	if !waitForGoroutineIn(t, 30*time.Second, ".(*Xray).checkXrayHealth", ".(*Xray).lockSync") {
		t.Fatal("the health loop never decided to restart the stopped core")
	}

	h.stats.release()
	close(gate)

	select {
	case err := <-synced:
		if err != nil {
			t.Fatalf("the full sync failed: %v", err)
		}
	case <-time.After(advRestartBound):
		t.Fatal("the full sync never finished")
	}
	syncedPID := runningCorePID(h.x.core)
	if syncedPID == 0 {
		t.Fatal("the full sync did not leave a running core")
	}

	deadline := time.Now().Add(advRestartBound)
	for goroutineIn(".(*Xray).checkXrayHealth", ".(*Xray).Restart") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if goroutineIn(".(*Xray).checkXrayHealth", ".(*Xray).Restart") {
		t.Fatal("the queued health restart never finished")
	}

	if after := runningCorePID(h.x.core); after != syncedPID {
		t.Fatalf("a health restart decided while the core was down ran after the full sync had already brought up a healthy core, and restarted it again (core pid %d -> %d)", syncedPID, after)
	}
}
