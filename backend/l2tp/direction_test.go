package l2tp

import (
	"context"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func directionL2TP() *L2TP {
	return &L2TP{
		config:         &Config{InboundTag: "l2tp-dir"},
		statsTracker:   stats.New(),
		interfaceStats: stats.NewInterfaceCountersTracker(),
		inboundStats:   stats.NewInterfaceCountersTracker(),
		state:          lifecycleRunning,
	}
}

func directionRead(t *testing.T, o *L2TP, request *common.StatRequest) (map[string]int64, map[string]int64) {
	t.Helper()
	resp, err := o.GetStats(context.Background(), request)
	if err != nil {
		t.Fatalf("GetStats(%v): %v", request.GetType(), err)
	}
	byType := map[string]int64{}
	sumByName := map[string]int64{}
	for _, s := range resp.GetStats() {
		if _, dup := byType[s.GetType()]; dup {
			t.Fatalf("GetStats(%v): type %q reported twice", request.GetType(), s.GetType())
		}
		byType[s.GetType()] = s.GetValue()
		sumByName[s.GetName()] += s.GetValue()
	}
	return byType, sumByName
}

func TestL2TPUserUplinkIsWhatTheClientSent(t *testing.T) {
	o := directionL2TP()
	o.statsTracker.UpdateStatsBatch([]stats.Sample{{PublicKey: "alice", Email: "alice", Rx: 10, Tx: 90}})

	got, sum := directionRead(t, o, &common.StatRequest{Type: common.StatType_UserStat, Name: "alice"})
	if got["uplink"] != 10 || got["downlink"] != 90 {
		t.Fatalf("UserStat: ppp rx=10 tx=90, got %v", got)
	}
	if sum["alice"] != 100 {
		t.Fatalf("UserStat: per-user sum %v, want 100", sum)
	}

	got, sum = directionRead(t, o, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if got["uplink"] != 10 || got["downlink"] != 90 {
		t.Fatalf("UsersStat: ppp rx=10 tx=90, got %v", got)
	}
	if sum["alice"] != 100 {
		t.Fatalf("UsersStat: per-user sum %v, want 100", sum)
	}
}

func TestL2TPInboundAndOutboundUplinkIsWhatClientsSent(t *testing.T) {
	o := directionL2TP()
	directionRead(t, o, &common.StatRequest{Type: common.StatType_Inbounds, Reset_: true})
	directionRead(t, o, &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true})

	o.mu.Lock()
	o.totalRx, o.totalTx = 10, 90
	o.mu.Unlock()

	for _, request := range []*common.StatRequest{
		{Type: common.StatType_Inbound, Name: "l2tp-dir"},
		{Type: common.StatType_Outbound, Name: "l2tp-dir"},
		{Type: common.StatType_Inbounds, Reset_: true},
		{Type: common.StatType_Outbounds, Reset_: true},
	} {
		got, sum := directionRead(t, o, request)
		if got["uplink"] != 10 || got["downlink"] != 90 {
			t.Fatalf("%v: ppp rx=10 tx=90, got %v", request.GetType(), got)
		}
		if sum["l2tp-dir"] != 100 {
			t.Fatalf("%v: sum %v, want 100", request.GetType(), sum)
		}
	}
}
