package l2tp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const fallbackBoot = "5a4b3c2d-1e0f-4a1b-9c8d-7e6f5a4b3c2d"

func (e *acctEnv) bootAt(boot string, centis int64) {
	e.t.Helper()
	e.write(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"), boot+"\n")
	e.write(filepath.Join(procRoot, "uptime"), fmt.Sprintf("%d.%02d 0.00\n", centis/100, centis%100))
}

func (e *acctEnv) sessionWithMono(ifname, username string, pid int, started int64, mono string, rx, tx int64) {
	e.t.Helper()
	content := fmt.Sprintf("user=%s\ntunnel_ip=127.0.0.1\nclient=\ntag=%s\npid=%d\nstarted=%d\nmono=%s\n", username, e.tag, pid, started, mono)
	e.write(filepath.Join(sessionStateDir, ifname), content)
	e.counters(ifname, rx, tx)
}

func (e *acctEnv) finalWithMono(ifname, username string, pid int, started int64, mono string, rx, tx int64) {
	e.t.Helper()
	content := fmt.Sprintf("user=%s\ntag=%s\nifname=%s\npid=%d\nstarted=%d\nmono=%s\nrx=%d\ntx=%d\n", username, e.tag, ifname, pid, started, mono, rx, tx)
	e.write(e.finalName(ifname), content)
}

func openFallbackWindow(t *testing.T, e *acctEnv) *bool {
	t.Helper()
	pinWallClock(t, fallbackLoad)
	e.bootAt(fallbackBoot, 500000)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)
	return &blocked
}

func TestSessionStartedBeforeTheFallbackUnderAClockThatWasAheadIsTakenAsCounted(t *testing.T) {
	e := newAcctEnv(t)
	e.sessionWithMono("ppp0", "7", 100, fallbackLoad+600, fallbackBoot+":400000", 4000, 1000)
	blocked := openFallbackWindow(t, e)
	a := e.instance("7")
	a.poll()
	*blocked = false
	a.poll()
	expectUsers(t, "a session present at the fallback whose start the clock put after it", takeUsers(t, a), map[string]int64{})
	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "its growth", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestSessionStartedAfterTheFallbackUnderAClockSteppedBackCountsFromZero(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	a := e.instance("8")
	a.poll()
	e.sessionWithMono("ppp1", "8", 200, fallbackLoad-600, fallbackBoot+":600000", 70, 30)
	*blocked = false
	a.poll()
	expectUsers(t, "a session started after the fallback whose start the clock put before it", takeUsers(t, a), map[string]int64{"8": 100})
}

func TestFinalOfASessionStartedBeforeTheFallbackUnderAClockThatWasAheadIsTakenAsCounted(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	blockListing(t, func() string { return sessionFinalDir }, blocked)
	e.finalWithMono("ppp0", "7", 100, fallbackLoad+600, fallbackBoot+":400000", 4000, 1000)
	a := e.instance("7")
	a.poll()
	*blocked = false
	a.poll()
	expectUsers(t, "a final present at the fallback whose start the clock put after it", takeUsers(t, a), map[string]int64{})
}

func TestFallbackKeepsItsBootTimeAcrossARestart(t *testing.T) {
	e := newAcctEnv(t)
	e.sessionWithMono("ppp0", "7", 100, fallbackLoad+600, fallbackBoot+":400000", 4000, 1000)
	blocked := openFallbackWindow(t, e)
	a := e.instance("7")
	a.poll()
	pinWallClock(t, fallbackLoad-3600)
	e.bootAt(fallbackBoot, 700000)
	b := e.instance("7")
	*blocked = false
	b.poll()
	expectUsers(t, "a session present at the fallback, after a restart under another clock", takeUsers(t, b), map[string]int64{})
}

func (e *acctEnv) failCounters(ifname string) func() {
	e.t.Helper()
	rx := filepath.Join(sysClassNet, ifname, "statistics", "rx_bytes")
	saved, err := os.ReadFile(rx)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.Remove(rx); err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(rx, 0o700); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		if err := os.RemoveAll(rx); err != nil {
			e.t.Fatal(err)
		}
		e.write(rx, string(saved))
	}
}

func TestFallbackNeverTakesACounterThatCannotBeReadAsZero(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	e.write(e.countedPath(), "not a counted state\n")
	restore := e.failCounters("ppp0")
	a := e.instance("7")
	a.poll()
	restore()
	a.poll()
	expectUsers(t, "a session present at the fallback whose counters could not be read at first", takeUsers(t, a), map[string]int64{})
	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "its growth", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestFallbackWindowNeverTakesACounterThatCannotBeReadAsZero(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	blocked := openFallbackWindow(t, e)
	a := e.instance("7")
	a.poll()
	restore := e.failCounters("ppp0")
	*blocked = false
	a.poll()
	restore()
	a.poll()
	expectUsers(t, "a session first listed while its counters could not be read", takeUsers(t, a), map[string]int64{})
}

func TestCounterThatCannotBeReadIsNotCountedAndLosesNothing(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})
	e.counters("ppp0", 4100, 1050)
	restore := e.failCounters("ppp0")
	a.poll()
	expectUsers(t, "while a counter cannot be read", takeUsers(t, a), map[string]int64{})
	restore()
	a.poll()
	expectUsers(t, "once it can", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestScriptsRecordTheBootTimeOfASessionAndKeepItAcrossARenegotiation(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.advance(30)
	h.ipUp(p)
	first, ok := h.session(p.ifname)
	want := parseMono(h.boot + ":" + strconv.FormatInt(h.uptime, 10))
	if !ok || first.mono != want {
		t.Fatalf("ip-up recorded boot time %+v, want %+v", first.mono, want)
	}
	h.advance(600)
	h.stepClock(-3600)
	h.ipDown(p)
	h.ipUp(p)
	again, _ := h.session(p.ifname)
	if again.mono != first.mono || again.identity() != first.identity() {
		t.Fatalf("a renegotiation changed the boot time or identity: %+v -> %+v", first, again)
	}
	scan := scanFinalRecords(e.tag)
	if len(scan.records) != 1 || scan.records[0].mono != first.mono {
		t.Fatalf("ip-down did not copy the boot time into the final record: %+v", scan.records)
	}
	a.poll()
}

func TestFallbackWindowAcrossTwoRebootsBillsWhatStartedInTheMiddleBoot(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	a := e.instance("8")
	a.poll()
	*blocked = false

	middle := "6b5c4d3e-2f10-4b2c-8d9e-8f7a6b5c4d3e"
	e.bootAt(middle, 1500)
	b := e.instance("8")
	b.poll()
	unreadable := true
	blockFinalRecords(t, func(string) bool { return unreadable })
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middle+":3000", 70, 30)
	b.poll()

	e.bootAt("7c6d5e4f-3021-4c3d-9eaf-9a8b7c6d5e4f", 1500)
	c := e.instance("8")
	unreadable = false
	c.poll()
	expectUsers(t, "the final of a session of a boot between the fallback and now", takeUsers(t, c), map[string]int64{"8": 100})
}
