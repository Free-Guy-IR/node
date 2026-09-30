package l2tp

import (
	"testing"
	"time"
)

func pinWallClockToHost(t *testing.T, h *fakeHost) {
	t.Helper()
	prev := wallClock
	t.Cleanup(func() { wallClock = prev })
	wallClock = func() time.Time { return time.Unix(h.clock, 0) }
}

func TestFallbackBillsASessionThatCameUpAfterItOnAClockSteppedBack(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	pinWallClockToHost(t, h)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("8")
	a.poll()
	h.advance(-30)
	p := h.spawn(4100, "8")
	h.grow(p, 70, 30)
	h.ipUp(p)
	blocked = false
	a.poll()
	expectUsers(t, "a session that came up after the fallback while the clock was stepped back", takeUsers(t, a), map[string]int64{"8": 100})
}

func TestFallbackDoesNotBillAgainASessionCountedBeforeAClockStepBack(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	pinWallClockToHost(t, h)
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.grow(p, 4000, 1000)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "the previous process", takeUsers(t, a), map[string]int64{"7": 5000})

	h.advance(-30)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)
	b := e.instance("7")
	b.poll()
	blocked = false
	b.poll()
	expectUsers(t, "a session the previous process counted, after the clock was stepped back", takeUsers(t, b), map[string]int64{})
	h.grow(p, 100, 50)
	b.poll()
	expectUsers(t, "its growth after that", takeUsers(t, b), map[string]int64{"7": 150})
}
