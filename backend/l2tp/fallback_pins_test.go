package l2tp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingSessionAndFinalDirectoriesAreNothingToList(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	for _, dir := range []string{sessionStateDir, sessionFinalDir} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	e.write(e.countedPath(), "not a counted state\n")
	a := e.instance("7")
	a.poll()
	for _, l := range readCountedLines(t, e.countedPath()) {
		if strings.HasPrefix(l, countedSincePrefix) {
			t.Fatalf("a directory that does not exist has nothing to list and must not open a fallback window, got %q", l)
		}
	}
	for _, line := range drainLogs(a) {
		if strings.Contains(line, "cannot list") {
			t.Fatalf("a directory that does not exist must not be logged as a listing failure: %s", line)
		}
	}
}

func TestLaterBootHoldingAColonMakesTheCountedStateUnreadable(t *testing.T) {
	line := "since 1800000000 " + fallbackBoot + ":500000 " + nextBoot + ":3000"
	if _, err := decodeCountedState([]byte("v2\n" + line + "\n")); err == nil {
		t.Fatalf("a counted state with %q must be unreadable", line)
	}
}

func TestCountersReadWhileTheInterfaceIsReplacedAreNotCounted(t *testing.T) {
	e := newAcctEnv(t)
	verifySessionWithIfindex(e, "ppp0", "7", 100, 1000, 41, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})

	e.counters("ppp0", 4100, 1050)
	prev := readCounterFile
	t.Cleanup(func() { readCounterFile = prev })
	replaced := false
	readCounterFile = func(path string) ([]byte, error) {
		if !replaced {
			replaced = true
			e.write(filepath.Join(sysClassNet, "ppp0", "ifindex"), "42\n")
			e.counters("ppp0", 900000, 900000)
		}
		return prev(path)
	}
	a.poll()
	expectUsers(t, "counters read while another interface took the name", takeUsers(t, a), map[string]int64{})
}
