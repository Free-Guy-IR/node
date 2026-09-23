package l2tp

import (
	"testing"
)

func (e *acctEnv) killPppd(pid int) {
	sessionIsAlive = func(s l2tpSession) bool { return s.pid != pid }
}

func TestWatermarkOfAReapedSessionWaitsForItsFinalRecord(t *testing.T) {
	t.Run("same instance", func(t *testing.T) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

		e.killPppd(100)
		a.poll()
		a.poll()
		expectUsers(t, "while ip-down is still running", takeUsers(t, a), map[string]int64{})

		e.final("ppp0", "7", 100, 1000, 1300, 700)
		a.poll()
		expectUsers(t, "late final record", takeUsers(t, a), map[string]int64{"7": 500})
		a.poll()
		expectUsers(t, "second poll", takeUsers(t, a), map[string]int64{})
	})
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

		e.killPppd(100)
		a.poll()
		b := e.restart(a, handOver, "7")
		b.poll()
		e.final("ppp0", "7", 100, 1000, 1300, 700)
		b.poll()
		expectUsers(t, "late final record after a restart", takeUsers(t, b), map[string]int64{"7": 500})
	})
}

func TestWatermarkWithoutAFinalRecordIsDroppedAfterTheGrace(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()

	e.killPppd(100)
	for i := 0; i < countedGraceRounds; i++ {
		a.poll()
	}
	if got := readCountedLines(t, e.countedPath()); len(got) != 1 || got[0] != "ppp0 100 1000 1000 500" {
		t.Fatalf("watermark within the grace = %q, want the reaped session kept", got)
	}
	a.poll()
	if got := readCountedLines(t, e.countedPath()); len(got) != 0 {
		t.Fatalf("watermark after the grace = %q, want it dropped", got)
	}
}

func TestOldFormatFinalPrefersTheMostRecentlyLiveWatermark(t *testing.T) {
	t.Run("same instance", func(t *testing.T) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 5000, 5000)
		a := e.instance("7", "8")
		a.poll()
		e.session("ppp0", "8", 200, 2000, 400, 100)
		a.poll()
		expectUsers(t, "both sessions", takeUsers(t, a), map[string]int64{"7": 10000, "8": 500})

		e.removeSession("ppp0")
		e.oldFinal("ppp0", "8", 700, 300)
		a.poll()
		expectUsers(t, "old-format final of the newer session", takeUsers(t, a), map[string]int64{"8": 500})
	})
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 5000, 5000)
		a := e.instance("7", "8")
		a.poll()
		e.session("ppp0", "8", 200, 2000, 400, 100)
		a.poll()
		expectUsers(t, "both sessions", takeUsers(t, a), map[string]int64{"7": 10000, "8": 500})

		b := e.restart(a, handOver, "7", "8")
		e.removeSession("ppp0")
		e.oldFinal("ppp0", "8", 700, 300)
		b.poll()
		expectUsers(t, "old-format final of the newer session after a restart", takeUsers(t, b), map[string]int64{"8": 500})
	})
}
