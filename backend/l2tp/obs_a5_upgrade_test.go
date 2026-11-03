package l2tp

import (
	"os"
	"testing"
)

func TestObsA5UpgradeWithoutCountedFileDoesNotRebillLiveSession(t *testing.T) {
	e := newAcctEnv(t)
	const gib = int64(1) << 30
	e.session("ppp0", "7", 100, 1000, 40*gib, 10*gib)
	e.unmarked()
	if _, err := os.Stat(e.countedPath()); !os.IsNotExist(err) {
		t.Fatalf("counted file must be absent like on a 0.6.20 node, stat err = %v", err)
	}
	b := e.instance("7")
	b.poll()
	got := takeUsers(t, b)
	t.Logf("first poll after start without counted file billed: %v", got)
	expectUsers(t, "first poll after upgrade", got, map[string]int64{"7": 0})
}

func TestObsA5UnreadableCountedFileTakesLiveSessionAsCounted(t *testing.T) {
	e := newAcctEnv(t)
	const gib = int64(1) << 30
	e.session("ppp0", "7", 100, 1000, 40*gib, 10*gib)
	e.write(e.countedPath(), "garbage\n")
	b := e.instance("7")
	b.poll()
	got := takeUsers(t, b)
	t.Logf("first poll after start with unreadable counted file billed: %v", got)
	expectUsers(t, "first poll after corrupt state", got, map[string]int64{"7": 0})
}

func TestObsA5MarkerIsWrittenOnTheFirstSuccessfulSave(t *testing.T) {
	e := newAcctEnv(t)
	e.unmarked()
	b := e.instance("7")
	b.poll()
	if _, err := os.Stat(countedMarkerPath(e.tag)); err != nil {
		t.Fatalf("marker after the first successful save: %v", err)
	}
	if _, err := os.Stat(e.countedPath()); err != nil {
		t.Fatalf("counted file after the first successful save: %v", err)
	}
}
