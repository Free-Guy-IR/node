package ipsec

import (
	"testing"
	"time"
)

func TestLogNeverBlocksWhileTheDaemonMutexIsHeld(t *testing.T) {
	d := &daemon{}
	var got []string
	var logf Logf = func(severity, message string) { got = append(got, severity+":"+message) }
	d.logf.Store(&logf)

	done := make(chan struct{})
	go func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.log("Info", "logged while holding the mutex")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("log() deadlocked against the daemon mutex")
	}
	if len(got) != 1 || got[0] != "Info:logged while holding the mutex" {
		t.Fatalf("unexpected log output: %v", got)
	}
}

func TestLogWithoutASinkIsANoOp(t *testing.T) {
	d := &daemon{}
	d.log("Info", "nobody is listening")
}

func TestStopOnAnUnstartedDaemonIsSafe(t *testing.T) {
	d := &daemon{}
	d.stop()
	if d.running() {
		t.Fatal("an unstarted daemon must not report running")
	}
}
