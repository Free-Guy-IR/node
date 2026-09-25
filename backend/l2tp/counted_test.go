package l2tp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

var acctTagSeq atomic.Int64

type acctEnv struct {
	t      *testing.T
	tag    string
	root   string
	finals int
}

func newAcctEnv(t *testing.T) *acctEnv {
	t.Helper()
	useTempSessionDirs(t)
	prevNet, prevCounted, prevProc, prevRecords := sysClassNet, sessionCountedDir, procRoot, pppdRecordDir
	root := t.TempDir()
	sysClassNet = filepath.Join(root, "net")
	sessionCountedDir = filepath.Join(root, "counted")
	procRoot = filepath.Join(root, "proc")
	pppdRecordDir = filepath.Join(root, "pppd")
	t.Cleanup(func() {
		sysClassNet, sessionCountedDir, procRoot, pppdRecordDir = prevNet, prevCounted, prevProc, prevRecords
	})
	return &acctEnv{t: t, tag: fmt.Sprintf("acct-%d", acctTagSeq.Add(1)), root: root}
}

func (e *acctEnv) nodeConfig() *config.Config {
	return &config.Config{
		GeneratedConfigPath:        filepath.Join(e.root, "generated"),
		LogBufferSize:              256,
		StatsUpdateIntervalSeconds: 10,
	}
}

func (e *acctEnv) write(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *acctEnv) counters(ifname string, rx, tx int64) {
	e.t.Helper()
	dir := filepath.Join(sysClassNet, ifname, "statistics")
	e.write(filepath.Join(dir, "rx_bytes"), fmt.Sprintf("%d\n", rx))
	e.write(filepath.Join(dir, "tx_bytes"), fmt.Sprintf("%d\n", tx))
}

func (e *acctEnv) session(ifname, username string, pid int, started, rx, tx int64) {
	e.t.Helper()
	content := fmt.Sprintf("user=%s\ntunnel_ip=127.0.0.1\nclient=\ntag=%s\npid=%d\nstarted=%d\n", username, e.tag, pid, started)
	e.write(filepath.Join(sessionStateDir, ifname), content)
	e.counters(ifname, rx, tx)
}

func (e *acctEnv) removeSession(ifname string) {
	e.t.Helper()
	if err := os.Remove(filepath.Join(sessionStateDir, ifname)); err != nil {
		e.t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(sysClassNet, ifname)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *acctEnv) finalName(ifname string) string {
	e.finals++
	return filepath.Join(sessionFinalDir, fmt.Sprintf("%s.%d.%d", ifname, 30000+e.finals, 1700000000+e.finals))
}

func (e *acctEnv) final(ifname, username string, pid int, started, rx, tx int64) {
	e.t.Helper()
	content := fmt.Sprintf("user=%s\ntag=%s\nifname=%s\npid=%d\nstarted=%d\nrx=%d\ntx=%d\n", username, e.tag, ifname, pid, started, rx, tx)
	e.write(e.finalName(ifname), content)
}

func (e *acctEnv) oldFinal(ifname, username string, rx, tx int64) {
	e.t.Helper()
	content := fmt.Sprintf("user=%s\ntag=%s\nifname=%s\nrx=%d\ntx=%d\n", username, e.tag, ifname, rx, tx)
	e.write(e.finalName(ifname), content)
}

func (e *acctEnv) end(ifname, username string, pid int, started, rx, tx int64) {
	e.t.Helper()
	e.final(ifname, username, pid, started, rx, tx)
	e.removeSession(ifname)
}

func (e *acctEnv) countedPath() string {
	return filepath.Join(sessionCountedDir, e.tag)
}

func (e *acctEnv) users(usernames ...string) []*common.User {
	out := make([]*common.User, 0, len(usernames))
	for _, u := range usernames {
		out = append(out, user(u, "pw-"+u, e.tag))
	}
	return out
}

func (e *acctEnv) prepare(usernames ...string) *L2TP {
	o := newInstance(&Config{InboundTag: e.tag}, e.nodeConfig())
	o.users.replaceAll(e.users(usernames...))
	return o
}

func (e *acctEnv) instance(usernames ...string) *L2TP {
	e.t.Helper()
	o := e.prepare(usernames...)
	if err := o.startWithAccounting(func() error {
		o.state = lifecycleRunning
		return nil
	}); err != nil {
		e.t.Fatalf("start: %v", err)
	}
	return o
}

func (e *acctEnv) restart(o *L2TP, handOver bool, usernames ...string) *L2TP {
	e.t.Helper()
	if handOver {
		stopAccounting(o)
	}
	return e.instance(usernames...)
}

func stopAccounting(o *L2TP) {
	o.mu.Lock()
	o.state = lifecycleStopped
	o.handOverLocked()
	o.mu.Unlock()
}

func takeUsers(t *testing.T, o *L2TP) map[string]int64 {
	t.Helper()
	resp, err := o.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if err != nil {
		t.Fatalf("UsersStat: %v", err)
	}
	out := make(map[string]int64)
	for _, s := range resp.GetStats() {
		out[s.GetName()] += s.GetValue()
	}
	return out
}

func takeNode(t *testing.T, o *L2TP, typ common.StatType) int64 {
	t.Helper()
	resp, err := o.GetStats(context.Background(), &common.StatRequest{Type: typ, Reset_: true})
	if err != nil {
		t.Fatalf("GetStats(%v): %v", typ, err)
	}
	var total int64
	for _, s := range resp.GetStats() {
		total += s.GetValue()
	}
	return total
}

func expectUsers(t *testing.T, step string, got, want map[string]int64) {
	t.Helper()
	keys := make(map[string]struct{})
	for k := range got {
		keys[k] = struct{}{}
	}
	for k := range want {
		keys[k] = struct{}{}
	}
	for k := range keys {
		if got[k] != want[k] {
			t.Fatalf("%s: user %s billed %d bytes, want %d (all: %v)", step, k, got[k], want[k], got)
		}
	}
}

func drainLogs(o *L2TP) []string {
	var lines []string
	for {
		select {
		case line := <-o.logChan:
			lines = append(lines, line)
		default:
			return lines
		}
	}
}

func readCountedLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("counted state not readable: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 0 || lines[0] != "v2" {
		t.Fatalf("counted state must start with v2, got %q", string(b))
	}
	body := append([]string(nil), lines[1:]...)
	sort.Strings(body)
	return body
}

func forBothRestarts(t *testing.T, run func(t *testing.T, handOver bool)) {
	t.Run("start hand-over", func(t *testing.T) { run(t, true) })
	t.Run("process restart", func(t *testing.T) { run(t, false) })
}

func TestCountedLiveSessionIsNotRecountedAfterARestart(t *testing.T) {
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500})

		e.counters("ppp0", 1300, 700)
		b := e.restart(a, handOver, "7")
		b.poll()
		expectUsers(t, "after restart", takeUsers(t, b), map[string]int64{"7": 500})
	})
}

func TestCountedFinalRecordAfterTheLastPollIsBilledForItsTailOnce(t *testing.T) {
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500})

		if handOver {
			stopAccounting(a)
		}
		e.end("ppp0", "7", 100, 1000, 1300, 700)
		b := e.instance("7")
		b.poll()
		expectUsers(t, "tail after restart", takeUsers(t, b), map[string]int64{"7": 500})
		b.poll()
		expectUsers(t, "second poll", takeUsers(t, b), map[string]int64{})
	})
}

func TestCountedNeverSeenSessionAndFinalAreBilledWholeOnce(t *testing.T) {
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		e.final("ppp1", "8", 200, 1100, 300, 100)
		a := e.instance("7", "8")
		if logs := drainLogs(a); len(logs) != 0 {
			t.Fatalf("a missing counted state must not log, got %v", logs)
		}
		a.poll()
		expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500, "8": 400})
		a.poll()
		expectUsers(t, "second poll", takeUsers(t, a), map[string]int64{})

		b := e.restart(a, handOver, "7", "8")
		b.poll()
		expectUsers(t, "after restart", takeUsers(t, b), map[string]int64{})
	})
}

func TestCountedFinalAndStillListedSessionInOnePollCountOnce(t *testing.T) {
	t.Run("session counted before", func(t *testing.T) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

		e.counters("ppp0", 1200, 600)
		e.final("ppp0", "7", 100, 1000, 1200, 600)
		a.poll()
		expectUsers(t, "final and live session together", takeUsers(t, a), map[string]int64{"7": 300})

		e.removeSession("ppp0")
		a.poll()
		expectUsers(t, "after the session file is gone", takeUsers(t, a), map[string]int64{})
	})
	t.Run("session never counted", func(t *testing.T) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1200, 600)
		e.final("ppp0", "7", 100, 1000, 1200, 600)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "final and live session together", takeUsers(t, a), map[string]int64{"7": 1800})

		e.removeSession("ppp0")
		a.poll()
		expectUsers(t, "after the session file is gone", takeUsers(t, a), map[string]int64{})
	})
}

func TestCountedReusedInterfaceNameStartsFromZero(t *testing.T) {
	t.Run("same instance", func(t *testing.T) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 5000, 5000)
		a := e.instance("7", "8")
		a.poll()
		expectUsers(t, "first session", takeUsers(t, a), map[string]int64{"7": 10000})

		e.session("ppp0", "8", 200, 2000, 400, 100)
		a.poll()
		expectUsers(t, "new session on the reused name", takeUsers(t, a), map[string]int64{"8": 500})

		e.counters("ppp0", 600, 200)
		a.poll()
		expectUsers(t, "growth of the new session", takeUsers(t, a), map[string]int64{"8": 300})
	})
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 5000, 5000)
		a := e.instance("7", "8")
		a.poll()
		expectUsers(t, "first session", takeUsers(t, a), map[string]int64{"7": 10000})

		b := e.restart(a, handOver, "7", "8")
		e.session("ppp0", "8", 200, 2000, 400, 100)
		b.poll()
		expectUsers(t, "new session on the reused name", takeUsers(t, b), map[string]int64{"8": 500})
	})
}

func TestCountedStateSurvivesAProcessRestart(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	e.session("ppp1", "8", 200, 1100, 2000, 1000)
	a := e.instance("7", "8")
	a.poll()
	expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500, "8": 3000})

	e.counters("ppp0", 1100, 600)
	e.end("ppp1", "8", 200, 1100, 2500, 1200)
	b := e.instance("7", "8")
	b.poll()
	expectUsers(t, "after a process restart", takeUsers(t, b), map[string]int64{"7": 200, "8": 700})

	got := readCountedLines(t, e.countedPath())
	want := []string{"consumed ppp1 200 1100 2500 1200", "ppp0 100 1000 1100 600"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("counted state lines = %q, want %q", got, want)
	}
	fi, err := os.Stat(e.countedPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("counted state mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(sessionCountedDir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("counted state directory mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestUnreadableCountedStateCountsOnlyLaterGrowth(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(e *acctEnv)
	}{
		{"garbage", func(e *acctEnv) { e.write(e.countedPath(), "not a counted state\n") }},
		{"wrong version", func(e *acctEnv) { e.write(e.countedPath(), "v1\nppp0 100 1000 1 1\n") }},
		{"malformed line", func(e *acctEnv) { e.write(e.countedPath(), "v2\nppp0 100 1000 x 500\n") }},
		{"malformed consumed line", func(e *acctEnv) { e.write(e.countedPath(), "v2\nconsumed ppp0 100\n") }},
		{"malformed final name", func(e *acctEnv) { e.write(e.countedPath(), "v2\nfinal \n") }},
		{"short line", func(e *acctEnv) { e.write(e.countedPath(), "v2\nppp0 100 1000\n") }},
		{"empty", func(e *acctEnv) { e.write(e.countedPath(), "") }},
		{"not a file", func(e *acctEnv) {
			if err := os.MkdirAll(e.countedPath(), 0o700); err != nil {
				e.t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			e.final("ppp1", "8", 200, 1100, 300, 100)
			c.corrupt(e)

			o := e.prepare("7", "8")
			if err := o.startWithAccounting(func() error {
				o.state = lifecycleRunning
				return nil
			}); err != nil {
				t.Fatalf("an unreadable counted state must never fail the start: %v", err)
			}
			logs := drainLogs(o)

			o.poll()
			expectUsers(t, "first poll", takeUsers(t, o), map[string]int64{})
			if n := len(readFinalRecords(e.tag)); n != 0 {
				t.Fatalf("the final record must be consumed, %d left", n)
			}
			if len(logs) != 1 {
				t.Fatalf("an unreadable counted state must log exactly one line, got %d: %v", len(logs), logs)
			}

			e.counters("ppp0", 1050, 550)
			o.poll()
			expectUsers(t, "later growth", takeUsers(t, o), map[string]int64{"7": 100})
		})
	}
}

func TestCountedStateWriteFailureLogsOncePerStreakAndRetries(t *testing.T) {
	e := newAcctEnv(t)
	blocker := filepath.Join(e.root, "blocker")
	e.write(blocker, "")
	sessionCountedDir = filepath.Join(blocker, "counted")

	o := e.instance("7")
	drainLogs(o)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	o.poll()
	e.counters("ppp0", 1100, 600)
	o.poll()
	expectUsers(t, "billing during a write failure", takeUsers(t, o), map[string]int64{"7": 1700})
	if logs := drainLogs(o); len(logs) != 1 {
		t.Fatalf("a failure streak must log exactly once, got %d: %v", len(logs), logs)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	e.counters("ppp0", 1200, 700)
	o.poll()
	expectUsers(t, "billing after recovery", takeUsers(t, o), map[string]int64{"7": 200})
	if logs := drainLogs(o); len(logs) != 0 {
		t.Fatalf("a successful write must not log, got %v", logs)
	}
	got := readCountedLines(t, filepath.Join(sessionCountedDir, e.tag))
	if want := "ppp0 100 1000 1200 700"; len(got) != 1 || got[0] != want {
		t.Fatalf("counted state after recovery = %q, want %q", got, want)
	}

	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	e.write(blocker, "")
	e.counters("ppp0", 1300, 800)
	o.poll()
	if logs := drainLogs(o); len(logs) != 1 {
		t.Fatalf("a new failure streak must log once, got %d: %v", len(logs), logs)
	}
}

func TestOldFormatFinalRecordIsMatchedByInterfaceName(t *testing.T) {
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7", "8")
		a.poll()
		expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500})

		b := e.restart(a, handOver, "7", "8")
		e.removeSession("ppp0")
		e.oldFinal("ppp0", "7", 1200, 600)
		e.oldFinal("ppp3", "8", 40, 2)
		b.poll()
		expectUsers(t, "old-format finals", takeUsers(t, b), map[string]int64{"7": 300, "8": 42})
		b.poll()
		expectUsers(t, "second poll", takeUsers(t, b), map[string]int64{})
	})
}

func TestStartHandsOverCountedButUnreadDeltas(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	takeNode(t, a, common.StatType_Inbounds)
	takeNode(t, a, common.StatType_Outbounds)
	a.poll()
	stopAccounting(a)

	b := e.instance("7")
	expectUsers(t, "unread per-user delta", takeUsers(t, b), map[string]int64{"7": 1500})
	if got := takeNode(t, b, common.StatType_Inbounds); got != 1500 {
		t.Fatalf("unread inbound delta = %d, want 1500", got)
	}
	if got := takeNode(t, b, common.StatType_Outbounds); got != 1500 {
		t.Fatalf("unread outbound delta = %d, want 1500", got)
	}
	b.poll()
	expectUsers(t, "no growth", takeUsers(t, b), map[string]int64{})
}

func TestShutdownCountsFinalRecordsBeforeHandingOver(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500})

	e.end("ppp0", "7", 100, 1000, 1300, 700)
	stopAccounting(a)
	if n := len(readFinalRecords(e.tag)); n != 0 {
		t.Fatalf("the shutdown poll must consume the final record, %d left", n)
	}

	b := e.instance("7")
	expectUsers(t, "tail counted by the shutdown poll", takeUsers(t, b), map[string]int64{"7": 500})
	b.poll()
	expectUsers(t, "second instance", takeUsers(t, b), map[string]int64{})
}

func TestFailedStartPutsTheHandOverBack(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	stopAccounting(a)

	failing := e.prepare("7")
	boom := errors.New("xl2tpd did not start")
	if err := failing.startWithAccounting(func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("start error = %v, want %v", err, boom)
	}

	c := e.instance("7")
	expectUsers(t, "hand-over after a failed start", takeUsers(t, c), map[string]int64{"7": 1500})
	e.counters("ppp0", 1100, 600)
	c.poll()
	expectUsers(t, "growth", takeUsers(t, c), map[string]int64{"7": 200})
}

func TestHandOverForgetsUsersMissingFromTheNewUserList(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	e.session("ppp1", "8", 200, 1100, 2000, 1000)
	a := e.instance("7", "8")
	a.poll()
	e.end("ppp1", "8", 200, 1100, 2400, 1200)
	stopAccounting(a)

	b := e.instance("7")
	online, err := b.GetUserOnlineStats(context.Background(), "8")
	if err != nil {
		t.Fatal(err)
	}
	expectUsers(t, "pending deltas reported once", takeUsers(t, b), map[string]int64{"7": 1500, "8": 3600})
	if online.GetValue() != 0 {
		t.Fatalf("a user missing from the new user list must be offline, got %d", online.GetValue())
	}
	b.mu.RLock()
	_, kept := b.cumRx["8"]
	b.mu.RUnlock()
	if kept {
		t.Fatal("a user missing from the new user list must be forgotten")
	}
	expectUsers(t, "second read", takeUsers(t, b), map[string]int64{})
	b.poll()
	expectUsers(t, "poll after the start", takeUsers(t, b), map[string]int64{})
}

func TestIPDownCopiesTheSessionIdentityIntoTheFinalRecord(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell available")
	}
	e := newAcctEnv(t)
	a := e.instance("7")
	if err := os.MkdirAll(a.config.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.writeIPScripts(); err != nil {
		t.Fatal(err)
	}
	e.session("ppp97", "7", 4242, 1700000123, 1000, 500)
	a.poll()
	expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500})

	cmd := exec.Command(sh, a.ipDownScriptPath())
	cmd.Env = append(os.Environ(), "IFNAME=ppp97", "BYTES_RCVD=1300", "BYTES_SENT=700")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ip-down: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(sessionStateDir, "ppp97")); !os.IsNotExist(err) {
		t.Fatalf("ip-down must remove the session file, stat err = %v", err)
	}
	recs := readFinalRecords(e.tag)
	if len(recs) != 1 {
		t.Fatalf("ip-down wrote %d final records, want 1", len(recs))
	}
	raw, err := os.ReadFile(recs[0].path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for _, want := range []string{"user=7", "ifname=ppp97", "pid=4242", "started=1700000123"} {
		found := false
		for _, l := range lines {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("final record lacks %q:\n%s", want, raw)
		}
	}

	b := e.instance("7")
	b.poll()
	expectUsers(t, "tail from the ip-down record", takeUsers(t, b), map[string]int64{"7": 500})
}

func TestFinalRecordStillBeingWrittenIsLeftAlone(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	partial := filepath.Join(sessionFinalDir, ".ppp0.30001")
	e.write(partial, "user=7\ntag="+e.tag+"\nifname=ppp0\npid=100\n")
	e.counters("ppp0", 1100, 600)
	a.poll()
	expectUsers(t, "growth next to a partial record", takeUsers(t, a), map[string]int64{"7": 200})
	a.poll()
	expectUsers(t, "no growth", takeUsers(t, a), map[string]int64{})
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("a record still being written must be left in place: %v", err)
	}
}

func TestOccupiedHandOverIsReplacedLoudly(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	older := e.instance("7")
	newer := e.instance("7")
	older.poll()
	stopAccounting(older)

	e.counters("ppp0", 1100, 600)
	newer.poll()
	drainLogs(newer)
	stopAccounting(newer)
	if logs := drainLogs(newer); len(logs) != 1 || !strings.Contains(logs[0], "[Error]") {
		t.Fatalf("a deposit on an occupied tag must log one error line, got %v", logs)
	}

	next := e.instance("7")
	expectUsers(t, "the newer deposit replaces the older", takeUsers(t, next), map[string]int64{"7": 1700})
}
