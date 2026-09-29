package l2tp

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func blockListing(t *testing.T, dir func() string, blocked *bool) {
	t.Helper()
	prev := listDir
	t.Cleanup(func() { listDir = prev })
	listDir = func(name string) ([]os.DirEntry, error) {
		if *blocked && name == dir() {
			return nil, &fs.PathError{Op: "open", Path: name, Err: syscall.EMFILE}
		}
		return prev(name)
	}
}

func TestSessionFileThatCannotBeReadForTheWholeGraceIsNotBilledAgain(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})

	blocked := true
	prev := readSessionFile
	t.Cleanup(func() { readSessionFile = prev })
	readSessionFile = func(path string) ([]byte, error) {
		if blocked && filepath.Base(path) == "ppp0" {
			return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}
		}
		return prev(path)
	}
	drainLogs(a)
	for i := 0; i <= countedGraceRounds; i++ {
		a.poll()
	}
	if logs := drainLogs(a); len(logs) != 1 || !strings.Contains(logs[0], "ppp0") {
		t.Fatalf("a session file that cannot be read must log once per streak, got %v", logs)
	}
	blocked = false
	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "the same live session once its file can be read again", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestSessionDirectoryThatCannotBeListedLogsOncePerStreak(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)
	drainLogs(a)
	for i := 0; i < 5; i++ {
		a.poll()
	}
	if logs := drainLogs(a); len(logs) != 1 {
		t.Fatalf("a session directory that cannot be listed must log once per streak, got %v", logs)
	}
	blocked = false
	a.poll()
	blocked = true
	a.poll()
	if logs := drainLogs(a); len(logs) != 1 {
		t.Fatalf("a new streak must log once more, got %v", logs)
	}
}

func TestFinalRecordDirectoryThatCannotBeListedForTheWholeGraceIsNotBilledAgain(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})

	e.end("ppp0", "7", 100, 1000, 4100, 1050)
	blocked := true
	blockListing(t, func() string { return sessionFinalDir }, &blocked)
	drainLogs(a)
	for i := 0; i <= countedGraceRounds; i++ {
		a.poll()
	}
	if logs := drainLogs(a); len(logs) != 1 {
		t.Fatalf("a final-record directory that cannot be listed must log once per streak, got %v", logs)
	}
	blocked = false
	a.poll()
	expectUsers(t, "the final record once its directory can be listed again", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestUnreadableCountedStateWhileSessionsCannotBeListedCountsThemWhenTheyCanBe(t *testing.T) {
	for _, dir := range []string{"sessions", "final records"} {
		t.Run(dir, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 4000, 1000)
			e.final("ppp1", "8", 200, 1100, 300, 100)
			e.write(e.countedPath(), "not a counted state\n")
			blocked := true
			blockListing(t, func() string {
				if dir == "sessions" {
					return sessionStateDir
				}
				return sessionFinalDir
			}, &blocked)

			a := e.instance("7", "8")
			a.poll()
			a.poll()
			blocked = false
			a.poll()
			expectUsers(t, "what was present when the counted state was unreadable", takeUsers(t, a), map[string]int64{})

			e.counters("ppp0", 4100, 1050)
			a.poll()
			expectUsers(t, "later growth", takeUsers(t, a), map[string]int64{"7": 150})

			b := e.instance("7", "8")
			b.poll()
			expectUsers(t, "after a process restart", takeUsers(t, b), map[string]int64{})
		})
	}
}

func TestUnreadableCountedStateWhileFinalRecordsCannotBeListedStillBillsLiveGrowth(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	e.final("ppp1", "8", 200, 1100, 300, 100)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionFinalDir }, &blocked)

	a := e.instance("7", "8")
	a.poll()
	expectUsers(t, "what was present when the counted state was unreadable", takeUsers(t, a), map[string]int64{})
	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "growth of a session seen while the final records cannot be listed", takeUsers(t, a), map[string]int64{"7": 150})

	e.session("ppp2", "8", 300, 1200, 70, 30)
	a.poll()
	blocked = false
	a.poll()
	expectUsers(t, "a session that started after the start and the final present at the start", takeUsers(t, a), map[string]int64{"8": 100})
}
