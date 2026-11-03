//go:build !windows

package xray

import (
	"context"
	"sync/atomic"
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

func TestAnAbortRacingACoreThatTurnsReadyEndsInOneCleanOutcome(t *testing.T) {
	stats := &fakeStatsService{}
	x := newFakeCoreXray(t, stats)
	x.stuckGrace = 200 * time.Millisecond
	x.readyPoll = 20 * time.Millisecond
	x.restoreReady = 3 * time.Second
	x.restartReady = 3 * time.Second
	var restarts atomic.Int64
	x.restartCore = func(config *Config) error {
		err := x.core.Restart(config, false)
		restarts.Add(1)
		return err
	}
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}

	outcomes := map[string]int{}
	for round, readyAfter := range []time.Duration{0, 230 * time.Millisecond, 250 * time.Millisecond, 270 * time.Millisecond, 600 * time.Millisecond} {
		stats.healthy.Store(false)
		observed := x.core.state.Load()
		previous := x.activeConfig()
		before := restarts.Load()

		callerCtx, cancelCaller := context.WithTimeout(context.Background(), 100*time.Millisecond)
		syncDone := make(chan error, 1)
		go func() { syncDone <- x.SyncUsers(callerCtx, nil) }()
		waitUntil(t, "the full sync started its candidate core", func() bool { return restarts.Load() == before+1 })
		time.Sleep(x.stuckGrace + 100*time.Millisecond)

		restartDone := make(chan healthRestartResult, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			restarted, err := x.RestartUnlessReplaced(ctx, observed)
			restartDone <- healthRestartResult{restarted, err}
		}()
		time.Sleep(readyAfter)
		stats.healthy.Store(true)

		var syncErr error
		var result healthRestartResult
		for pending := 2; pending > 0; pending-- {
			select {
			case syncErr = <-syncDone:
			case result = <-restartDone:
			case <-time.After(30 * time.Second):
				cancelCaller()
				t.Fatalf("round %d: the abort racing a ready core left a call hanging", round)
			}
		}
		cancelCaller()

		if result.err != nil {
			t.Fatalf("round %d: the health restart failed: %v", round, result.err)
		}
		total := restarts.Load() - before
		var outcome string
		switch {
		case syncErr == nil && !result.restarted:
			outcome = "ready"
			if total != 1 {
				t.Fatalf("round %d: the sync won and the health restart was skipped, yet the core restarted %d times", round, total)
			}
		case syncErr == nil:
			outcome = "ready then restart"
			if total != 2 {
				t.Fatalf("round %d: the sync won and the health restart restarted, yet the core restarted %d times", round, total)
			}
		default:
			outcome = "restart"
			if !result.restarted {
				t.Fatalf("round %d: the sync was aborted (%v) but the health restart skipped the core", round, syncErr)
			}
			if total != 3 {
				t.Fatalf("round %d: the aborted sync and the health restart restarted the core %d times, want candidate, restore and one health restart", round, total)
			}
		}
		if active := x.activeConfig(); (syncErr == nil) == (active == previous) {
			t.Fatalf("round %d: outcome %q left the node on the wrong config (sync err %v)", round, outcome, syncErr)
		}
		if !x.syncMu.TryLock() {
			t.Fatalf("round %d: outcome %q leaked syncMu", round, outcome)
		}
		x.syncMu.Unlock()
		x.holderMu.Lock()
		holder := x.syncHolder
		x.holderMu.Unlock()
		if holder != nil {
			t.Fatalf("round %d: outcome %q left a sync holder registered", round, outcome)
		}
		if err := x.probe(context.Background()); err != nil {
			t.Fatalf("round %d: outcome %q left a core that does not answer: %v", round, outcome, err)
		}
		time.Sleep(300 * time.Millisecond)
		if got := restarts.Load() - before; got != total {
			t.Fatalf("round %d: outcome %q restarted the core again after both calls returned (%d, then %d)", round, outcome, total, got)
		}
		outcomes[outcome]++
	}
	t.Logf("outcomes: %v", outcomes)
}
