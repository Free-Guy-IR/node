package stats

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func accountingValues(resp *common.StatResponse, name string) []int64 {
	values := make([]int64, 0, 2)
	for _, stat := range resp.GetStats() {
		if stat.GetName() == name {
			values = append(values, stat.GetValue())
		}
	}
	slices.Sort(values)
	return values
}

func expectAccountingValues(t *testing.T, resp *common.StatResponse, name string, want ...int64) {
	t.Helper()

	wantSorted := make([]int64, 0, len(want))
	for _, value := range want {
		if value != 0 {
			wantSorted = append(wantSorted, value)
		}
	}
	slices.Sort(wantSorted)

	got := accountingValues(resp, name)
	if !slices.Equal(got, wantSorted) {
		t.Fatalf("%s: reported values %v, want %v", name, got, wantSorted)
	}
}

func applyAccountingSample(tracker *Tracker, sample Sample) {
	tracker.UpdateStatsBatch([]Sample{sample})
}

func backdateActivity(tracker *Tracker, key string, at time.Time) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.stats[key].LastActiveTime = at
}

func TestTrackerQuietSampleCountsBytesWithoutMarkingActivity(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "quiet@example.com", Rx: 100, Tx: 40, EndpointIP: "endpoint-a"})
	old := time.Now().Add(-time.Hour)
	backdateActivity(tracker, "key1", old)

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "quiet@example.com", Rx: 700, Tx: 300, EndpointIP: "endpoint-b", Quiet: true})

	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "quiet@example.com", 700, 300)

	entry := tracker.GetStatsEntries([]string{"key1"})["key1"]
	if !entry.LastActiveTime.Equal(old) {
		t.Fatalf("quiet sample moved LastActiveTime from %v to %v", old, entry.LastActiveTime)
	}
	if entry.EndpointIP != "endpoint-a" {
		t.Fatalf("quiet sample replaced the endpoint: got %q, want %q", entry.EndpointIP, "endpoint-a")
	}
	if tracker.AnyActiveSince([]string{"key1"}, time.Now().Add(-time.Minute)) {
		t.Fatal("quiet sample made the key active")
	}
	activity := tracker.EndpointActivity([]string{"key1"})
	if _, ok := activity["endpoint-b"]; ok {
		t.Fatalf("quiet sample published its endpoint: %v", activity)
	}
}

func TestTrackerQuietSampleForNewKeyCountsBytesAndStaysOffline(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "never@example.com", Rx: 90, Tx: 10, EndpointIP: "endpoint-a", Quiet: true})

	expectAccountingValues(t, tracker.GetUsersStats(ctx, false), "never@example.com", 90, 10)

	entry := tracker.GetStatsEntries([]string{"key1"})["key1"]
	if !entry.LastActiveTime.IsZero() {
		t.Fatalf("quiet sample set LastActiveTime to %v", entry.LastActiveTime)
	}
	if entry.EndpointIP != "" {
		t.Fatalf("quiet sample set the endpoint to %q", entry.EndpointIP)
	}
	if len(tracker.EndpointActivity([]string{"key1"})) != 0 {
		t.Fatal("quiet sample produced endpoint activity")
	}
}

func TestTrackerQuietSampleWithUnchangedCountersKeepsEndpoint(t *testing.T) {
	tracker := New()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "idle@example.com", Rx: 100, Tx: 40, EndpointIP: "endpoint-a"})
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "idle@example.com", Rx: 100, Tx: 40, EndpointIP: "endpoint-b", Quiet: true})

	entry := tracker.GetStatsEntries([]string{"key1"})["key1"]
	if entry.EndpointIP != "endpoint-a" {
		t.Fatalf("quiet sample replaced the endpoint: got %q, want %q", entry.EndpointIP, "endpoint-a")
	}
}

func TestTrackerZeroValueSampleKeepsActivityAndEndpoint(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "active@example.com", Rx: 100, Tx: 40, EndpointIP: "endpoint-a"})
	backdateActivity(tracker, "key1", time.Now().Add(-time.Hour))
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "active@example.com", Rx: 150, Tx: 60, EndpointIP: "endpoint-b"})

	expectAccountingValues(t, tracker.GetUsersStats(ctx, false), "active@example.com", 150, 60)

	if !tracker.AnyActiveSince([]string{"key1"}, time.Now().Add(-time.Minute)) {
		t.Fatal("a sample without Quiet must mark the key active")
	}
	entry := tracker.GetStatsEntries([]string{"key1"})["key1"]
	if entry.EndpointIP != "endpoint-b" {
		t.Fatalf("a sample without Quiet must update the endpoint: got %q", entry.EndpointIP)
	}

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "active@example.com", Rx: 150, Tx: 60, EndpointIP: "endpoint-c"})
	entry = tracker.GetStatsEntries([]string{"key1"})["key1"]
	if entry.EndpointIP != "endpoint-c" {
		t.Fatalf("an unchanged sample without Quiet must update the endpoint: got %q", entry.EndpointIP)
	}
}

func TestTrackerRestartCountersKeepsPendingAndCountsFromZero(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "restart@example.com", Rx: 1000, Tx: 400})
	tracker.GetUsersStats(ctx, true)
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "restart@example.com", Rx: 1300, Tx: 500})

	tracker.RestartCounters([]string{"key1", "missing"})

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "restart@example.com", Rx: 50, Tx: 20})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "restart@example.com", 350, 120)

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "restart@example.com", Rx: 80, Tx: 30})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "restart@example.com", 30, 10)

	if entries := tracker.GetStatsEntries([]string{"missing"}); len(entries) != 0 {
		t.Fatal("RestartCounters created an entry for an unknown key")
	}
}

func TestTrackerRestartCountersCountsGrowthLargerThanTheOldCounter(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "big@example.com", Rx: 1000, Tx: 400})
	tracker.GetUsersStats(ctx, true)
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "big@example.com", Rx: 1300, Tx: 500})

	tracker.RestartCounters([]string{"key1"})
	tracker.RestartCounters([]string{"key1"})

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "big@example.com", Rx: 5000, Tx: 2000})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "big@example.com", 5300, 2100)
}

func TestTrackerRestartCountersAfterReadCarriesNothing(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "read@example.com", Rx: 1000, Tx: 400})
	tracker.GetUsersStats(ctx, true)

	tracker.RestartCounters([]string{"key1"})

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "read@example.com", Rx: 1200, Tx: 450})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "read@example.com", 1200, 450)
}

func TestTrackerReaddAfterRemoveCountsTheFirstSample(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "readd@example.com", Rx: 1000, Tx: 2000})
	tracker.RemoveStats("key1")

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "readd@example.com", Rx: 100, Tx: 200})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "readd@example.com", 1100, 2200)

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "readd@example.com", Rx: 150, Tx: 260})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "readd@example.com", 50, 60)
}

func TestTrackerReaddWithoutPendingCountsTheFirstSample(t *testing.T) {
	cases := []struct {
		name   string
		rx, tx int64
	}{
		{name: "smaller than the old counter", rx: 30, tx: 40},
		{name: "larger than the old counter", rx: 1500, tx: 2500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := New()
			ctx := context.Background()

			applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "zero@example.com", Rx: 1000, Tx: 2000})
			tracker.GetUsersStats(ctx, true)
			tracker.RemoveStats("key1")

			applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "zero@example.com", Rx: tc.rx, Tx: tc.tx})
			expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "zero@example.com", tc.rx, tc.tx)

			entry := tracker.GetStatsEntries([]string{"key1"})["key1"]
			if entry == nil || entry.IsDeleted {
				t.Fatal("a re-added key must stay tracked and not deleted")
			}
		})
	}
}

func TestTrackerRemoveThenRestartThenReaddKeepsPendingOnce(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "mixed@example.com", Rx: 1000, Tx: 400})
	tracker.RemoveStats("key1")
	tracker.RestartCounters([]string{"key1"})

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "mixed@example.com", Rx: 10, Tx: 20})
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "mixed@example.com", 1010, 420)
}

func TestTrackerRestartedRemovedEntryIsReportedOnceThenDeleted(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "gone@example.com", Rx: 1000, Tx: 400})
	tracker.RestartCounters([]string{"key1"})
	tracker.RemoveStats("key1")
	tracker.CleanupDeletedEntries()

	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "gone@example.com", 1000, 400)
	expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "gone@example.com")

	if entries := tracker.GetStatsEntries([]string{"key1"}); len(entries) != 0 {
		t.Fatal("a removed entry must be deleted once its pending delta was read")
	}
}

func TestTrackerDetachMovesEveryEntryAndLeavesTheTrackerEmpty(t *testing.T) {
	tracker := New()
	ctx := context.Background()

	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "moved@example.com", Rx: 1000, Tx: 400})
	tracker.GetUsersStats(ctx, true)
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "moved@example.com", Rx: 1300, Tx: 500})
	applyAccountingSample(tracker, Sample{PublicKey: "key2", Email: "other@example.com", Rx: 50, Tx: 60})

	detached := tracker.Detach()
	if detached == tracker {
		t.Fatal("Detach must return a different tracker")
	}

	expectAccountingValues(t, tracker.GetUsersStats(ctx, false), "moved@example.com")
	if keys := tracker.Keys(); len(keys) != 0 {
		t.Fatalf("expected the detached-from tracker to be empty, got %v", keys)
	}

	tracker.RemoveStats("key1")
	applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "moved@example.com", Rx: 9000, Tx: 9000})
	tracker.RestartCounters([]string{"key2"})

	resp := detached.GetUsersStats(ctx, true)
	expectAccountingValues(t, resp, "moved@example.com", 300, 100)
	expectAccountingValues(t, resp, "other@example.com", 50, 60)

	applyAccountingSample(detached, Sample{PublicKey: "key1", Email: "moved@example.com", Rx: 1310, Tx: 505})
	expectAccountingValues(t, detached.GetUsersStats(ctx, true), "moved@example.com", 10, 5)
}

func TestTrackerRollbackInOneDirectionKeepsTheOtherDirectionsGrowth(t *testing.T) {
	cases := []struct {
		name           string
		rx, tx         int64
		want           []int64
		nextRx, nextTx int64
		wantNext       []int64
	}{
		{name: "rx dips while tx grows", rx: 5, tx: 70, want: []int64{20, 30}, nextRx: 6, nextTx: 75, wantNext: []int64{1, 5}},
		{name: "tx dips while rx grows", rx: 130, tx: 3, want: []int64{50, 10}, nextRx: 131, nextTx: 9, wantNext: []int64{1, 6}},
		{name: "both dip", rx: 5, tx: 3, want: []int64{20, 10}, nextRx: 7, nextTx: 4, wantNext: []int64{2, 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := New()
			ctx := context.Background()

			applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: 80, Tx: 40})
			tracker.GetUsersStats(ctx, true)
			applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: 100, Tx: 50})

			applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: tc.rx, Tx: tc.tx})
			expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "dip@example.com", tc.want...)

			applyAccountingSample(tracker, Sample{PublicKey: "key1", Email: "dip@example.com", Rx: tc.nextRx, Tx: tc.nextTx})
			expectAccountingValues(t, tracker.GetUsersStats(ctx, true), "dip@example.com", tc.wantNext...)
		})
	}
}
