package l2tp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const overlapSharedKey = "overlap-shared-key"

func runningInstanceFromConfig(t *testing.T, e *acctEnv, usernames ...string) *L2TP {
	t.Helper()

	cfg, err := NewConfig(fmt.Sprintf(`{"inbound_tag":%q,"server_addr":"1.2.3.4","pool":"10.31.0.0/24","psk":%q}`, e.tag, overlapSharedKey))
	if err != nil {
		t.Fatal(err)
	}
	o := newInstance(cfg, e.nodeConfig())
	o.users.replaceAll(e.users(usernames...))
	if err := o.startWithAccounting(func() error {
		o.state = lifecycleRunning
		return nil
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	return o
}

func l2tpGoroutineBlockedIn(frames ...string) bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for stack := range strings.SplitSeq(string(buf), "\n\n") {
		matched := true
		for _, frame := range frames {
			if !strings.Contains(stack, frame) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func waitForL2TPGoroutine(timeout time.Duration, frames ...string) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l2tpGoroutineBlockedIn(frames...) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestL2TPRestartWaitsForAShutdownThatHoldsTheLock(t *testing.T) {
	e := newAcctEnv(t)
	o := runningInstanceFromConfig(t, e, "u1", "u2")

	o.mu.Lock()
	held := true
	release := func() {
		if held {
			held = false
			o.mu.Unlock()
		}
	}
	t.Cleanup(release)

	restarted := make(chan error, 1)
	go func() { restarted <- o.Restart() }()

	if !waitForL2TPGoroutine(10*time.Second, ".(*L2TP).Restart", "sync.(*RWMutex).Lock") {
		release()
		err := <-restarted
		t.Fatalf("Restart ran without waiting for the lock a running Shutdown holds (it returned %v)", err)
	}

	o.state = lifecycleStopped
	release()

	select {
	case err := <-restarted:
		if !errors.Is(err, errNotStarted) {
			t.Fatalf("a Restart that waited while the backend shut down returned %v, want errNotStarted", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the Restart never returned")
	}
	if o.process != nil || o.Started() {
		t.Fatal("a Restart that waited while the backend shut down started xl2tpd")
	}
}

func TestL2TPRestartsOverlappingARealShutdownAreSerialised(t *testing.T) {
	e := newAcctEnv(t)
	o := runningInstanceFromConfig(t, e, "u1")
	ctx := context.Background()

	stop := make(chan struct{})
	var workers sync.WaitGroup
	for range 3 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = o.Restart()
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = o.UpdateUsers(ctx, e.users("u1"))
		}
	}()

	time.Sleep(30 * time.Millisecond)
	o.Shutdown()
	time.Sleep(30 * time.Millisecond)
	close(stop)
	workers.Wait()

	if err := o.Restart(); !errors.Is(err, errNotStarted) {
		t.Fatalf("a Restart after the overlapping Shutdown returned %v, want errNotStarted", err)
	}
	if o.process != nil || o.Started() {
		t.Fatal("a Restart overlapping Shutdown left xl2tpd running")
	}
}
