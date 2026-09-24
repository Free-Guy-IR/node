package l2tp

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIPCPRenegotiationThroughTheScriptsKeepsOneSessionIdentity(t *testing.T) {
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		h := newFakeHost(t, e)
		a := e.instance("7")
		p := h.spawn(4100, "7")
		h.grow(p, 40, 20)
		h.ipUp(p)
		first, ok := h.session(p.ifname)
		if !ok {
			t.Fatal("ip-up wrote no session")
		}
		a.poll()
		expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 60})

		h.grow(p, 1000, 500)
		h.advance(3600)
		h.ipDown(p)
		h.grow(p, 3, 3)
		h.advance(2)
		h.ipUp(p)
		again, ok := h.session(p.ifname)
		if !ok {
			t.Fatal("the renegotiation's ip-up wrote no session")
		}
		if again.identity() != first.identity() {
			t.Fatalf("one pppd on one interface changed its session identity from %+v to %+v", first.identity(), again.identity())
		}
		a.poll()
		expectUsers(t, "the renegotiation's final record", takeUsers(t, a), map[string]int64{"7": 1500})

		a = e.restart(a, handOver, "7")
		h.grow(p, 100, 50)
		a.poll()
		expectUsers(t, "growth after the renegotiation", takeUsers(t, a), map[string]int64{"7": 156})

		h.ipDown(p)
		h.exit(p)
		a.poll()
		a.poll()
		expectUsers(t, "end of the session", takeUsers(t, a), map[string]int64{})
	})
}

func TestNewPppdOnAReusedPidAndInterfaceIsCountedFromZero(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7", "8")
	old := h.spawn(4100, "7")
	h.grow(old, 300, 200)
	h.ipUp(old)
	a.poll()
	h.grow(old, 700, 300)
	h.ipDown(old)
	h.exit(old)
	a.poll()
	expectUsers(t, "the old pppd", takeUsers(t, a), map[string]int64{"7": 1500})

	a = e.restart(a, false, "7", "8")
	h.advance(30)
	reused := h.spawn(4100, "8")
	if reused.ifname != old.ifname {
		t.Fatalf("the new pppd took %s, want %s", reused.ifname, old.ifname)
	}
	h.grow(reused, 1200, 900)
	h.ipUp(reused)
	a.poll()
	expectUsers(t, "a new pppd with the old pid on the old interface name", takeUsers(t, a), map[string]int64{"8": 2100})
}

func TestLegacySessionOnARecreatedInterfaceWithTheSamePidIsCountedFromZero(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 5000, 5000)
	a := e.instance("7", "8")
	a.poll()
	e.end("ppp0", "7", 100, 1000, 6000, 6000)
	a.poll()
	expectUsers(t, "first session", takeUsers(t, a), map[string]int64{"7": 12000})

	e.session("ppp0", "8", 100, 2000, 400, 100)
	a.poll()
	expectUsers(t, "a recreated interface under the same pid", takeUsers(t, a), map[string]int64{"8": 500})
}

func TestLegacyRenegotiationWhileItsFinalRecordCannotBeOpenedIsBilledOnce(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	e.counters("ppp0", 1200, 600)
	e.final("ppp0", "7", 100, 1000, 1200, 600)
	if err := os.Remove(filepath.Join(sessionStateDir, "ppp0")); err != nil {
		t.Fatal(err)
	}
	e.session("ppp0", "7", 100, 1007, 1200, 600)
	blocked := true
	prev := openFinalRecord
	t.Cleanup(func() { openFinalRecord = prev })
	openFinalRecord = func(path string) (io.ReadCloser, error) {
		if blocked {
			return nil, tooManyOpenFiles(path)
		}
		return prev(path)
	}

	e.counters("ppp0", 1300, 650)
	a.poll()
	a.poll()
	blocked = false
	e.counters("ppp0", 1400, 700)
	a.poll()
	a.poll()
	expectUsers(t, "renegotiation next to an unopenable final record", takeUsers(t, a), map[string]int64{"7": 600})
}

func TestNewPppdOnAReusedPidAndInterfaceInTheSameSecondGetsItsOwnIdentity(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7", "8")
	old := h.spawn(4100, "7")
	h.grow(old, 300, 200)
	h.ipUp(old)
	a.poll()
	first, _ := h.session(old.ifname)
	h.grow(old, 700, 300)
	h.ipDown(old)
	h.exit(old)
	a.poll()
	expectUsers(t, "the old pppd", takeUsers(t, a), map[string]int64{"7": 1500})

	a = e.restart(a, false, "7", "8")
	reused := h.spawn(4100, "8")
	h.grow(reused, 400, 100)
	h.ipUp(reused)
	again, _ := h.session(reused.ifname)
	if again.identity() == first.identity() {
		t.Fatalf("a new pppd took the identity %+v of an ended session", again.identity())
	}
	a.poll()
	expectUsers(t, "a new pppd on the old pid and name within the same second", takeUsers(t, a), map[string]int64{"8": 500})
}

func TestLegacyRenegotiationEndingBeforeItsNextPollIsBilledOnce(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	e.counters("ppp0", 1200, 600)
	e.final("ppp0", "7", 100, 1000, 1200, 600)
	if err := os.Remove(filepath.Join(sessionStateDir, "ppp0")); err != nil {
		t.Fatal(err)
	}
	e.session("ppp0", "7", 100, 1007, 1200, 600)
	e.end("ppp0", "7", 100, 1007, 1300, 650)
	a.poll()
	a.poll()
	expectUsers(t, "both finals of one pppd", takeUsers(t, a), map[string]int64{"7": 450})
}
