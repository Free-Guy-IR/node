package stats

import (
	"sync"
	"testing"
)

type counterRead func(t *InterfaceCountersTracker, rx, tx int64, reset bool) (int64, int64)

var counterReads = map[string]counterRead{
	"Delta":        (*InterfaceCountersTracker).Delta,
	"DeltaClamped": (*InterfaceCountersTracker).DeltaClamped,
}

func expectCounterRead(t *testing.T, name string, read counterRead, tr *InterfaceCountersTracker, rx, tx int64, reset bool, wantRx, wantTx int64) {
	t.Helper()
	gotRx, gotTx := read(tr, rx, tx, reset)
	if gotRx != wantRx || gotTx != wantTx {
		t.Fatalf("%s(%d, %d, reset=%v) = %d/%d, want %d/%d", name, rx, tx, reset, gotRx, gotTx, wantRx, wantTx)
	}
}

func TestCountFromZeroReportsCarriedBytesOnceOnTopOfGrowth(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()
			tr.CountFromZero(100, 40)

			expectCounterRead(t, name, read, tr, 0, 0, false, 100, 40)
			expectCounterRead(t, name, read, tr, 30, 5, false, 130, 45)
			expectCounterRead(t, name, read, tr, 30, 5, true, 130, 45)
			expectCounterRead(t, name, read, tr, 30, 5, true, 0, 0)
			expectCounterRead(t, name, read, tr, 50, 5, true, 20, 0)
			expectCounterRead(t, name, read, tr, 50, 5, false, 0, 0)
		})
	}
}

func TestCountFromZeroCountsTheFirstSampleFromZero(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()
			tr.CountFromZero(0, 0)

			expectCounterRead(t, name, read, tr, 70, 30, true, 70, 30)
			expectCounterRead(t, name, read, tr, 90, 35, true, 20, 5)
		})
	}
}

func TestZeroValueTrackerStillTakesItsFirstSampleAsBaseline(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()

			expectCounterRead(t, name, read, tr, 70, 30, true, 0, 0)
			expectCounterRead(t, name, read, tr, 90, 35, true, 20, 5)
		})
	}
}

func TestCountFromZeroRestartsASourceThatWasAlreadyRead(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()
			expectCounterRead(t, name, read, tr, 1000, 500, true, 0, 0)
			expectCounterRead(t, name, read, tr, 1100, 520, true, 100, 20)

			unreadRx, unreadTx := read(tr, 1300, 600, true)
			if unreadRx != 200 || unreadTx != 80 {
				t.Fatalf("unread part before the restart = %d/%d, want 200/80", unreadRx, unreadTx)
			}
			tr.CountFromZero(unreadRx, unreadTx)

			expectCounterRead(t, name, read, tr, 10, 4, true, 210, 84)
			expectCounterRead(t, name, read, tr, 10, 4, true, 0, 0)
			expectCounterRead(t, name, read, tr, 25, 9, true, 15, 5)
		})
	}
}

func TestCountFromZeroAddsUpRepeatedCarries(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()
			tr.CountFromZero(100, 10)
			tr.CountFromZero(50, 5)

			expectCounterRead(t, name, read, tr, 7, 3, true, 157, 18)
			expectCounterRead(t, name, read, tr, 7, 3, true, 0, 0)
		})
	}
}

func TestCountFromZeroIgnoresNegativeCarries(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()
			tr.CountFromZero(-100, 30)

			expectCounterRead(t, name, read, tr, 0, 0, true, 0, 30)
		})
	}
}

func TestCountFromZeroIsReportedExactlyOnceUnderConcurrentReads(t *testing.T) {
	for name, read := range counterReads {
		t.Run(name, func(t *testing.T) {
			tr := NewInterfaceCountersTracker()
			tr.CountFromZero(1_000_000, 2_000_000)

			var (
				wg               sync.WaitGroup
				mu               sync.Mutex
				totalRx, totalTx int64
			)
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					rx, tx := read(tr, 0, 0, true)
					mu.Lock()
					totalRx += rx
					totalTx += tx
					mu.Unlock()
				}()
			}
			wg.Wait()

			if totalRx != 1_000_000 || totalTx != 2_000_000 {
				t.Fatalf("concurrent reset reads reported %d/%d, want 1000000/2000000 exactly once", totalRx, totalTx)
			}
		})
	}
}
