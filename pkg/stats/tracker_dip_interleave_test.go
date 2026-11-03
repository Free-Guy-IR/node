package stats

import (
	"context"
	"sync"
	"testing"
)

func TestTrackerDetachKeepsTheRestartOnAnyDipRule(t *testing.T) {
	cases := []struct {
		name    string
		tracker *Tracker
		want    []int64
	}{
		{name: "wireguard rule", tracker: NewRestartingOnAnyDip(), want: []int64{500, 300}},
		{name: "default rule", tracker: New(), want: []int64{200, 100}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			detached := tc.tracker.Detach()

			applyAccountingSample(detached, Sample{PublicKey: "key1", Email: "detach@example.com", Rx: 1000, Tx: 500})
			detached.GetUsersStats(ctx, true)
			applyAccountingSample(detached, Sample{PublicKey: "key1", Email: "detach@example.com", Rx: 1200, Tx: 600})
			applyAccountingSample(detached, Sample{PublicKey: "key1", Email: "detach@example.com", Rx: 300, Tx: 200})

			expectAccountingValues(t, detached.GetUsersStats(ctx, true), "detach@example.com", tc.want...)
		})
	}
}

func runDipInterleave(t *testing.T, tracker *Tracker, wireguardShaped bool) {
	t.Helper()
	ctx := context.Background()
	const key, email = "key1", "interleave@example.com"

	var pulledMu sync.Mutex
	var pulledRx, pulledTx int64
	pull := func() {
		resp := tracker.GetUsersStats(ctx, true)
		pulledMu.Lock()
		defer pulledMu.Unlock()
		for _, stat := range resp.GetStats() {
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
	restarts := make(chan chan struct{})
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-done:
					return
				default:
					pull()
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for ack := range restarts {
			tracker.RestartCounters([]string{key})
			close(ack)
		}
	}()

	var closedRx, closedTx, openRx, openTx, sentRx, sentTx int64
	for step := int64(1); step <= 3000; step++ {
		switch {
		case step%211 == 0:
			ack := make(chan struct{})
			restarts <- ack
			<-ack
			closedRx, closedTx, openRx, openTx = 0, 0, 0, 0
		case step%97 == 0 && wireguardShaped:
			closedRx, closedTx = 0, 0
			openRx, openTx = step%7, step%5
			sentRx += openRx
			sentTx += openTx
		case step%97 == 0:
			openRx, openTx = 0, 0
		case step%50 == 0 && !wireguardShaped:
			closedRx += openRx
			closedTx += openTx
			openRx, openTx = 0, 0
		default:
			growRx, growTx := step%13+1, step%11+1
			openRx += growRx
			openTx += growTx
			sentRx += growRx
			sentTx += growTx
		}
		tracker.UpdateStatsBatch([]Sample{{PublicKey: key, Email: email, Rx: closedRx + openRx, Tx: closedTx + openTx}})
	}
	close(restarts)
	close(done)
	workers.Wait()
	pull()

	if pulledRx != sentRx || pulledTx != sentTx {
		t.Fatalf("reported %d/%d bytes, the counters carried %d/%d", pulledRx, pulledTx, sentRx, sentTx)
	}
}

func TestTrackerWireGuardRuleRacingPullsAndRestartsReportEachByteOnce(t *testing.T) {
	runDipInterleave(t, NewRestartingOnAnyDip(), true)
}

func TestTrackerDefaultRuleOpenVPNShapedDipsRacingPullsAndRestartsReportEachByteOnce(t *testing.T) {
	runDipInterleave(t, New(), false)
}
