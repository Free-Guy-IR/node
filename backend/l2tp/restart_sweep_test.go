package l2tp

import (
	"errors"
	"sync"
	"testing"
)

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

func TestL2TPRestartAfterTheBackendStoppedIsRefusedAndSpawnsNothing(t *testing.T) {
	e := newAcctEnv(t)
	o := e.instance("u1", "u2")
	stopAccounting(o)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- o.Restart()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, errNotStarted) {
			t.Fatalf("Restart after the backend stopped must return errNotStarted, got %v", err)
		}
	}
	if o.process != nil {
		t.Fatal("a refused restart after the backend stopped must not spawn a process")
	}
	if o.Started() {
		t.Fatal("a stopped backend must not report started")
	}
}
