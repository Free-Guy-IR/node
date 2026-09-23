package stats

import (
	"context"
	"slices"
	"testing"

	"github.com/pasarguard/node/common"
)

func directionLegacyStats(name, link string, rx, tx int64) []*common.Stat {
	if rx == 0 && tx == 0 {
		return nil
	}
	out := make([]*common.Stat, 0, 2)
	if tx > 0 {
		out = append(out, &common.Stat{Name: name, Type: "uplink", Link: link, Value: tx})
	}
	if rx > 0 {
		out = append(out, &common.Stat{Name: name, Type: "downlink", Link: link, Value: rx})
	}
	return out
}

func directionByType(t *testing.T, list []*common.Stat) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, s := range list {
		if _, dup := out[s.GetType()]; dup {
			t.Fatalf("type %q reported twice in %v", s.GetType(), list)
		}
		out[s.GetType()] = s.GetValue()
	}
	return out
}

func directionSumByName(list []*common.Stat) map[string]int64 {
	out := map[string]int64{}
	for _, s := range list {
		out[s.GetName()] += s.GetValue()
	}
	return out
}

func directionValues(list []*common.Stat) []int64 {
	out := make([]int64, 0, len(list))
	for _, s := range list {
		out = append(out, s.GetValue())
	}
	slices.Sort(out)
	return out
}

func directionNonZero(values ...int64) []int64 {
	out := make([]int64, 0, len(values))
	for _, v := range values {
		if v != 0 {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func TestBuildInterfaceStatsReportsReceivedBytesAsUplink(t *testing.T) {
	got := directionByType(t, BuildInterfaceStats("wg0", "interface", 10, 90))
	if got["uplink"] != 10 || got["downlink"] != 90 || len(got) != 2 {
		t.Fatalf("rx=10 tx=90 must report uplink=10 downlink=90, got %v", got)
	}
}

func TestBuildDeltaStatsOmitsZeroDirection(t *testing.T) {
	onlyRx := directionByType(t, buildDeltaStats("u", "k", 10, 0))
	if onlyRx["uplink"] != 10 || len(onlyRx) != 1 {
		t.Fatalf("rx=10 tx=0 must report only uplink=10, got %v", onlyRx)
	}
	onlyTx := directionByType(t, buildDeltaStats("u", "k", 0, 90))
	if onlyTx["downlink"] != 90 || len(onlyTx) != 1 {
		t.Fatalf("rx=0 tx=90 must report only downlink=90, got %v", onlyTx)
	}
	if none := buildDeltaStats("u", "k", 0, 0); len(none) != 0 {
		t.Fatalf("rx=0 tx=0 must report nothing, got %v", none)
	}
}

func TestTrackerReportsReceivedBytesAsUplink(t *testing.T) {
	tracker := New()
	tracker.UpdateStatsBatch([]Sample{{PublicKey: "key-a", Email: "a@example.com", Rx: 10, Tx: 90}})

	user := directionByType(t, tracker.GetStats(context.Background(), []string{"key-a"}, false).GetStats())
	if user["uplink"] != 10 || user["downlink"] != 90 || len(user) != 2 {
		t.Fatalf("GetStats: rx=10 tx=90 must report uplink=10 downlink=90, got %v", user)
	}

	users := directionByType(t, tracker.GetUsersStats(context.Background(), true).GetStats())
	if users["uplink"] != 10 || users["downlink"] != 90 || len(users) != 2 {
		t.Fatalf("GetUsersStats: rx=10 tx=90 must report uplink=10 downlink=90, got %v", users)
	}
}

func TestDirectionMappingIsByteIdenticalToLegacy(t *testing.T) {
	cases := []struct{ rx, tx int64 }{
		{0, 0},
		{10, 0},
		{0, 90},
		{10, 90},
		{90, 10},
		{1, 1},
		{7, 1 << 40},
		{1 << 41, 3},
		{123456789, 987654321},
	}

	for _, c := range cases {
		got := buildDeltaStats("user@example.com", "key", c.rx, c.tx)
		legacy := directionLegacyStats("user@example.com", "key", c.rx, c.tx)

		for _, s := range got {
			if s.GetName() != "user@example.com" || s.GetLink() != "key" {
				t.Fatalf("rx=%d tx=%d: name/link changed: %v", c.rx, c.tx, s)
			}
		}

		sum := directionSumByName(got)
		if c.rx+c.tx == 0 {
			if len(got) != 0 {
				t.Fatalf("rx=0 tx=0 must report nothing, got %v", got)
			}
		} else if sum["user@example.com"] != c.rx+c.tx || len(sum) != 1 {
			t.Fatalf("rx=%d tx=%d: per-name sum %v, want %d", c.rx, c.tx, sum, c.rx+c.tx)
		}

		legacySum := directionSumByName(legacy)
		if sum["user@example.com"] != legacySum["user@example.com"] {
			t.Fatalf("rx=%d tx=%d: per-name sum %d differs from eab2601 %d", c.rx, c.tx, sum["user@example.com"], legacySum["user@example.com"])
		}

		values := directionValues(got)
		if !slices.Equal(values, directionNonZero(c.rx, c.tx)) {
			t.Fatalf("rx=%d tx=%d: values %v, want %v", c.rx, c.tx, values, directionNonZero(c.rx, c.tx))
		}
		if !slices.Equal(values, directionValues(legacy)) {
			t.Fatalf("rx=%d tx=%d: values %v differ from eab2601 %v", c.rx, c.tx, values, directionValues(legacy))
		}

		tracker := New()
		tracker.UpdateStatsBatch([]Sample{{PublicKey: "key", Email: "user@example.com", Rx: c.rx, Tx: c.tx}})
		viaTracker := tracker.GetUsersStats(context.Background(), true).GetStats()
		if !slices.Equal(directionValues(viaTracker), directionValues(legacy)) {
			t.Fatalf("rx=%d tx=%d: tracker values %v differ from eab2601 %v", c.rx, c.tx, directionValues(viaTracker), directionValues(legacy))
		}
		if directionSumByName(viaTracker)["user@example.com"] != c.rx+c.tx {
			t.Fatalf("rx=%d tx=%d: tracker sum %v, want %d", c.rx, c.tx, directionSumByName(viaTracker), c.rx+c.tx)
		}
	}
}
