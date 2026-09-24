package l2tp

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLateIPDownOfAnExitedPppdBillsItsOwnTailAndLeavesTheNextSessionAlone(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7", "8")
	old := h.spawn(5100, "7")
	h.grow(old, 300, 200)
	h.ipUp(old)
	a.poll()
	expectUsers(t, "the first pppd", takeUsers(t, a), map[string]int64{"7": 500})

	h.grow(old, 400, 100)
	h.exit(old)
	next := h.spawn(5200, "8")
	if next.ifname != old.ifname {
		t.Fatalf("the next pppd took %s, want %s", next.ifname, old.ifname)
	}
	h.grow(next, 60, 40)
	h.ipUp(next)
	a.poll()
	expectUsers(t, "the next pppd on the name", takeUsers(t, a), map[string]int64{"8": 100})

	h.ipDown(old)
	s, ok := h.session(next.ifname)
	if !ok || s.pid != next.pid || s.user != "8" {
		t.Fatalf("the late ip-down of pppd %d touched the session of pppd %d: %+v (listed %v)", old.pid, next.pid, s, ok)
	}
	a.poll()
	h.grow(next, 100, 100)
	a.poll()
	expectUsers(t, "the late tail and the next session's growth", takeUsers(t, a), map[string]int64{"7": 500, "8": 200})

	h.ipDown(next)
	h.exit(next)
	a.poll()
	a.poll()
	expectUsers(t, "end of the next session", takeUsers(t, a), map[string]int64{})
}

func TestIPDownAfterItsSessionFileWasReapedStillBillsTheTail(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	sessionIsAlive = pppdSessionIsAlive
	a := e.instance("7")
	p := h.spawn(5100, "7")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "live session", takeUsers(t, a), map[string]int64{"7": 500})

	h.grow(p, 400, 100)
	h.exit(p)
	a.poll()
	if _, err := os.Stat(filepath.Join(sessionStateDir, p.ifname)); !os.IsNotExist(err) {
		t.Fatalf("the session of an exited pppd must be reaped, stat err = %v", err)
	}
	h.ipDown(p)
	a.poll()
	a.poll()
	expectUsers(t, "tail from the late ip-down", takeUsers(t, a), map[string]int64{"7": 500})
}

func TestIPDownOfALivePppdReadsItsOwnInterfaceCounters(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7")
	p := h.spawn(5100, "7")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "live session", takeUsers(t, a), map[string]int64{"7": 500})

	h.grow(p, 400, 100)
	h.run(h.scripts.ipDownScriptPath(), "PEERNAME=7", "IFNAME="+p.ifname, "PPPD_PID="+strconv.Itoa(p.pid), "BYTES_RCVD=650", "BYTES_SENT=280")
	h.exit(p)
	a.poll()
	expectUsers(t, "tail read from the interface", takeUsers(t, a), map[string]int64{"7": 500})
}

func TestIPDownOfAPppdThatNeverRecordedASessionIgnoresAStaleRecordOfItsPid(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7", "8")
	old := h.spawn(4100, "7")
	h.grow(old, 300, 200)
	h.ipUp(old)
	h.ipDown(old)
	h.exit(old)
	a.poll()
	expectUsers(t, "the old pppd", takeUsers(t, a), map[string]int64{"7": 500})

	reused := h.spawn(4100, "")
	h.grow(reused, 900, 900)
	h.run(h.scripts.ipUpScriptPath(), "IFNAME="+reused.ifname, "PPPD_PID=4100")
	h.run(h.scripts.ipDownScriptPath(), "IFNAME="+reused.ifname, "PPPD_PID=4100", "BYTES_RCVD=900", "BYTES_SENT=900")
	if names := finalRecordPaths(t); len(names) != 0 {
		t.Fatalf("a pppd without a session wrote final records %v from a stale record of its pid", names)
	}
	a.poll()
	expectUsers(t, "a pppd without a session", takeUsers(t, a), map[string]int64{})
}
