package l2tp

import "testing"

func TestLingeringSessionListingAfterItsFinalRecordAddsNothing(t *testing.T) {
	for _, oldFormat := range []bool{false, true} {
		name := "final with identity"
		if oldFormat {
			name = "old-format final"
		}
		t.Run(name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			a := e.instance("7")
			a.poll()
			expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

			e.counters("ppp0", 1300, 700)
			if oldFormat {
				e.oldFinal("ppp0", "7", 1300, 700)
			} else {
				e.final("ppp0", "7", 100, 1000, 1300, 700)
			}
			var billed []int64
			for i := 0; i < 3; i++ {
				a.poll()
				billed = append(billed, takeUsers(t, a)["7"])
			}
			e.removeSession("ppp0")
			a.poll()
			billed = append(billed, takeUsers(t, a)["7"])
			want := []int64{500, 0, 0, 0}
			for i := range want {
				if billed[i] != want[i] {
					t.Fatalf("billed per poll = %v, want %v", billed, want)
				}
			}
		})
	}
	forBothRestarts(t, func(t *testing.T, handOver bool) {
		e := newAcctEnv(t)
		e.session("ppp0", "7", 100, 1000, 1000, 500)
		a := e.instance("7")
		a.poll()
		expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

		e.counters("ppp0", 1300, 700)
		e.final("ppp0", "7", 100, 1000, 1300, 700)
		a.poll()
		expectUsers(t, "final record", takeUsers(t, a), map[string]int64{"7": 500})

		b := e.restart(a, handOver, "7")
		b.poll()
		expectUsers(t, "lingering listing after a restart", takeUsers(t, b), map[string]int64{})
	})
}

func TestConsumedWatermarkIsDroppedAfterTheGrace(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()

	e.end("ppp0", "7", 100, 1000, 1300, 700)
	for i := 0; i < countedGraceRounds; i++ {
		a.poll()
	}
	if got := readCountedLines(t, e.countedPath()); len(got) != 1 || got[0] != "consumed ppp0 100 1000 1300 700" {
		t.Fatalf("watermark within the grace = %q, want the final values kept", got)
	}
	a.poll()
	if got := readCountedLines(t, e.countedPath()); len(got) != 0 {
		t.Fatalf("watermark after the grace = %q, want it dropped", got)
	}
}
