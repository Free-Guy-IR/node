package stats

import (
	"sync"

	"github.com/pasarguard/node/common"
)

// InterfaceCountersTracker tracks delta and reset state for interface-level RX/TX counters.
type InterfaceCountersTracker struct {
	mu sync.Mutex

	baseRx  int64
	baseTx  int64
	baseSet bool
}

func NewInterfaceCountersTracker() *InterfaceCountersTracker {
	return &InterfaceCountersTracker{}
}

// Delta calculates counters relative to the current baseline.
// On first sample, it sets baseline and returns zero.
// If counters roll back (interface reset/restart), it rebases and returns zero.
//
// Intended for a single, genuinely monotonic source (e.g. a real kernel
// network-interface byte counter, as used by WireGuard's
// handleInterfaceOutboundStats) - a rollback there only ever means the
// interface itself was torn down and recreated. Do not reuse this for an
// aggregate summed across many independent, frequently-churning counters
// (e.g. per-user session totals) - see DeltaClamped below for that case.
func (t *InterfaceCountersTracker) Delta(currentRx, currentTx int64, reset bool) (int64, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.baseSet {
		t.baseRx = currentRx
		t.baseTx = currentTx
		t.baseSet = true
	}

	if currentRx < t.baseRx || currentTx < t.baseTx {
		t.baseRx = currentRx
		t.baseTx = currentTx
	}

	deltaRx := currentRx - t.baseRx
	deltaTx := currentTx - t.baseTx

	if reset {
		t.baseRx = currentRx
		t.baseTx = currentTx
	}

	return deltaRx, deltaTx
}

// DeltaClamped is Delta's counterpart for a counter that is itself an
// aggregate SUM across many independent, frequently-churning sources -
// OpenVPN's per-instance outbound total, computed by summing
// mgmt.AllUserStats() across every currently-known user, is exactly this:
// unlike a real interface counter, the sum can transiently DIP (not just
// plateau) whenever a user disconnects and briefly isn't counted in either
// the live-session or closed-totals bucket before being re-added - with
// thousands of real, constantly reconnecting users this happens often, and
// Delta's "any decrease means the source restarted, rebase to zero" logic
// would misfire on every such dip, permanently discarding already-tracked
// progress and making the reported total appear to stay at zero forever
// even though real traffic keeps flowing (caught via a production
// discrepancy: this reported zero while node_user_usages, the independent
// per-user billing path, kept growing normally).
//
// The baseline here only ever moves forward (never down, not even on
// reset), so a transient dip is simply reported as zero for that one poll -
// no data is lost, nothing is double-counted once the aggregate climbs back
// past the prior high-water mark, and there is no reliance on "restart"
// detection at all (a genuine backend restart replaces the owning
// instanceProcess - and this tracker along with it - long before Delta is
// ever called again).
func (t *InterfaceCountersTracker) DeltaClamped(currentRx, currentTx int64, reset bool) (int64, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.baseSet {
		t.baseRx = currentRx
		t.baseTx = currentTx
		t.baseSet = true
	}

	deltaRx := currentRx - t.baseRx
	if deltaRx < 0 {
		deltaRx = 0
	}
	deltaTx := currentTx - t.baseTx
	if deltaTx < 0 {
		deltaTx = 0
	}

	if reset {
		if currentRx > t.baseRx {
			t.baseRx = currentRx
		}
		if currentTx > t.baseTx {
			t.baseTx = currentTx
		}
	}

	return deltaRx, deltaTx
}

func buildDeltaStats(name, link string, rx, tx int64) []*common.Stat {
	if rx == 0 && tx == 0 {
		return nil
	}

	stats := make([]*common.Stat, 0, 2)
	if tx > 0 {
		stats = append(stats, &common.Stat{
			Name:  name,
			Type:  "uplink",
			Link:  link,
			Value: tx,
		})
	}
	if rx > 0 {
		stats = append(stats, &common.Stat{
			Name:  name,
			Type:  "downlink",
			Link:  link,
			Value: rx,
		})
	}

	return stats
}

func BuildInterfaceStats(name, link string, rx, tx int64) []*common.Stat {
	return buildDeltaStats(name, link, rx, tx)
}
