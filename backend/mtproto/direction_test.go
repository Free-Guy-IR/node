package mtproto

import (
	"context"
	"testing"

	"github.com/9seconds/mtg/v2/mtglib"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func directionMTProto() (*Backend, *mtprotoEventStream) {
	b := &Backend{
		order:           []string{"mtproto-dir"},
		instances:       map[string]*proxyInstance{},
		inboundCounters: map[string]*inboundCounter{"mtproto-dir": {}},
		statsTracker:    stats.New(),
		secretsByID:     map[string]secretEntry{"alice": {email: "alice@example.com"}},
	}
	counter := b.inboundCounters["mtproto-dir"]
	accumulator := newEventAccumulator(&b.outboundRx, &b.outboundTx, &counter.rx, &counter.tx)
	b.instances["mtproto-dir"] = &proxyInstance{tag: "mtproto-dir", accumulator: accumulator}
	return b, newMtprotoEventStream(accumulator, b.emailForUsername)
}

func directionRead(t *testing.T, b *Backend, request *common.StatRequest) (map[string]int64, map[string]int64) {
	t.Helper()
	resp, err := b.GetStats(context.Background(), request)
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

func TestMTProtoUplinkIsWhatTheClientSentToTelegram(t *testing.T) {
	b, stream := directionMTProto()
	ctx := context.Background()
	stream.Send(ctx, mtglib.NewEventAuthenticated("stream-1", "alice"))
	stream.Send(ctx, mtglib.NewEventTraffic("stream-1", 10, false))
	stream.Send(ctx, mtglib.NewEventTraffic("stream-1", 90, true))

	for _, c := range []struct {
		request *common.StatRequest
		name    string
	}{
		{&common.StatRequest{Type: common.StatType_UserStat, Name: "alice@example.com"}, "alice@example.com"},
		{&common.StatRequest{Type: common.StatType_UsersStat, Reset_: true}, "alice@example.com"},
		{&common.StatRequest{Type: common.StatType_Inbounds}, "mtproto-dir"},
		{&common.StatRequest{Type: common.StatType_Inbounds, Reset_: true}, "mtproto-dir"},
		{&common.StatRequest{Type: common.StatType_Outbounds}, "mtproto"},
		{&common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}, "mtproto"},
	} {
		got, sum := directionRead(t, b, c.request)
		if got["uplink"] != 10 || got["downlink"] != 90 {
			t.Fatalf("%v reset=%v: client sent 10 and Telegram returned 90, got %v", c.request.GetType(), c.request.GetReset_(), got)
		}
		if sum[c.name] != 100 || len(sum) != 1 {
			t.Fatalf("%v reset=%v: sum %v, want %s=100", c.request.GetType(), c.request.GetReset_(), sum, c.name)
		}
	}
}
