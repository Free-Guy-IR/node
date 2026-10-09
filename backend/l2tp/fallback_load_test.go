package l2tp

import (
	"os"
	"testing"
)

const (
	middleBoot = "6b5c4d3e-2f10-4b2c-8d9e-8f7a6b5c4d3e"
	nextBoot   = "7c6d5e4f-3021-4c3d-9eaf-9a8b7c6d5e4f"
)

func TestBootNotedAtTheLoadIsWrittenAtOnceWhileTheFinalRecordsCannotBeListed(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	finals := true
	blockListing(t, func() string { return sessionFinalDir }, &finals)
	a := e.instance("8")
	a.poll()
	*blocked = false

	e.bootAt(middleBoot, 1500)
	e.instance("8")
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middleBoot+":3000", 70, 30)

	e.bootAt(nextBoot, 1500)
	finals = false
	c := e.instance("8")
	c.poll()
	expectUsers(t, "the final of a boot whose process never polled, listed only after the next reboot", takeUsers(t, c), map[string]int64{"8": 100})
}

func TestCountedStateIsNotRewrittenByALoadOrAPollThatChangesNothing(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})
	before, err := os.Stat(e.countedPath())
	if err != nil {
		t.Fatal(err)
	}
	b := e.instance("7")
	b.poll()
	expectUsers(t, "nothing moved", takeUsers(t, b), map[string]int64{})
	after, err := os.Stat(e.countedPath())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("a load and a poll that change nothing must not rewrite the counted state")
	}
}
