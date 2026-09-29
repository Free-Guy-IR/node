package l2tp

import (
	"io/fs"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const fallbackLoad = int64(1800000000)

func pinWallClock(t *testing.T, at int64) {
	t.Helper()
	prev := wallClock
	t.Cleanup(func() { wallClock = prev })
	wallClock = func() time.Time { return time.Unix(at, 0) }
}

func TestSessionStartedAfterTheFallbackIsBilledWhileSessionsCannotBeListed(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("7", "8")
	a.poll()
	e.session("ppp1", "8", 200, fallbackLoad+60, 70, 30)
	a.poll()
	blocked = false
	a.poll()
	expectUsers(t, "a session present at the fallback and one started after it", takeUsers(t, a), map[string]int64{"8": 100})
	e.counters("ppp0", 4100, 1050)
	e.counters("ppp1", 170, 80)
	a.poll()
	expectUsers(t, "growth of both", takeUsers(t, a), map[string]int64{"7": 150, "8": 150})
}

func TestFinalOfASessionStartedAfterTheFallbackIsBilledWhole(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)
	blockListing(t, func() string { return sessionFinalDir }, &blocked)

	a := e.instance("8")
	a.poll()
	e.session("ppp1", "8", 200, fallbackLoad+60, 70, 30)
	e.end("ppp1", "8", 200, fallbackLoad+60, 90, 40)
	a.poll()
	blocked = false
	a.poll()
	expectUsers(t, "the final of a session that started after the fallback", takeUsers(t, a), map[string]int64{"8": 130})
}

func TestSessionStartedInTheSecondOfTheFallbackCountsFromZero(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("8")
	a.poll()
	e.session("ppp1", "8", 200, fallbackLoad, 70, 30)
	blocked = false
	a.poll()
	expectUsers(t, "a session whose ip-up fell in the second of the fallback", takeUsers(t, a), map[string]int64{"8": 100})
}

func TestFallbackStateIsKeptAcrossARestartWhileASessionFileCannotBeRead(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	e.session("ppp1", "8", 200, fallbackLoad-3600, 2000, 500)
	e.write(e.countedPath(), "not a counted state\n")
	prev := readSessionFile
	t.Cleanup(func() { readSessionFile = prev })
	readSessionFile = func(path string) ([]byte, error) {
		if filepath.Base(path) == "ppp0" {
			return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}
		}
		return prev(path)
	}

	a := e.instance("7", "8")
	a.poll()
	e.counters("ppp1", 2100, 550)
	a.poll()
	expectUsers(t, "growth while another session file cannot be read", takeUsers(t, a), map[string]int64{"8": 150})

	e.counters("ppp1", 2200, 600)
	b := e.instance("7", "8")
	b.poll()
	expectUsers(t, "growth across a restart while the file still cannot be read", takeUsers(t, b), map[string]int64{"8": 150})

	readSessionFile = prev
	b.poll()
	expectUsers(t, "the session that could not be read is taken as counted when it first can be", takeUsers(t, b), map[string]int64{})
	e.counters("ppp0", 4100, 1050)
	b.poll()
	expectUsers(t, "its growth after that", takeUsers(t, b), map[string]int64{"7": 150})
}

func TestFallbackTimeIsWrittenUntilAGraceOfCleanPolls(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionFinalDir }, &blocked)

	a := e.instance("7")
	want := "since 1800000000"
	hasSince := func() bool {
		for _, l := range readCountedLines(t, e.countedPath()) {
			if l == want {
				return true
			}
		}
		return false
	}
	if !hasSince() {
		t.Fatalf("the fallback must be written at once with %q, got %q", want, readCountedLines(t, e.countedPath()))
	}
	blocked = false
	for i := 0; i < countedGraceRounds; i++ {
		a.poll()
	}
	if !hasSince() {
		t.Fatal("the fallback time must be kept through the grace of clean polls")
	}
	e.counters("ppp0", 4100, 1050)
	a.poll()
	if hasSince() {
		t.Fatalf("the fallback time must be dropped after the grace, got %q", readCountedLines(t, e.countedPath()))
	}
	expectUsers(t, "growth of the session present at the fallback", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestMalformedFallbackTimeMakesTheCountedStateUnreadable(t *testing.T) {
	for _, line := range []string{"since x", "since 0", "since -5", "since 1700000000\nsince 1700000001"} {
		if _, err := decodeCountedState([]byte("v2\n" + line + "\n")); err == nil {
			t.Fatalf("a counted state with %q must be unreadable", line)
		}
	}
	c, err := decodeCountedState([]byte("v2\nppp0 100 1000 1 2\nsince 1700000000\n"))
	if err != nil || c.since != 1700000000 {
		t.Fatalf("decode = %+v, %v", c, err)
	}
}
