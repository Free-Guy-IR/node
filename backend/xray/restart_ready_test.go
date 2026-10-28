package xray

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func userSyncCalls(x *Xray, ctx context.Context) map[string]func() error {
	return map[string]func() error{
		"SyncUser":              func() error { return x.SyncUser(ctx, &common.User{}) },
		"SyncUsers":             func() error { return x.SyncUsers(ctx, nil) },
		"UpdateUsers":           func() error { return x.UpdateUsers(ctx, nil) },
		"UpdateUsersAndRestart": func() error { return x.UpdateUsersAndRestart(ctx, nil) },
	}
}

func callWithoutPanic(call func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("the sync ran and panicked: %v", recovered)
		}
	}()
	return call()
}

func TestUserSyncsQueuedBehindARestartGiveUpWhenTheirContextIsCancelled(t *testing.T) {
	x := &Xray{}
	x.syncMu.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := userSyncCalls(x, ctx)
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(calls))
	for name, call := range calls {
		go func() { results <- result{name, callWithoutPanic(call)} }()
	}

	time.Sleep(100 * time.Millisecond)
	cancel()

	deadline := time.After(2 * time.Second)
	for range calls {
		select {
		case got := <-results:
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("%s queued behind a restart returned %v after its context was cancelled, want context.Canceled", got.name, got.err)
			}
		case <-deadline:
			t.Fatal("a user sync kept waiting for the sync lock after its context was cancelled, so the controller's lifecycle lock stays held for the whole restart")
		}
	}

	x.syncMu.Unlock()
	relocked := make(chan struct{})
	go func() {
		x.syncMu.Lock()
		close(relocked)
	}()
	select {
	case <-relocked:
		x.syncMu.Unlock()
	case <-time.After(2 * time.Second):
		t.Fatal("a user sync that gave up on the sync lock left it held")
	}
}

func TestAUserSyncWhoseContextEndsAsItGetsTheSyncLockDoesNotRun(t *testing.T) {
	for _, name := range []string{"SyncUser", "SyncUsers", "UpdateUsers", "UpdateUsersAndRestart"} {
		x := &Xray{}
		ctx, cancel := context.WithCancel(context.Background())
		x.syncEntered = func(context.Context) { cancel() }

		err := callWithoutPanic(userSyncCalls(x, ctx)[name])
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s whose context ended as it got the sync lock returned %v, want context.Canceled", name, err)
		}
		if !x.syncMu.TryLock() {
			t.Fatalf("%s left the sync lock held", name)
		}
		x.syncMu.Unlock()
	}
}
