package controller

import (
	"context"
	"github.com/pasarguard/node/backend"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

type fakeStatsBackend struct {
	backend.Backend
	rx, tx int64
	calls  int
}

func (f *fakeStatsBackend) Started() bool { return true }

func (f *fakeStatsBackend) GetStats(_ context.Context, req *common.StatRequest) (*common.StatResponse, error) {
	f.calls++
	if req.GetReset_() {
		panic("own-throughput sampling must never reset the billing counters")
	}
	return &common.StatResponse{Stats: []*common.Stat{
		{Name: "u", Type: "uplink", Value: f.tx},
		{Name: "u", Type: "downlink", Value: f.rx},
		{Name: "u", Type: "online_ip", Value: 9_999_999},
	}}, nil
}

func TestSampleOwnThroughputReportsThisNodesTraffic(t *testing.T) {
	b := &fakeStatsBackend{}
	c := &Controller{backend: b, lastRequest: time.Now()}

	if _, _, ok := c.sampleOwnThroughput(context.Background()); !ok {
		t.Fatal("first sample should establish a baseline")
	}

	b.rx, b.tx = 3_000_000, 1_500_000
	c.mu.Lock()
	c.ownSampledAt = time.Now().Add(-4 * time.Second)
	c.mu.Unlock()

	rx, tx, ok := c.sampleOwnThroughput(context.Background())
	if !ok {
		t.Fatal("expected a measurement")
	}
	if rx < 700_000 || rx > 800_000 {
		t.Fatalf("incoming speed off: %d", rx)
	}
	if tx < 350_000 || tx > 400_000 {
		t.Fatalf("outgoing speed off: %d", tx)
	}
}

func TestSampleOwnThroughputRebaselinesAfterPanelReset(t *testing.T) {
	b := &fakeStatsBackend{rx: 5_000_000, tx: 5_000_000}
	c := &Controller{backend: b, lastRequest: time.Now()}
	c.sampleOwnThroughput(context.Background())

	c.mu.Lock()
	c.ownSampledAt = time.Now().Add(-4 * time.Second)
	c.ownRxSpeed, c.ownTxSpeed = 111, 222
	c.mu.Unlock()

	b.rx, b.tx = 10, 10
	rx, tx, ok := c.sampleOwnThroughput(context.Background())
	if !ok {
		t.Fatal("expected a measurement")
	}
	if rx != 111 || tx != 222 {
		t.Fatalf("a counter reset must not invent a spike, got %d/%d", rx, tx)
	}
}

func TestSampleOwnThroughputSkippedWhenNobodyIsWatching(t *testing.T) {
	b := &fakeStatsBackend{}
	c := &Controller{backend: b, lastRequest: time.Now().Add(-5 * time.Minute)}
	c.sampleOwnThroughput(context.Background())
	if b.calls != 0 {
		t.Fatalf("idle node should not poll its backend, got %d calls", b.calls)
	}
}

func TestSampleOwnThroughputFallsBackWithoutBackend(t *testing.T) {
	c := &Controller{lastRequest: time.Now()}
	if _, _, ok := c.sampleOwnThroughput(context.Background()); ok {
		t.Fatal("without a started backend the host-wide reading must be used")
	}
}
