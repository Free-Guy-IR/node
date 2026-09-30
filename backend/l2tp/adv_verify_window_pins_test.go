package l2tp

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func verifyHasSince(t *testing.T, e *acctEnv) bool {
	t.Helper()
	for _, l := range readCountedLines(t, e.countedPath()) {
		if l == fmt.Sprintf("since %d", fallbackLoad) {
			return true
		}
	}
	return false
}

func TestFinalOfASessionStartedInTheSecondOfTheFallbackIsBilledWhole(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("8")
	a.poll()
	e.final("ppp1", "8", 200, fallbackLoad, 90, 40)
	blocked = false
	a.poll()
	expectUsers(t, "the final of a session whose ip-up fell in the second of the fallback", takeUsers(t, a), map[string]int64{"8": 130})
}

func TestFallbackWindowClosesOnlyAfterConsecutiveCleanPolls(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionFinalDir }, &blocked)

	a := e.instance("7")
	blocked = false
	for i := 0; i < countedGraceRounds/2; i++ {
		a.poll()
	}
	blocked = true
	a.poll()
	blocked = false
	for i := 0; i < countedGraceRounds/2+2; i++ {
		a.poll()
	}
	if !verifyHasSince(t, e) {
		t.Fatalf("a failing poll must restart the count of clean polls, got %q", readCountedLines(t, e.countedPath()))
	}
}

func TestFallbackWindowStaysOpenWhileASessionStartedBeforeItCannotBeMeasured(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	verifySessionWithIfindex(e, "ppp0", "7", 100, fallbackLoad-3600, 41, 4000, 1000)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("7")
	a.poll()
	restore := verifyMakeUnreadable(t, filepath.Join(sysClassNet, "ppp0", "ifindex"))
	blocked = false
	for i := 0; i <= countedGraceRounds+1; i++ {
		a.poll()
	}
	restore()
	a.poll()
	expectUsers(t, "a session started before the fallback, first measured after a grace of polls", takeUsers(t, a), map[string]int64{})
	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "its growth after that", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestFinalWithoutIdentityDuringTheFallbackWindowIsTakenAsCounted(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("8")
	a.poll()
	e.oldFinal("ppp1", "8", 90, 40)
	blocked = false
	a.poll()
	expectUsers(t, "a final record without identity while the fallback window is open", takeUsers(t, a), map[string]int64{})
}

func TestFallbackWindowStaysOpenWhileAFinalRecordCannotBeRemoved(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.final("ppp1", "8", 200, fallbackLoad-60, 300, 100)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockFinalRecords(t, func(string) bool { return blocked })
	failRemove := false
	prev := removeFinalRecord
	t.Cleanup(func() { removeFinalRecord = prev })
	removeFinalRecord = func(path string) error {
		if failRemove {
			return errors.New("device or resource busy")
		}
		return prev(path)
	}

	a := e.instance("8")
	a.poll()
	blocked = false
	failRemove = true
	for i := 0; i <= countedGraceRounds+1; i++ {
		a.poll()
	}
	failRemove = false
	a.poll()
	expectUsers(t, "a final record of a session started before the fallback, removable only after a grace of polls", takeUsers(t, a), map[string]int64{})
}
