package xray

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cancelledBySyncAbort(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "context canceled") || strings.Contains(message, "context cancelled") || strings.Contains(message, "code = Canceled")
}

func TestAUserSyncShorterThanTheGraceIsNotCancelledByAHealthRestart(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 3 * time.Second

	stalled := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		once.Do(func() {
			close(stalled)
			time.Sleep(time.Second)
		})
		return false, nil
	})

	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUser(context.Background(), syncTestUser("1.alice", syncTestTags...)) }()
	select {
	case <-stalled:
	case <-time.After(30 * time.Second):
		t.Fatal("the user sync never reached the core")
	}

	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatalf("a health restart cancelled a user sync that held the sync lock for less than the grace: %v", err)
	}
	h.requireWrittenMatchesLocal(t)
	if len(localState(h.x)["vless"]) != 1 {
		t.Fatalf("the synced user is missing from the node's state: %v", localState(h.x))
	}
}

func TestAHealthRestartCancelsAUserSyncStuckPastTheGrace(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = time.Second

	unstick := make(chan struct{})
	stuck := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		once.Do(func() { close(stuck) })
		<-unstick
		return false, nil
	})

	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUser(context.Background(), syncTestUser("1.alice", syncTestTags...)) }()
	t.Cleanup(func() { close(unstick) })
	select {
	case <-stuck:
	case <-time.After(30 * time.Second):
		t.Fatal("the user sync never reached the core")
	}

	started := time.Now()
	restarted := make(chan error, 1)
	go func() { restarted <- h.x.Restart() }()
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the health restart never recovered the core a user sync was stuck on")
	}

	select {
	case err := <-synced:
		if !cancelledBySyncAbort(err) {
			t.Fatalf("the stuck user sync returned %v, want it cancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stuck user sync was never cancelled")
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart did not leave a running core")
	}
	if elapsed := time.Since(started); elapsed > 45*time.Second {
		t.Fatalf("the health restart took %s to recover a stuck core", elapsed)
	}
	h.requireWrittenMatchesLocal(t)
}

func TestAHealthRestartStillRestartsACoreThatWasStoppedOrNeverReplaced(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)

	observed := h.x.core.state.Load()
	restarted, err := h.x.RestartUnlessReplaced(observed)
	if err != nil || !restarted {
		t.Fatalf("a health restart of a core nobody replaced returned restarted=%v err=%v", restarted, err)
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart did not start the core")
	}

	observed = h.x.core.state.Load()
	h.x.core.Stop()
	restarted, err = h.x.RestartUnlessReplaced(observed)
	if err != nil || !restarted {
		t.Fatalf("a health restart of a core that was stopped meanwhile returned restarted=%v err=%v", restarted, err)
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart left the stopped core down")
	}

	observed = h.x.core.state.Load()
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	replaced := h.x.core.state.Load()
	restarted, err = h.x.RestartUnlessReplaced(observed)
	if err != nil || restarted {
		t.Fatalf("a health restart of a core that was already replaced and runs returned restarted=%v err=%v", restarted, err)
	}
	if h.x.core.state.Load() != replaced {
		t.Fatal("a skipped health restart still replaced the running core")
	}
}

const stuckSyncRounds = 8

func TestHealthRestartsCancellingStuckSyncsNeverLeaveTheCoreBehindTheNode(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 100 * time.Millisecond
	audit := &restartAudit{h: h}
	h.x.syncEntered = audit.entered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	users := newInterleaveUsers()
	setup := users.setup()
	if err := h.x.UpdateUsers(withCall(ctx, setup.id), setup.users); err != nil {
		t.Fatal(err)
	}
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(problems)
	}

	var adds atomic.Int64
	h.core.setFault(func(op, tag, email string) (bool, error) {
		if op == addOperation && adds.Add(1)%4 == 0 {
			time.Sleep(600 * time.Millisecond)
		}
		return false, nil
	})

	cancelled := 0
	for round := range stuckSyncRounds {
		calls := restartRound(users, round)
		start := make(chan struct{})
		errs := make([]error, len(calls))
		var wg sync.WaitGroup
		for index, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[index] = call.run(ctx, h.x)
			}()
		}
		close(start)

		finished := make(chan struct{})
		go func() {
			wg.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Minute):
			t.Fatalf("round %d: a call never returned", round)
		}

		for index, err := range errs {
			switch {
			case err == nil:
			case calls[index].kind != "health" && cancelledBySyncAbort(err):
				cancelled++
			default:
				t.Fatalf("round %d: call %s failed: %v", round, calls[index].id, err)
			}
		}

		audit.settle()
		if _, problems := audit.take(); len(problems) != 0 {
			t.Fatalf("round %d:\n%s", round, strings.Join(problems, "\n"))
		}
		requireCoreMatchesLocal(t, h.x, h.core)
	}

	h.core.setFault(nil)
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	h.requireWrittenMatchesLocal(t)
	requireCoreMatchesLocal(t, h.x, h.core)
	if cancelled == 0 {
		t.Fatal("no user sync was ever cancelled, so the stuck-sync path was never exercised")
	}
	t.Logf("%d user syncs cancelled by health restarts across %d rounds", cancelled, stuckSyncRounds)
}
