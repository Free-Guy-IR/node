package stats

import (
	"context"
	"testing"
)

func TestTrackerUnmarkedCounterDipCountsTheNewCounterFromZero(t *testing.T) {
	tracker := NewRestartingOnAnyDip()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: 1000, Tx: 1000})
	tracker.GetUsersStats(ctx, true)
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: 1500, Tx: 1600})
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: 200, Tx: 300})

	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "dip@example.com", 700, 900)

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: 250, Tx: 330})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "dip@example.com", 50, 30)
}

func TestTrackerOneSidedDipRestartsBothCounters(t *testing.T) {
	tracker := NewRestartingOnAnyDip()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "half@example.com", Rx: 1000, Tx: 1000})
	tracker.GetUsersStats(ctx, true)
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "half@example.com", Rx: 1500, Tx: 1600})
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "half@example.com", Rx: 200, Tx: 1700})

	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "half@example.com", 700, 2300)
}

func TestTrackerUnmarkedDipsRacingResetPullsReportEachByteOnce(t *testing.T) {
	tracker := NewRestartingOnAnyDip()
	ctx := context.Background()
	const email = "race@example.com"

	var pulledRx, pulledTx int64
	pull := func() {
		for _, stat := range tracker.GetUsersStats(ctx, true).GetStats() {
			if stat.GetName() != email {
				continue
			}
			if stat.GetType() == "uplink" {
				pulledRx += stat.GetValue()
			} else {
				pulledTx += stat.GetValue()
			}
		}
	}

	done := make(chan struct{})
	pulled := make(chan struct{})
	go func() {
		defer close(pulled)
		for {
			select {
			case <-done:
				return
			default:
				pull()
			}
		}
	}()

	var rx, tx, sentRx, sentTx int64
	for step := int64(1); step <= 3000; step++ {
		if step%97 == 0 {
			rx, tx = step%7, step%5
		} else {
			rx += step%13 + 1
			tx += step%11 + 1
		}
		if step%97 == 0 {
			sentRx += rx
			sentTx += tx
		} else {
			sentRx += step%13 + 1
			sentTx += step%11 + 1
		}
		applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: email, Rx: rx, Tx: tx})
	}
	close(done)
	<-pulled
	pull()

	if pulledRx != sentRx || pulledTx != sentTx {
		t.Fatalf("reported %d/%d bytes, the counters carried %d/%d", pulledRx, pulledTx, sentRx, sentTx)
	}
}
