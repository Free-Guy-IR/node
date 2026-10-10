package l2tp

import (
	"slices"
	"strings"
	"testing"
)

func TestFinalOfTheFallbackBootStartedBeforeItAndWrittenAfterTheListingStaysCounted(t *testing.T) {
	e := newAcctEnv(t)
	openFallbackWindow(t, e)
	a := e.instance("7")
	a.poll()
	e.finalWithMono("ppp0", "7", 100, fallbackLoad+600, fallbackBoot+":400000", 4000, 1000)
	a.poll()
	expectUsers(t, "the final of a session of the fallback boot that started before it, never listed, written after the window listed the final records", takeUsers(t, a), map[string]int64{})
}

func TestFinalRecordNamedFinalAtTheFallbackIsNotNamedPrior(t *testing.T) {
	e := newAcctEnv(t)
	e.finalWithMono("ppp0", "7", 100, fallbackLoad-60, fallbackBoot+":400000", 4000, 1000)
	openFallbackWindow(t, e)
	e.instance("7")
	lines := readCountedLines(t, e.countedPath())
	named := slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, countedFinalPrefix) })
	if !named || len(priorLines(t, e)) != 0 {
		t.Fatalf("a final record named final at the fallback must not also be named prior, got %q", lines)
	}
}
