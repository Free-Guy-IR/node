package l2tp

import (
	"slices"
	"strings"
	"testing"
)

const earlierBoot = "4f3e2d1c-0b9a-4f8e-8d7c-6b5a4f3e2d1c"

func hideBootID(t *testing.T) *bool {
	t.Helper()
	hidden := true
	blockProcReads(t, func(name string) bool { return hidden && name == "sys/kernel/random/boot_id" })
	return &hidden
}

func priorLines(t *testing.T, e *acctEnv) []string {
	t.Helper()
	var out []string
	for _, l := range readCountedLines(t, e.countedPath()) {
		if strings.HasPrefix(l, countedPriorPrefix) {
			out = append(out, l)
		}
	}
	return out
}

func TestFinalOfABootWhoseIDWasNeverReadIsBilledThoughItCouldNotBeReadInThatBoot(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	a := e.instance("8")
	a.poll()
	*blocked = false

	e.bootAt(middleBoot, 1500)
	hidden := hideBootID(t)
	unreadable := true
	blockFinalRecords(t, func(string) bool { return unreadable })
	b := e.instance("8")
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middleBoot+":3000", 70, 30)
	b.poll()
	expectUsers(t, "while neither the boot id nor the final record can be read", takeUsers(t, b), map[string]int64{})

	e.bootAt(nextBoot, 1500)
	*hidden, unreadable = false, false
	c := e.instance("8")
	c.poll()
	expectUsers(t, "the final of that boot, first read after the next reboot", takeUsers(t, c), map[string]int64{"8": 100})
}

func TestFinalOfABootWhoseIDWasNeverReadIsBilledUnderAClockSteppedBack(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	a := e.instance("8")
	a.poll()
	*blocked = false

	e.bootAt(middleBoot, 1500)
	hidden := hideBootID(t)
	b := e.instance("8")
	e.finalWithMono("ppp1", "8", 200, fallbackLoad-3600, middleBoot+":3000", 70, 30)
	b.poll()

	e.bootAt(nextBoot, 1500)
	*hidden = false
	c := e.instance("8")
	c.poll()
	expectUsers(t, "the final of a boot whose id was never read, started under a clock stepped back", takeUsers(t, c), map[string]int64{"8": 100})
}

func TestFinalOfAnotherBootPresentAtTheFallbackStaysCountedAfterABootWhoseIDWasNeverRead(t *testing.T) {
	e := newAcctEnv(t)
	e.finalWithMono("ppp0", "7", 100, fallbackLoad+600, earlierBoot+":2000", 4000, 1000)
	unreadable := true
	blockFinalRecords(t, func(string) bool { return unreadable })
	blocked := openFallbackWindow(t, e)
	a := e.instance("7", "8")
	a.poll()
	*blocked = false

	e.bootAt(middleBoot, 1500)
	hidden := hideBootID(t)
	b := e.instance("7", "8")
	unreadable = false
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middleBoot+":3000", 70, 30)
	b.poll()
	expectUsers(t, "while the boot id cannot be read", takeUsers(t, b), map[string]int64{})

	e.bootAt(nextBoot, 1500)
	*hidden = false
	c := e.instance("7", "8")
	c.poll()
	expectUsers(t, "a final present at the fallback and one written after it, both of boots the window cannot place", takeUsers(t, c), map[string]int64{"8": 100})
	if lines := priorLines(t, e); len(lines) != 0 {
		t.Fatalf("a final record present at the fallback must be forgotten once it is consumed, got %q", lines)
	}
}

func TestFallbackListsTheFinalRecordsAtTheFirstPollThatCan(t *testing.T) {
	e := newAcctEnv(t)
	e.finalWithMono("ppp0", "7", 100, fallbackLoad+600, earlierBoot+":2000", 4000, 1000)
	blocked := openFallbackWindow(t, e)
	finals := true
	blockListing(t, func() string { return sessionFinalDir }, &finals)
	a := e.instance("7", "8")
	a.poll()
	*blocked = false
	finals = false
	a.poll()
	expectUsers(t, "a final of another boot present at the fallback, first listed after it", takeUsers(t, a), map[string]int64{})

	e.bootAt(middleBoot, 1500)
	hidden := hideBootID(t)
	finals = true
	b := e.instance("7", "8")
	finals = false
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middleBoot+":3000", 70, 30)
	b.poll()

	e.bootAt(nextBoot, 1500)
	*hidden = false
	c := e.instance("7", "8")
	c.poll()
	expectUsers(t, "the final of a boot whose id was never read, after a poll listed the final records", takeUsers(t, c), map[string]int64{"8": 100})
}

func TestFallbackListsTheFinalRecordsAtTheFirstLoadThatCan(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	finals := true
	blockListing(t, func() string { return sessionFinalDir }, &finals)
	a := e.instance("8")
	a.poll()
	*blocked = false

	e.bootAt(middleBoot, 1500)
	hidden := hideBootID(t)
	finals = false
	b := e.instance("8")
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middleBoot+":3000", 70, 30)
	b.poll()

	e.bootAt(nextBoot, 1500)
	*hidden = false
	c := e.instance("8")
	c.poll()
	expectUsers(t, "the final of a boot whose id was never read, written before its first poll", takeUsers(t, c), map[string]int64{"8": 100})
}

func TestPriorFinalRecordsAndTheListedMarkSurviveAReload(t *testing.T) {
	for _, line := range []string{"prior ", "prior a b", "prior a/b"} {
		if _, err := decodeCountedState([]byte("v2\n" + line + "\n")); err == nil {
			t.Fatalf("a counted state with %q must be unreadable", line)
		}
	}
	state := "v2\nprior ppp1.30001.1700000001\nsince 1800000000 " + fallbackBoot + ":500000 " + nextBoot + " +\n"
	c, err := decodeCountedState([]byte(state))
	if err != nil || !c.sinceSeen || !c.sincePrior["ppp1.30001.1700000001"] || !slices.Equal(c.sinceBoots, []string{nextBoot}) {
		t.Fatalf("decode = %+v, %v", c, err)
	}
	if got := string(c.encode()); got != state {
		t.Fatalf("encode = %q, want %q", got, state)
	}
}

func TestFallbackWindowForgetsItsPriorFinalRecordsWhenItCloses(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.bootAt(fallbackBoot, 500000)
	e.write(e.finalName("ppp5"), "user=9\ntag=another-inbound\nifname=ppp5\npid=500\nstarted=1\nrx=1\ntx=1\n")
	unreadable := true
	blockFinalRecords(t, func(string) bool { return unreadable })
	e.write(e.countedPath(), "not a counted state\n")
	a := e.instance("7")
	if lines := priorLines(t, e); len(lines) != 1 {
		t.Fatalf("the final records present at the fallback must be written at once, got %q", lines)
	}
	unreadable = false
	for i := 0; i <= countedGraceRounds; i++ {
		a.poll()
	}
	for _, l := range readCountedLines(t, e.countedPath()) {
		if strings.HasPrefix(l, countedPriorPrefix) || strings.HasPrefix(l, countedSincePrefix) {
			t.Fatalf("a closed window must forget its fallback and its prior final records, got %q", readCountedLines(t, e.countedPath()))
		}
	}
}
