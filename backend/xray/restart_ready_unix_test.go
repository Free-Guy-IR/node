//go:build !windows

package xray

import (
	"context"
	"testing"
	"time"
)

type restartOutcome struct {
	restarted bool
	err       error
}

func restartInBackground(x *Xray, ctx context.Context, observed *coreState) <-chan restartOutcome {
	done := make(chan restartOutcome, 1)
	go func() {
		restarted, err := x.RestartUnlessReplaced(ctx, observed)
		done <- restartOutcome{restarted, err}
	}()
	return done
}

func TestAHealthRestartHoldsTheSyncLockUntilTheCoreAnswersAndItsContextEndsTheWait(t *testing.T) {
	x := newFakeCoreXray(t, &fakeStatsService{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := restartInBackground(x, ctx, nil)
	waitUntil(t, "the health restart starts the new core", x.core.Started)

	time.Sleep(1500 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("the health restart returned %+v before the new core answered", got)
	default:
	}
	if x.syncMu.TryLock() {
		x.syncMu.Unlock()
		t.Fatal("the health restart released the sync lock before the new core answered")
	}

	cancel()
	select {
	case got := <-done:
		if !got.restarted || got.err == nil {
			t.Fatalf("a health restart whose context ended before the core answered returned %+v, want restarted with an error", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the health restart kept waiting for the core 5s after its context was cancelled")
	}
	if !x.syncMu.TryLock() {
		t.Fatal("the health restart left the sync lock held after its wait ended")
	}
	x.syncMu.Unlock()
}

func TestAHealthRestartLeavesACoreThatAnswersAgainAndRestartsOneThatDoesNot(t *testing.T) {
	stats := &fakeStatsService{}
	x := newFakeCoreXray(t, stats)
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}

	stats.healthy.Store(true)
	pid := corePID(x)
	restarted, err := x.RestartUnlessReplaced(context.Background(), x.core.state.Load())
	if err != nil || restarted {
		t.Fatalf("a health restart of a core that answers again returned restarted=%v err=%v, want it skipped", restarted, err)
	}
	if got := corePID(x); got != pid {
		t.Fatalf("a skipped health restart replaced the answering core (pid %d, now %d)", pid, got)
	}

	stats.healthy.Store(false)
	done := restartInBackground(x, context.Background(), x.core.state.Load())
	waitUntil(t, "the health restart replaces the core that does not answer", func() bool {
		got := corePID(x)
		return got != 0 && got != pid
	})
	stats.healthy.Store(true)
	select {
	case got := <-done:
		if !got.restarted || got.err != nil {
			t.Fatalf("a health restart of a core that does not answer returned %+v, want restarted without an error", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the health restart did not return 10s after the new core answered")
	}
}
