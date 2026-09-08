package mtproto

import (
	"testing"

	"github.com/pasarguard/node/common"
)

func newCountedBackend(tags ...string) *Backend {
	b := &Backend{
		order:           append([]string(nil), tags...),
		instances:       make(map[string]*proxyInstance, len(tags)),
		inboundCounters: make(map[string]*inboundCounter, len(tags)),
	}
	for _, tag := range tags {
		b.instances[tag] = &proxyInstance{}
		b.inboundCounters[tag] = &inboundCounter{}
	}
	return b
}

func valuesByTag(resp *common.StatResponse) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for _, s := range resp.GetStats() {
		if out[s.GetName()] == nil {
			out[s.GetName()] = map[string]int64{}
		}
		out[s.GetName()][s.GetType()] = s.GetValue()
	}
	return out
}

func TestInboundStatKeepsEachInstanceOnItsOwnTag(t *testing.T) {
	b := newCountedBackend("mtproto-tg", "mtproto-tg2", "mtproto-tg443")
	b.inboundCounters["mtproto-tg"].rx.Store(100)
	b.inboundCounters["mtproto-tg"].tx.Store(10)
	b.inboundCounters["mtproto-tg443"].rx.Store(700)
	b.inboundCounters["mtproto-tg443"].tx.Store(70)

	for i := 0; i < 50; i++ {
		got := valuesByTag(b.inboundStat(false))
		if got["mtproto-tg"]["downlink"] != 100 || got["mtproto-tg"]["uplink"] != 10 {
			t.Fatalf("run %d: mtproto-tg reported %v", i, got["mtproto-tg"])
		}
		if got["mtproto-tg443"]["downlink"] != 700 || got["mtproto-tg443"]["uplink"] != 70 {
			t.Fatalf("run %d: mtproto-tg443 reported %v", i, got["mtproto-tg443"])
		}
		if _, leaked := got["mtproto-tg2"]; leaked {
			t.Fatalf("run %d: an idle instance must not report traffic: %v", i, got["mtproto-tg2"])
		}
	}
}

func TestInboundStatResetDrainsOnlyOnce(t *testing.T) {
	b := newCountedBackend("mtproto-tg", "mtproto-tg2")
	b.inboundCounters["mtproto-tg"].rx.Store(500)
	b.inboundCounters["mtproto-tg"].tx.Store(50)

	first := valuesByTag(b.inboundStat(true))
	if first["mtproto-tg"]["downlink"] != 500 || first["mtproto-tg"]["uplink"] != 50 {
		t.Fatalf("first drain reported %v", first["mtproto-tg"])
	}

	if second := b.inboundStat(true); len(second.GetStats()) != 0 {
		t.Fatalf("a drained counter must report nothing, got %v", second.GetStats())
	}

	b.inboundCounters["mtproto-tg"].rx.Store(7)
	third := valuesByTag(b.inboundStat(false))
	if third["mtproto-tg"]["downlink"] != 7 {
		t.Fatalf("traffic after a drain must start from zero, got %v", third["mtproto-tg"])
	}
}

func TestInboundStatIgnoresCountersWithNoInstance(t *testing.T) {
	b := newCountedBackend("mtproto-tg")
	b.order = append(b.order, "vanished")
	b.inboundCounters["mtproto-tg"].rx.Store(3)

	got := valuesByTag(b.inboundStat(false))
	if _, ok := got["vanished"]; ok {
		t.Fatal("a tag with no counter must be skipped")
	}
	if got["mtproto-tg"]["downlink"] != 3 {
		t.Fatalf("mtproto-tg reported %v", got["mtproto-tg"])
	}
}

func TestPerInstanceInboundSumsToTheBackendOutbound(t *testing.T) {
	b := newCountedBackend("mtproto-tg", "mtproto-tg2")
	accums := map[string]*eventAccumulator{}
	for _, tag := range b.order {
		c := b.inboundCounters[tag]
		accums[tag] = newEventAccumulator(&b.outboundRx, &b.outboundTx, &c.rx, &c.tx)
	}

	accums["mtproto-tg"].traffic("s1", 1200, true)
	accums["mtproto-tg"].traffic("s1", 300, false)
	accums["mtproto-tg2"].traffic("s2", 800, true)
	accums["mtproto-tg2"].traffic("s2", 100, false)

	var sumRx, sumTx int64
	for _, tag := range b.order {
		sumRx += b.inboundCounters[tag].rx.Load()
		sumTx += b.inboundCounters[tag].tx.Load()
	}

	if sumRx != b.outboundRx.Load() || sumTx != b.outboundTx.Load() {
		t.Fatalf("per-inbound total %d/%d must equal the backend outbound total %d/%d",
			sumRx, sumTx, b.outboundRx.Load(), b.outboundTx.Load())
	}
	if sumRx != 2000 || sumTx != 400 {
		t.Fatalf("bytes went missing: got %d/%d want 2000/400", sumRx, sumTx)
	}
}
