package l2tp

import (
	"path/filepath"
	"testing"
)

func TestFallbackDoesNotBillAgainASessionWhoseCounterCannotBeReadAtTheLoad(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "the previous process", takeUsers(t, a), map[string]int64{"7": 5000})

	e.write(e.countedPath(), "not a counted state\n")
	restore := verifyMakeUnreadable(t, filepath.Join(sysClassNet, "ppp0", "statistics", "rx_bytes"))
	b := e.instance("7")
	restore()
	e.counters("ppp0", 4100, 1050)
	b.poll()
	expectUsers(t, "a session whose counter could not be read when the counted state was unreadable, at its first reading", takeUsers(t, b), map[string]int64{})
	e.counters("ppp0", 4200, 1100)
	b.poll()
	expectUsers(t, "its growth after that", takeUsers(t, b), map[string]int64{"7": 150})
}

func TestFallbackDoesNotBillAgainASessionWhoseInterfaceCannotBeCheckedAtTheLoad(t *testing.T) {
	e := newAcctEnv(t)
	verifySessionWithIfindex(e, "ppp0", "7", 100, 1000, 41, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "the previous process", takeUsers(t, a), map[string]int64{"7": 5000})

	e.write(e.countedPath(), "not a counted state\n")
	restore := verifyMakeUnreadable(t, filepath.Join(sysClassNet, "ppp0", "ifindex"))
	b := e.instance("7")
	restore()
	e.counters("ppp0", 4100, 1050)
	b.poll()
	expectUsers(t, "a session whose interface could not be checked when the counted state was unreadable, at its first reading", takeUsers(t, b), map[string]int64{})
	e.counters("ppp0", 4200, 1100)
	b.poll()
	expectUsers(t, "its growth after that", takeUsers(t, b), map[string]int64{"7": 150})
}

func TestFallbackWindowDoesNotBillAgainASessionWhoseCounterCannotBeReadWhenFirstListed(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockListing(t, func() string { return sessionStateDir }, &blocked)

	a := e.instance("7")
	a.poll()
	restore := verifyMakeUnreadable(t, filepath.Join(sysClassNet, "ppp0", "statistics", "rx_bytes"))
	blocked = false
	a.poll()
	restore()
	a.poll()
	expectUsers(t, "a session started before the fallback, first listed while its counter could not be read", takeUsers(t, a), map[string]int64{})
	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "its growth after that", takeUsers(t, a), map[string]int64{"7": 150})
}
