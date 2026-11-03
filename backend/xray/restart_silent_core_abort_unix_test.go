//go:build !windows

package xray

import (
	"context"
	"testing"
	"time"
)

type healthRestartResult struct {
	restarted bool
	err       error
}

func TestAHealthRestartPreemptsASilentFullSyncWhoseCallerAlreadyGaveUp(t *testing.T) {
	stats := &fakeStatsService{}
	x := newFakeCoreXray(t, stats)
	x.stuckGrace = 500 * time.Millisecond
	x.restoreReady = time.Second
	x.restartReady = time.Second
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}
	observed := x.core.state.Load()

	callerCtx, cancelCaller := context.WithTimeout(context.Background(), time.Second)
	defer cancelCaller()
	syncDone := make(chan error, 1)
	go func() { syncDone <- x.SyncUsers(callerCtx, nil) }()

	time.Sleep(2 * time.Second)
	if callerCtx.Err() == nil {
		t.Fatal("the caller of the full sync has not given up yet")
	}

	started := time.Now()
	restartDone := make(chan healthRestartResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		restarted, err := x.RestartUnlessReplaced(ctx, observed)
		restartDone <- healthRestartResult{restarted, err}
	}()

	limit := 20 * time.Second
	select {
	case result := <-restartDone:
		if !result.restarted {
			t.Fatalf("the health restart skipped the silent core after %s (err %v)", time.Since(started), result.err)
		}
	case <-time.After(limit):
		t.Fatalf("the health restart is still blocked behind a silent full sync %s after its caller gave up", limit)
	}
	select {
	case err := <-syncDone:
		if err == nil {
			t.Fatal("a full sync against a core that never answers succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the aborted full sync never returned")
	}
}
