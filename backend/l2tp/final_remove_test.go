package l2tp

import (
	"errors"
	"testing"
)

func TestUnremovableFinalRecordIsBilledOnceItIsRemoved(t *testing.T) {
	for _, listed := range []bool{false, true} {
		name := "session already gone"
		if listed {
			name = "session still listed"
		}
		t.Run(name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			a := e.instance("7")
			a.poll()
			expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

			if listed {
				e.counters("ppp0", 1300, 700)
				e.final("ppp0", "7", 100, 1000, 1300, 700)
			} else {
				e.end("ppp0", "7", 100, 1000, 1300, 700)
			}
			calls := 0
			prev := removeFinalRecord
			t.Cleanup(func() { removeFinalRecord = prev })
			removeFinalRecord = func(path string) error {
				calls++
				if calls <= 2 {
					return errors.New("device or resource busy")
				}
				return prev(path)
			}
			drainLogs(a)

			var billed []int64
			for i := 0; i < 4; i++ {
				a.poll()
				if listed && i == 0 {
					e.removeSession("ppp0")
				}
				billed = append(billed, takeUsers(t, a)["7"])
			}
			want := []int64{0, 0, 500, 0}
			for i := range want {
				if billed[i] != want[i] {
					t.Fatalf("billed per poll = %v, want %v", billed, want)
				}
			}
			if n := len(readFinalRecords(e.tag)); n != 0 {
				t.Fatalf("%d final records left after a successful removal", n)
			}
			if logs := drainLogs(a); len(logs) != 1 {
				t.Fatalf("a removal failure streak must log exactly once, got %d: %v", len(logs), logs)
			}
		})
	}
}
