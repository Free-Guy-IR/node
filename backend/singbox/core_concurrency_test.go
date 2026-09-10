//go:build linux

package singbox

import (
	"fmt"
	"github.com/pasarguard/node/backend"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func stubScript(body string) string {
	return fmt.Sprintf("#!/bin/sh\ncase \"$1\" in version) echo \"sing-box version 1.0.0-stub\"; exit 0;; esac\n%s\n", body)
}

func stubCoreWith(t *testing.T, body string) *Core {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "stub-sing-box")
	if err := os.WriteFile(exe, []byte(stubScript(body)), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
	core, err := NewCore(exe, dir, 16, 16)
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	return core
}

func stubConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := NewConfig(`{"log":{"level":"error"},"inbounds":[],"outbounds":[{"type":"direct"}]}`)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	return cfg
}

func TestCore_StubBinaryAnswersVersionProbe(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	if core.Version() != "1.0.0-stub" {
		t.Fatalf("stub binary must answer the version probe, got %q", core.Version())
	}
}

func TestCore_ConcurrentStartStopStartedIsRaceFree(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	cfg := stubConfig(t)
	t.Cleanup(core.Stop)

	done := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = core.Started()
					_ = core.Stopping()
					_ = core.Version()
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = core.Start(cfg)
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				core.Stop()
				time.Sleep(7 * time.Millisecond)
			}
		}
	}()

	time.Sleep(800 * time.Millisecond)
	close(done)
	wg.Wait()
}

func TestCore_StartedFlipsFalseOnceTheProcessExits(t *testing.T) {
	core := stubCoreWith(t, "exit 0")
	t.Cleanup(core.Stop)

	if err := core.Start(stubConfig(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for core.Started() {
		if time.Now().After(deadline) {
			t.Fatal("Started() still reports true long after the process exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCore_VersionProbeCannotHangForever(t *testing.T) {
	original := versionProbeTimeout
	versionProbeTimeout = 250 * time.Millisecond
	t.Cleanup(func() { versionProbeTimeout = original })

	dir := t.TempDir()
	exe := filepath.Join(dir, "stub-sing-box")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := NewCore(exe, dir, 16, 16); err != nil {
			t.Errorf("NewCore: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(versionProbeTimeout + 5*time.Second):
		t.Fatalf("NewCore blocked past the %s version-probe timeout on a hanging binary", versionProbeTimeout)
	}
}

func TestCore_StartedDoesNotBlockWhileTheCoreLockIsHeld(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	t.Cleanup(core.Stop)

	if err := core.Start(stubConfig(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	core.mu.Lock()
	answered := make(chan bool, 1)
	go func() { answered <- core.Started() }()

	select {
	case got := <-answered:
		core.mu.Unlock()
		if !got {
			t.Fatal("Started() must still see the running process while another goroutine holds the core lock")
		}
	case <-time.After(3 * time.Second):
		core.mu.Unlock()
		t.Fatal("Started() blocked on the core lock; the request hot path is no longer lock-free")
	}
}

func TestCore_StartedGoesFalseWhileTheCoreLockIsHeldDuringStop(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	cfg := stubConfig(t)

	if err := core.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !core.Started() {
		t.Fatal("Started() must be true right after a successful Start")
	}

	stopped := make(chan struct{})
	go func() {
		core.Stop()
		close(stopped)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for core.Started() {
		if time.Now().After(deadline) {
			t.Fatal("Started() never went false while Stop() was running")
		}
		time.Sleep(2 * time.Millisecond)
	}

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop() did not return")
	}
}

func TestWrapperStartedAnswersWhileShutdownHoldsTheWrapperLock(t *testing.T) {
	sb := &SingBox{core: stubCoreWith(t, "exec sleep 30")}
	t.Cleanup(sb.core.Stop)

	if err := sb.core.Start(stubConfig(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sb.mu.Lock()
	answered := make(chan bool, 1)
	go func() { answered <- sb.Started() }()

	select {
	case got := <-answered:
		sb.mu.Unlock()
		if !got {
			t.Fatal("the wrapper must report the running core while the wrapper lock is held")
		}
	case <-time.After(3 * time.Second):
		sb.mu.Unlock()
		t.Fatal("SingBox.Started() blocked on the wrapper lock; Shutdown would stall every request again")
	}
}

func TestVerifyProcessDeadIgnoresARecycledPid(t *testing.T) {
	self := os.Getpid()

	actual, ok := processStartTime(self)
	if !ok {
		t.Skip("cannot read the start time of this process")
	}

	if err := verifyProcessDead(self, actual); err == nil {
		t.Fatal("a live process with a matching start time must be reported as alive")
	}

	if err := verifyProcessDead(self, actual+1); err != nil {
		t.Fatalf("a live pid whose start time does not match is a different process and must count as dead, got %v", err)
	}

	if err := verifyProcessDead(self, 0); err == nil {
		t.Fatal("a zero start time must fall back to the old pid-only behaviour")
	}
}

func TestProcessStartTimeIsStableAndPerProcess(t *testing.T) {
	self := os.Getpid()
	a, ok := processStartTime(self)
	if !ok {
		t.Skip("cannot read the start time of this process")
	}
	b, _ := processStartTime(self)
	if a != b {
		t.Fatalf("start time must not change between reads: %d then %d", a, b)
	}
	if _, ok := processStartTime(1 << 30); ok {
		t.Fatal("a pid that cannot exist must not yield a start time")
	}
}

func TestCanKillByPidRequiresProvenIdentity(t *testing.T) {
	self := os.Getpid()
	actual, ok := processStartTime(self)
	if !ok {
		t.Skip("cannot read the start time of this process")
	}

	if !canKillByPid(self, actual) {
		t.Fatal("a pid whose start time matches is ours and must be killable")
	}
	if canKillByPid(self, actual+1) {
		t.Fatal("a recycled pid must never be killed by pid")
	}
	if canKillByPid(self, 0) {
		t.Fatal("an unknown start time must fail closed, not fall back to killing by pid")
	}
	if canKillByPid(1<<30, actual) {
		t.Fatal("a pid that cannot exist must not be killable")
	}
}

func TestStagedConfigIsOnlyCommittedByTheStartThatStagedIt(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")

	first, err := core.stageConfigFile([]byte(`{"first":true}`))
	if err != nil {
		t.Fatalf("stage first: %v", err)
	}
	second, err := core.stageConfigFile([]byte(`{"second":true}`))
	if err != nil {
		t.Fatalf("stage second: %v", err)
	}
	if first == second {
		t.Fatal("two concurrent stages must not share a temporary file")
	}

	if err := core.commitConfigFileLocked(second); err != nil {
		t.Fatalf("commit second: %v", err)
	}
	got, err := os.ReadFile(core.configFilePath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != `{"second":true}` {
		t.Fatalf("the committed config must be the one that was committed, got %s", got)
	}

	if err := core.commitConfigFileLocked(first); err != nil {
		t.Fatalf("commit first: %v", err)
	}
	got, err = os.ReadFile(core.configFilePath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != `{"first":true}` {
		t.Fatalf("each commit must replace the file wholesale, got %s", got)
	}
}

func TestConcurrentStartsLaunchWithTheWinnersConfig(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	t.Cleanup(core.Stop)

	first, err := NewConfig(`{"log":{"level":"error"},"inbounds":[],"outbounds":[{"type":"direct"}]}`)
	if err != nil {
		t.Fatalf("NewConfig first: %v", err)
	}
	second, err := NewConfig(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[{"type":"block"}]}`)
	if err != nil {
		t.Fatalf("NewConfig second: %v", err)
	}

	type attempt struct {
		cfg *Config
		err error
	}
	results := make([]attempt, 2)
	var wg sync.WaitGroup
	for i, cfg := range []*Config{first, second} {
		wg.Add(1)
		go func(i int, cfg *Config) {
			defer wg.Done()
			results[i] = attempt{cfg: cfg, err: core.Start(cfg)}
		}(i, cfg)
	}
	wg.Wait()

	winners := 0
	var winner *Config
	for _, r := range results {
		if r.err == nil {
			winners++
			winner = r.cfg
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one concurrent Start must succeed, got %d", winners)
	}

	want, err := winner.ToBytes()
	if err != nil {
		t.Fatalf("winner ToBytes: %v", err)
	}
	got, err := os.ReadFile(core.configFilePath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("the running process must have launched with the config of the Start that won:\n on disk: %s\n winner : %s", got, want)
	}

	leftovers, _ := filepath.Glob(filepath.Join(core.configDir, "singbox-*.json"))
	if len(leftovers) != 0 {
		t.Fatalf("staged temp files must not be left behind, found %v", leftovers)
	}
}

func reapMarkerChild(t *testing.T, marker string) {
	t.Helper()
	t.Cleanup(func() {
		raw, err := os.ReadFile(marker)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 1 {
			return
		}
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
}

func TestReapProbeNeverKillsItsOwnProcessGroup(t *testing.T) {
	marker := make(chan int, 1)
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & echo started; wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper without ConfigureProbe: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case pid := <-marker:
			if pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		default:
		}
	})

	self := os.Getpid()
	selfPgid, err := syscall.Getpgid(self)
	if err != nil {
		t.Fatalf("getpgid(self): %v", err)
	}

	backend.ReapProbe(cmd)

	if err := syscall.Kill(self, 0); err != nil {
		t.Fatalf("ReapProbe signalled our own process: %v", err)
	}
	if cur, err := syscall.Getpgid(self); err != nil || cur != selfPgid {
		t.Fatalf("our process group changed or died after ReapProbe (was %d now %d err %v)", selfPgid, cur, err)
	}
}

func assertProbeReapsGrandchild(t *testing.T, wrapperBody string) {
	t.Helper()
	original := versionProbeTimeout
	versionProbeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { versionProbeTimeout = original })

	dir := t.TempDir()
	exe := filepath.Join(dir, "stub-sing-box")
	marker := filepath.Join(dir, "grandchild.pid")
	script := "#!/bin/sh\nsleep 600 &\necho $! > " + marker + "\n" + wrapperBody + "\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
	reapMarkerChild(t, marker)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		if _, err := NewCore(exe, dir, 16, 16); err != nil {
			t.Errorf("NewCore: %v", err)
		}
	}()

	select {
	case <-done:
		t.Logf("NewCore returned after %s", time.Since(start))
	case <-time.After(versionProbeTimeout + backend.ProbeWaitDelay + 5*time.Second):
		t.Fatal("NewCore blocked past the probe timeout: a forked grandchild still holds stdout")
	}

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Skip("the stub never recorded a grandchild pid")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Skipf("unreadable grandchild pid %q", raw)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if !isProcessRunning(pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the probe left grandchild %d running; the whole process group must be killed", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestVersionProbeSurvivesABinaryThatForksAndHoldsStdout(t *testing.T) {
	assertProbeReapsGrandchild(t, "wait")
}

func TestVersionProbeReapsAGrandchildWhenTheWrapperExitsEarly(t *testing.T) {
	assertProbeReapsGrandchild(t, "exit 0")
}
