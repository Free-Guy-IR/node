package l2tp

import (
	"context"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func runningL2TP(tag string) *L2TP {
	return &L2TP{
		config:         &Config{InboundTag: tag},
		statsTracker:   stats.New(),
		interfaceStats: stats.NewInterfaceCountersTracker(),
		inboundStats:   stats.NewInterfaceCountersTracker(),
		state:          lifecycleRunning,
	}
}

func read(t *testing.T, o *L2TP, typ common.StatType, reset bool) (int64, int64) {
	t.Helper()
	resp, err := o.GetStats(context.Background(), &common.StatRequest{Type: typ, Reset_: reset})
	if err != nil {
		t.Fatalf("GetStats(%v): %v", typ, err)
	}
	var rx, tx int64
	for _, s := range resp.GetStats() {
		switch s.GetType() {
		case "downlink":
			rx = s.GetValue()
		case "uplink":
			tx = s.GetValue()
		}
	}
	return rx, tx
}

func TestInboundAndOutboundDoNotEatEachOthersDeltas(t *testing.T) {
	o := runningL2TP("l2tp-de")

	read(t, o, common.StatType_Inbounds, true)
	read(t, o, common.StatType_Outbounds, true)

	o.mu.Lock()
	o.totalRx, o.totalTx = 4000, 900
	o.mu.Unlock()

	inRx, inTx := read(t, o, common.StatType_Inbounds, true)
	if inRx != 4000 || inTx != 900 {
		t.Fatalf("inbound reported %d/%d, want 4000/900", inRx, inTx)
	}

	outRx, outTx := read(t, o, common.StatType_Outbounds, true)
	if outRx != 4000 || outTx != 900 {
		t.Fatalf("the inbound read consumed the outbound delta: got %d/%d, want 4000/900", outRx, outTx)
	}

	if rx, tx := read(t, o, common.StatType_Inbounds, true); rx != 0 || tx != 0 {
		t.Fatalf("a drained inbound must report nothing, got %d/%d", rx, tx)
	}
}

func TestInboundReportsUnderTheConfiguredTag(t *testing.T) {
	o := runningL2TP("l2tp-de")
	read(t, o, common.StatType_Inbounds, true)

	o.mu.Lock()
	o.totalRx = 512
	o.mu.Unlock()

	resp, err := o.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_Inbounds})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetStats()) == 0 {
		t.Fatal("expected a stat")
	}
	for _, s := range resp.GetStats() {
		if s.GetName() != "l2tp-de" {
			t.Fatalf("reported under %q, want the configured inbound tag", s.GetName())
		}
	}
}

func TestInboundSurvivesAPppInterfaceTeardown(t *testing.T) {
	o := runningL2TP("l2tp-de")
	read(t, o, common.StatType_Inbounds, true)

	o.mu.Lock()
	o.totalRx, o.totalTx = 10_000, 2_000
	o.mu.Unlock()
	read(t, o, common.StatType_Inbounds, true)

	o.mu.Lock()
	o.totalRx, o.totalTx = 300, 60
	o.mu.Unlock()

	if rx, tx := read(t, o, common.StatType_Inbounds, true); rx != 0 || tx != 0 {
		t.Fatalf("a counter that went backwards must rebase, not report a negative spike: got %d/%d", rx, tx)
	}

	o.mu.Lock()
	o.totalRx, o.totalTx = 800, 160
	o.mu.Unlock()
	if rx, tx := read(t, o, common.StatType_Inbounds, true); rx != 500 || tx != 100 {
		t.Fatalf("after rebasing it must count forward again: got %d/%d, want 500/100", rx, tx)
	}
}
