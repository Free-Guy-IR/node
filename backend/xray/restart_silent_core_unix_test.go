//go:build !windows

package xray

import (
	"context"
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
	stats := &fakeStatsService{}
	x := newFakeCoreXray(t, stats)
	if err := x.restartCoreWithConfig(x.config); err != nil {
		t.Fatal(err)
	}
	observed := x.core.state.Load()

	syncCtx, cancelSync := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelSync()
	if err := x.SyncUsers(syncCtx, nil); err == nil {
		t.Fatal("a full sync against a core that never answers succeeded")
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
}
