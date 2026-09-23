package l2tp

import (
	"os"
	"testing"
)

func TestTwoOldFormatFinalsOfOneInterfaceInOnePollAreEachBilledWhole(t *testing.T) {
	e := newAcctEnv(t)
	e.oldFinal("ppp0", "7", 1000, 500)
	e.oldFinal("ppp0", "8", 1300, 700)
	a := e.instance("7", "8")
	a.poll()
	expectUsers(t, "two never-counted old-format finals", takeUsers(t, a), map[string]int64{"7": 1500, "8": 2000})
}

func TestOldFormatFinalAfterAnEarlierOldFormatFinalIsBilledWhole(t *testing.T) {
	e := newAcctEnv(t)
	e.oldFinal("ppp0", "7", 1000, 500)
	a := e.instance("7", "8")
	a.poll()
	expectUsers(t, "first old-format final", takeUsers(t, a), map[string]int64{"7": 1500})

	e.oldFinal("ppp0", "8", 1300, 700)
	a.poll()
	expectUsers(t, "second old-format final of the same interface", takeUsers(t, a), map[string]int64{"8": 2000})
}

func TestOldFormatFinalNeverMatchesAnAlreadyConsumedSession(t *testing.T) {
	for _, restart := range []string{"same instance", "hand-over", "process restart"} {
		t.Run(restart, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 4100, 1700000100, 400, 100)
			a := e.instance("7", "8")
			a.poll()
			expectUsers(t, "live session polled", takeUsers(t, a), map[string]int64{"7": 500})

			e.end("ppp0", "7", 4100, 1700000100, 1000, 500)
			a.poll()
			expectUsers(t, "its final record", takeUsers(t, a), map[string]int64{"7": 1000})

			switch restart {
			case "hand-over":
				a = e.restart(a, true, "7", "8")
			case "process restart":
				a = e.restart(a, false, "7", "8")
			}

			e.oldFinal("ppp0", "8", 1300, 700)
			a.poll()
			expectUsers(t, "a later never-counted old-format final on the same interface", takeUsers(t, a), map[string]int64{"8": 2000})
		})
	}
}

func TestUnreadableCountedStateTreatsEveryPresentOldFormatFinalAsCounted(t *testing.T) {
	e := newAcctEnv(t)
	e.oldFinal("ppp0", "7", 1000, 500)
	e.oldFinal("ppp0", "8", 1300, 700)
	e.write(e.countedPath(), "not a counted state\n")

	a := e.instance("7", "8")
	a.poll()
	expectUsers(t, "old-format finals present when the counted state was unreadable", takeUsers(t, a), map[string]int64{})

	entries, err := os.ReadDir(sessionFinalDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the present final records must be consumed, %d left", len(entries))
	}
}
