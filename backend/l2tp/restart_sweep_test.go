package l2tp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func rsweepInstance(t *testing.T, usernames ...string) (*acctEnv, *L2TP) {
	t.Helper()
	e := newAcctEnv(t)
	return e, e.instance(usernames...)
}

func TestL2TPRestartOnANeverStartedInstanceIsRefused(t *testing.T) {
	e := newAcctEnv(t)
	o := e.prepare("u1", "u2")

	if err := o.Restart(); !errors.Is(err, errNotStarted) {
		t.Fatalf("Restart on a never-started instance must return errNotStarted, got %v", err)
	}
	if o.process != nil {
		t.Fatal("a refused restart must not spawn a process")
	}
	if o.Started() {
		t.Fatal("a never-started instance must not report started after a refused restart")
	}
}

func TestL2TPRestartAfterShutdownIsRefusedAndSpawnsNothing(t *testing.T) {
	_, o := rsweepInstance(t, "u1", "u2")

	o.Shutdown()

	if err := o.Restart(); !errors.Is(err, errNotStarted) {
		t.Fatalf("Restart after Shutdown must return errNotStarted, got %v", err)
	}
	if err := o.UpdateUsersAndRestart(context.Background(), e2eUsers(o, "u1")); !errors.Is(err, errNotStarted) {
		t.Fatalf("UpdateUsersAndRestart after Shutdown must return errNotStarted, got %v", err)
	}
	if o.process != nil {
		t.Fatal("a refused restart after shutdown must not spawn a process")
	}
	if o.Started() {
		t.Fatal("a backend must not report started after shutdown")
	}
}

func e2eUsers(o *L2TP, usernames ...string) []*common.User {
	out := make([]*common.User, 0, len(usernames))
	for _, u := range usernames {
		out = append(out, user(u, "pw-"+u, o.config.InboundTag))
	}
	return out
}

func TestL2TPShutdownRacingUserSyncsAndRestartsIsRaceFree(t *testing.T) {
	e, o := rsweepInstance(t, "u1")
	ctx := context.Background()
	pool := []string{"u1", "u2", "u3", "u4"}

	stop := make(chan struct{})
	shutdownDone := make(chan struct{})
	var workers sync.WaitGroup

	spawn := func(fn func(i int)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				fn(i)
			}
		}()
	}

	spawn(func(i int) { _ = o.SyncUser(ctx, user(pool[i%len(pool)], "pw", o.config.InboundTag)) })
	spawn(func(i int) { _ = o.SyncUsers(ctx, e.users(pool[:1+i%len(pool)]...)) })
	spawn(func(i int) { _ = o.UpdateUsers(ctx, e2eUsers(o, pool[i%len(pool)])) })
	spawn(func(int) {
		select {
		case <-shutdownDone:
		case <-stop:
			return
		}
		if err := o.Restart(); !errors.Is(err, errNotStarted) {
			t.Errorf("a restart after shutdown must return errNotStarted, got %v", err)
			return
		}
	})

	time.Sleep(time.Duration(10+time.Now().UnixNano()%20) * time.Millisecond)
	o.Shutdown()
	close(shutdownDone)
	time.Sleep(15 * time.Millisecond)
	close(stop)
	workers.Wait()

	if o.Started() {
		t.Fatal("the backend reports started after shutdown")
	}
	if err := o.Restart(); !errors.Is(err, errNotStarted) {
		t.Fatalf("a final restart after shutdown must return errNotStarted, got %v", err)
	}
	if o.process != nil {
		t.Fatal("no restart after shutdown may spawn a process")
	}
}
