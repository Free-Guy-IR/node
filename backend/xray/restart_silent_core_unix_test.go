//go:build !windows

package xray

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestAHealthRestartRestartsACoreASyncReplacedThatDoesNotAnswer(t *testing.T) {
	stats := &fakeStatsService{}
	x := newFakeCoreXray(t, stats)
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}
	observed := x.core.state.Load()
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}
	replacedPID := corePID(x)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	restarted, err := x.RestartUnlessReplaced(ctx, observed)
	if !restarted {
		t.Fatalf("a health restart skipped the core a sync put in place although it does not answer its API (pid %d, now %d, err %v)", replacedPID, corePID(x), err)
	}
	if got := corePID(x); got == replacedPID {
		t.Fatalf("a health restart that reported a restart left the silent core running (pid %d)", got)
	}
}

func TestAHealthRestartRestartsTheSilentCoreAFailedFullSyncLeftBehind(t *testing.T) {
	const budget = 15 * time.Second
	started := time.Now()
	stats := &fakeStatsService{}
	x := newFakeCoreXray(t, stats)
	x.fullSyncReady = time.Second
	x.restoreReady = time.Second
	x.restartReady = time.Second
	var inCoreRestarts atomic.Int64
	x.restartCore = func(config *Config) error {
		began := time.Now()
		defer func() { inCoreRestarts.Add(int64(time.Since(began))) }()
		return x.core.Restart(config, x.cfg.Debug)
	}
	overBudget := func() time.Duration {
		return time.Since(started) - time.Duration(inCoreRestarts.Load()) - budget
	}
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}
	observed := x.core.state.Load()

	syncCtx, cancelSync := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelSync()
	syncDone := make(chan error, 1)
	go func() { syncDone <- x.SyncUsers(syncCtx, nil) }()
	select {
	case err := <-syncDone:
		if err == nil {
			t.Fatal("a full sync against a core that never answers succeeded")
		}
	case <-time.After(time.Minute):
		t.Fatal("the failed full sync still holds the node a minute later")
	}
	if over := overBudget(); over > 0 {
		t.Fatalf("the failed full sync waited %s past the test's %s budget outside core restarts", over, budget)
	}
	if !x.core.Started() {
		t.Fatal("the failed full sync left no core running")
	}
	if err := x.probe(context.Background()); err == nil {
		t.Fatal("the core the failed full sync left behind answers its API")
	}
	silentPID := corePID(x)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	restarted, err := x.RestartUnlessReplaced(ctx, observed)
	if !restarted {
		t.Fatalf("a health restart skipped the silent core a failed full sync left behind (pid %d, now %d, err %v)", silentPID, corePID(x), err)
	}
	if got := corePID(x); got == silentPID {
		t.Fatalf("a health restart that reported a restart left the silent core running (pid %d)", got)
	}
	if over := overBudget(); over > 0 {
		t.Fatalf("the silent core recovery waited %s past the test's %s budget outside core restarts", over, budget)
	}
	t.Logf("took %s, %s of it in core restarts", time.Since(started).Round(time.Millisecond), time.Duration(inCoreRestarts.Load()).Round(time.Millisecond))
}
