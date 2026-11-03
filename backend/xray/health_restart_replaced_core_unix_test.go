//go:build !windows

package xray

import (
	"context"
	"testing"
	"time"
)

func TestTheHealthLoopJudgesTheCoreASyncPutInPlaceByWhetherItAnswers(t *testing.T) {
	for _, tc := range []struct {
		name        string
		answers     bool
		wantRestart bool
	}{
		{name: "the sync's core does not answer", answers: false, wantRestart: true},
		{name: "the sync's core answers", answers: true, wantRestart: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := &fakeStatsService{probes: make(chan struct{}, 256)}
			x := newFakeCoreXray(t, stats)

			unlockSync, err := x.lockSync(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			syncHeld := true
			releaseSync := func() {
				if syncHeld {
					syncHeld = false
					unlockSync()
				}
			}
			defer releaseSync()

			healthCtx, stopHealth := context.WithCancel(context.Background())
			healthStopped := make(chan struct{})
			go func() {
				defer close(healthStopped)
				x.checkXrayHealth(healthCtx)
			}()
			t.Cleanup(func() {
				stopHealth()
				select {
				case <-healthStopped:
				case <-time.After(60 * time.Second):
					t.Error("the health loop did not stop 60s after its context was cancelled")
				}
			})

			if !waitForGoroutineIn(t, 30*time.Second, ".(*Xray).checkXrayHealth", ".(*Xray).lockSync") {
				t.Fatal("the health loop never queued a restart of the stopped core behind the sync")
			}

			if err := x.restartCoreWithConfig(x.activeConfig()); err != nil {
				t.Fatal(err)
			}
			syncPID := corePID(x)
			if syncPID == 0 {
				t.Fatal("the sync left no core running")
			}
			stats.healthy.Store(tc.answers)
			for len(stats.probes) > 0 {
				<-stats.probes
			}
			releaseSync()

			if tc.wantRestart {
				deadline := time.Now().Add(12 * time.Second)
				for corePID(x) == syncPID && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				if got := corePID(x); got == syncPID {
					t.Fatalf("the health restart that waited behind the sync left the core the sync put in place running for 12s although that core never answers its API (pid %d)", got)
				}
				return
			}

			for seen := 0; seen < 2; seen++ {
				select {
				case <-stats.probes:
				case <-time.After(15 * time.Second):
					t.Fatalf("the health loop probed the core %d times in 15s after the sync released its lock, want 2", seen)
				}
			}
			if got := corePID(x); got != syncPID {
				t.Fatalf("the health restart that waited behind the sync replaced the core the sync put in place although that core answers (pid %d, now %d)", syncPID, got)
			}
		})
	}
}
