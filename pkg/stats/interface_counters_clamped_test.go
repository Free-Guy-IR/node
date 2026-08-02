package stats

import "testing"

// Reproduces the exact production scenario: an aggregate sum across many
// churning users climbs, then transiently dips (a user's bytes briefly drop
// out during the sessionStats -> closedTotals handoff), then keeps
// climbing. Delta() would rebase-to-zero on the dip and permanently lose
// track of prior progress; DeltaClamped must not.
func TestDeltaClamped_TolerantOfChurnDip(t *testing.T) {
	tr := NewInterfaceCountersTracker()

	// First poll establishes baseline.
	rx, tx := tr.DeltaClamped(1000, 500, true)
	if rx != 0 || tx != 0 {
		t.Fatalf("first poll: want (0,0), got (%d,%d)", rx, tx)
	}

	// Real growth.
	rx, tx = tr.DeltaClamped(2000, 900, true)
	if rx != 1000 || tx != 400 {
		t.Fatalf("growth poll: want (1000,400), got (%d,%d)", rx, tx)
	}

	// Churn dip: aggregate SUM transiently drops (a user's bytes briefly
	// missing from both sessionStats and closedTotals).
	rx, tx = tr.DeltaClamped(1800, 850, true)
	if rx != 0 || tx != 0 {
		t.Fatalf("dip poll: want clamped (0,0), got (%d,%d)", rx, tx)
	}

	// Aggregate recovers past the prior high-water mark (2000/900) - the
	// delta must reflect only genuinely NEW growth beyond that mark, not
	// double-count anything and not have lost the mark during the dip.
	rx, tx = tr.DeltaClamped(2500, 1000, true)
	if rx != 500 || tx != 100 {
		t.Fatalf("recovery poll: want (500,100), got (%d,%d)", rx, tx)
	}
}

// Sanity check that the un-clamped Delta still exhibits the OLD
// rebase-on-decrease behavior (proving this test suite would have caught
// the bug if Delta had been used for the churn scenario above).
func TestDelta_StillRebasesOnDecrease(t *testing.T) {
	tr := NewInterfaceCountersTracker()
	tr.Delta(1000, 500, true)
	tr.Delta(2000, 900, true)
	rx, tx := tr.Delta(1800, 850, true)
	if rx != 0 || tx != 0 {
		t.Fatalf("want (0,0) after rebase, got (%d,%d)", rx, tx)
	}
	// After rebasing to (1800,850), growth to (2500,1000) computes delta
	// from the LOWERED baseline - this is the actual production bug: real
	// progress made before the dip (up to 2000/900) is silently discarded.
	rx, tx = tr.Delta(2500, 1000, false)
	if rx != 700 || tx != 150 {
		t.Fatalf("want (700,150) demonstrating the bug, got (%d,%d)", rx, tx)
	}
}
