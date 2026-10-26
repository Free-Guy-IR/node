package backend

import (
	"context"
	"errors"
	"testing"
	"time"
)

func doneWithin(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestADetachedWaitIgnoresItsCallerLeaving(t *testing.T) {
	for name, leave := range map[string]func() (context.Context, context.CancelFunc){
		"cancel": func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
		"deadline": func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), time.Millisecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			caller, cancelCaller := leave()
			detached, cancel := DetachFromCaller(caller, time.Minute)
			defer cancel()
			cancelCaller()
			<-caller.Done()
			if doneWithin(detached, 200*time.Millisecond) {
				t.Fatalf("the detached wait ended when its caller left (%v)", context.Cause(caller))
			}
		})
	}
}

func TestADetachedWaitStopsWhenItsSyncIsAbortedEvenAfterTheCallerLeft(t *testing.T) {
	abort, cancelAbort := context.WithCancel(context.Background())
	caller, cancelCaller := context.WithCancel(WithSyncAbort(context.Background(), abort))
	detached, cancel := DetachFromCaller(caller, time.Minute)
	defer cancel()

	cancelCaller()
	if doneWithin(detached, 100*time.Millisecond) {
		t.Fatal("the detached wait ended when its caller left")
	}
	cancelAbort()
	if !doneWithin(detached, time.Second) {
		t.Fatal("aborting the sync did not end the detached wait")
	}
}

func TestADetachedWaitStartedAfterItsSyncWasAbortedEndsAtOnce(t *testing.T) {
	abort, cancelAbort := context.WithCancel(context.Background())
	cancelAbort()
	detached, cancel := DetachFromCaller(WithSyncAbort(context.Background(), abort), time.Minute)
	defer cancel()
	if !doneWithin(detached, time.Second) {
		t.Fatal("a wait detached from an already aborted sync did not end")
	}
}

func TestADetachedWaitStopsWhenItsCallerIsCancelledForAnotherReason(t *testing.T) {
	caller, cancelCaller := context.WithCancelCause(context.Background())
	detached, cancel := DetachFromCaller(caller, time.Minute)
	defer cancel()

	cancelCaller(errors.New("a health restart needs the core back"))
	if !doneWithin(detached, time.Second) {
		t.Fatal("a cancellation that is not the caller leaving did not end the detached wait")
	}
}

func TestADetachedWaitEndsAtItsOwnTimeoutAndKeepsTheCallersValues(t *testing.T) {
	type key struct{}
	caller := context.WithValue(context.Background(), key{}, "kept")
	detached, cancel := DetachFromCaller(caller, 50*time.Millisecond)
	defer cancel()
	if detached.Value(key{}) != "kept" {
		t.Fatal("the detached wait lost the caller's values")
	}
	if !doneWithin(detached, time.Second) || !errors.Is(detached.Err(), context.DeadlineExceeded) {
		t.Fatalf("the detached wait did not end at its own timeout: %v", detached.Err())
	}
}
