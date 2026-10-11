package l2tp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type hostSeam struct {
	swanctl string
	pids    string
	loads   atomic.Int64
}

func useHostSeam(t *testing.T, e *acctEnv) *hostSeam {
	t.Helper()
	root := t.TempDir()
	h := &hostSeam{swanctl: filepath.Join(root, "swanctl"), pids: filepath.Join(root, "xl2tpd.pids")}
	fake := filepath.Join(root, "xl2tpd")
	script := fmt.Sprintf("#!/bin/sh\necho $$ >> '%s'\nexec sleep 3600\n", h.pids)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prevChap, prevSwan, prevXl2tpd, prevLoad := chapSecretsPath, swanctlDir, xl2tpdPath, loadSwanctl
	chapSecretsPath = filepath.Join(root, "ppp", "chap-secrets")
	swanctlDir = h.swanctl
	xl2tpdPath = fake
	loadSwanctl = func() error {
		h.loads.Add(1)
		return nil
	}
	t.Cleanup(func() {
		chapSecretsPath, swanctlDir, xl2tpdPath, loadSwanctl = prevChap, prevSwan, prevXl2tpd, prevLoad
	})
	e.write(chapSecretsPath, chapKeptComment+"\n"+chapForeignLine+"\n")
	return h
}

func (h *hostSeam) spawned(t *testing.T) []int {
	t.Helper()
	raw, err := os.ReadFile(h.pids)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, f := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("pid file holds %q", f)
		}
		pids = append(pids, pid)
	}
	return pids
}

func (h *hostSeam) waitSpawned(t *testing.T, n int) []int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if pids := h.spawned(t); len(pids) >= n || time.Now().After(deadline) {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *hostSeam) expectNoXl2tpdLeft(t *testing.T, step string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range h.spawned(t) {
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("%s: xl2tpd %d is still running", step, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func (h *hostSeam) fragment(o *L2TP) string {
	return filepath.Join(h.swanctl, "conf.d", o.confFileName())
}

func seamInstance(t *testing.T, e *acctEnv, usernames ...string) *L2TP {
	t.Helper()
	cfg, err := NewConfig(fmt.Sprintf(`{"inbound_tag":%q,"server_addr":"1.2.3.4","pool":"10.31.0.0/24","psk":"seam-shared-key"}`, e.tag))
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
	t.Cleanup(o.Shutdown)
	return o
}

func expectStoppedAndClean(t *testing.T, step string, e *acctEnv, h *hostSeam, o *L2TP) {
	t.Helper()
	if err := o.Restart(); !errors.Is(err, errNotStarted) {
		t.Fatalf("%s: Restart returned %v, want errNotStarted", step, err)
	}
	if err := o.UpdateUsersAndRestart(context.Background(), e.users("u1")); !errors.Is(err, errNotStarted) {
		t.Fatalf("%s: UpdateUsersAndRestart returned %v, want errNotStarted", step, err)
	}
	o.mu.RLock()
	process := o.process
	o.mu.RUnlock()
	if process != nil || o.Started() {
		t.Fatalf("%s: the stopped backend still has xl2tpd", step)
	}
	h.expectNoXl2tpdLeft(t, step)
	expectChapSecrets(t, step, e)
	if _, err := os.Stat(h.fragment(o)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: the swanctl fragment is still there (%v)", step, err)
	}
}

func TestL2TPSeamRestartOfARunningInstanceRewritesItsConfigAndReplacesXl2tpd(t *testing.T) {
	e := newAcctEnv(t)
	h := useHostSeam(t, e)
	o := seamInstance(t, e, "u1", "u2")

	for i := 1; i <= 2; i++ {
		if err := o.Restart(); err != nil {
			t.Fatalf("Restart %d of a running instance: %v", i, err)
		}
		pids := h.waitSpawned(t, i)
		if len(pids) != i || syscall.Kill(pids[i-1], 0) != nil {
			t.Fatalf("Restart %d: xl2tpd processes %v, want %d with the last one running", i, pids, i)
		}
		if i == 2 && syscall.Kill(pids[0], 0) == nil {
			t.Fatalf("Restart %d left the previous xl2tpd %d running", i, pids[0])
		}
	}
	if h.loads.Load() != 2 {
		t.Fatalf("swanctl was loaded %d times, want once per Restart", h.loads.Load())
	}
	if _, err := os.Stat(h.fragment(o)); err != nil {
		t.Fatalf("Restart did not write the swanctl fragment: %v", err)
	}
	expectChapSecrets(t, "after the restarts", e, "u1", "u2")
	if err := o.UpdateUsersAndRestart(context.Background(), e.users("u3")); err != nil {
		t.Fatalf("UpdateUsersAndRestart of a running instance: %v", err)
	}
	expectChapSecrets(t, "after UpdateUsersAndRestart", e, "u1", "u2", "u3")

	o.Shutdown()
	expectStoppedAndClean(t, "after the shutdown", e, h, o)
}

func TestL2TPSeamRestartAfterARealShutdownIsRefusedAndSpawnsNothing(t *testing.T) {
	e := newAcctEnv(t)
	h := useHostSeam(t, e)
	o := seamInstance(t, e, "u1", "u2")
	if err := o.Restart(); err != nil {
		t.Fatalf("Restart of a running instance: %v", err)
	}

	h.waitSpawned(t, 1)
	o.Shutdown()
	before := len(h.spawned(t))
	expectStoppedAndClean(t, "restarts after a real shutdown", e, h, o)
	if after := len(h.spawned(t)); after != before {
		t.Fatalf("a refused restart spawned xl2tpd (%d -> %d)", before, after)
	}
}

func seamWorkers(stop <-chan struct{}, fns ...func(i int)) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
	return &wg
}

func TestL2TPSeamShutdownRacingUserSyncsAndRestartsLeavesNothingBehind(t *testing.T) {
	e := newAcctEnv(t)
	h := useHostSeam(t, e)
	o := seamInstance(t, e, "u1")
	if err := o.Restart(); err != nil {
		t.Fatalf("Restart of a running instance: %v", err)
	}
	ctx := context.Background()
	pool := []string{"u1", "u2", "u3", "u4"}

	var restarted atomic.Int64
	count := func(err error) {
		if err == nil {
			restarted.Add(1)
		}
	}
	stop := make(chan struct{})
	workers := seamWorkers(stop,
		func(i int) { _ = o.SyncUser(ctx, e.users(pool[i%len(pool)])[0]) },
		func(i int) { _ = o.SyncUsers(ctx, e.users(pool[:1+i%len(pool)]...)) },
		func(i int) { _ = o.UpdateUsers(ctx, e.users(pool[i%len(pool)])) },
		func(i int) { count(o.UpdateUsersAndRestart(ctx, e.users(pool[i%len(pool)]))) },
		func(int) { count(o.Restart()) },
	)

	time.Sleep(time.Duration(20+time.Now().UnixNano()%20) * time.Millisecond)
	o.Shutdown()
	time.Sleep(15 * time.Millisecond)
	close(stop)
	workers.Wait()

	expectStoppedAndClean(t, "after user syncs and restarts raced a real shutdown", e, h, o)
	if restarted.Load() == 0 {
		t.Fatal("no Restart of the running instance succeeded before the shutdown")
	}
}

func TestL2TPSeamRestartsOverlappingARealShutdownAreSerialised(t *testing.T) {
	e := newAcctEnv(t)
	h := useHostSeam(t, e)
	o := seamInstance(t, e, "u1")
	ctx := context.Background()

	var restarted atomic.Int64
	stop := make(chan struct{})
	restart := func(int) {
		if o.Restart() == nil {
			restarted.Add(1)
		}
	}
	workers := seamWorkers(stop, restart, restart, restart, func(int) { _ = o.UpdateUsers(ctx, e.users("u1")) })

	time.Sleep(30 * time.Millisecond)
	o.Shutdown()
	time.Sleep(30 * time.Millisecond)
	close(stop)
	workers.Wait()

	expectStoppedAndClean(t, "after restarts overlapped a real shutdown", e, h, o)
	if restarted.Load() == 0 {
		t.Fatal("no Restart of the running instance succeeded before the shutdown")
	}
}
