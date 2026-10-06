package xray

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

const coreStartedHelperEnv = "PG_NODE_STARTED_TEST_HELPER"

func TestCoreStartedHelperProcess(t *testing.T) {
	if os.Getenv(coreStartedHelperEnv) != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte{1})
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startCoreTestProcess(t *testing.T, core *Core) (io.WriteCloser, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoreStartedHelperProcess$")
	cmd.Env = append(os.Environ(), coreStartedHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		t.Fatal(err)
	}

	waitDone := make(chan struct{})
	core.mu.Lock()
	core.process = cmd
	core.processPID = cmd.Process.Pid
	core.waitDone = waitDone
	core.publishStateLocked()
	core.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		waitForCoreTestProcess(t, waitDone)
	})

	if _, err := io.ReadFull(stdout, make([]byte, 1)); err != nil {
		t.Fatalf("child did not become ready: %v", err)
	}
	return stdin, waitDone
}

func waitForCoreTestProcess(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child process")
	}
}

func pollCoreStarted(core *Core) func() {
	done := make(chan struct{})
	var ready, workers sync.WaitGroup
	for range 4 {
		ready.Add(1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			core.Started()
			ready.Done()
			for {
				select {
				case <-done:
					return
				default:
					core.Started()
					runtime.Gosched()
				}
			}
		}()
	}
	ready.Wait()
	return func() {
		close(done)
		workers.Wait()
	}
}

func startedWithin(core *Core, limit time.Duration) (started bool, answered bool) {
	answer := make(chan bool, 1)
	go func() { answer <- core.Started() }()
	select {
	case started = <-answer:
		return started, true
	case <-time.After(limit):
		return false, false
	}
}

func TestCoreStartedProcessExit(t *testing.T) {
	core := &Core{}
	if core.Started() {
		t.Fatal("new core reports running")
	}
	stdin, waitDone := startCoreTestProcess(t, core)
	if !core.Started() {
		t.Fatal("live child reports stopped")
	}
	stopPolling := pollCoreStarted(core)
	defer stopPolling()

	core.mu.Lock()
	_ = stdin.Close()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		core.mu.Unlock()
		t.Fatal("process watcher blocked while core mutex was held")
	}
	started, answered := startedWithin(core, 2*time.Second)
	core.mu.Unlock()
	if !answered {
		t.Fatal("Started blocked while the core mutex was held; it must read the published state without c.mu")
	}
	if started || core.Started() {
		t.Fatal("exited child reports running")
	}
}

func TestCoreStartedConcurrentStopAndReplacement(t *testing.T) {
	core := &Core{}
	stopPolling := pollCoreStarted(core)
	defer stopPolling()

	for range 8 {
		_, waitDone := startCoreTestProcess(t, core)
		if !core.Started() {
			t.Fatal("replacement child reports stopped")
		}
		if err := core.Start(&Config{}, false); err == nil {
			t.Fatal("duplicate start succeeded")
		}
		core.Stop()
		waitForCoreTestProcess(t, waitDone)
		if core.Started() {
			t.Fatal("stopped child reports running")
		}
	}
}
