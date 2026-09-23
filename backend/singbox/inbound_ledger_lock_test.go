package singbox

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/pasarguard/node/backend/singbox/api"
	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"github.com/pasarguard/node/common"
)

type blockingLedgerCore struct {
	*ledgerFakeCore
	arrived chan struct{}
	release chan struct{}
}

func (b *blockingLedgerCore) QueryStats(ctx context.Context, req *statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
	b.arrived <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.ledgerFakeCore.QueryStats(ctx, req)
}

func newBlockingLedgerSingBox(t *testing.T) (*SingBox, *blockingLedgerCore) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	core := &blockingLedgerCore{
		ledgerFakeCore: &ledgerFakeCore{counters: make(map[string]*atomic.Int64)},
		arrived:        make(chan struct{}, 8),
		release:        make(chan struct{}),
	}
	srv := grpc.NewServer()
	statscmd.RegisterStatsServiceServer(srv, core)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := api.NewClient(lis.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	return &SingBox{client: client}, core
}

func TestLedgerDrainsDoNotWaitBehindTheLedgerLock(t *testing.T) {
	sb, core := newBlockingLedgerSingBox(t)
	core.add(ledgerHy2Down, 1000)

	var released sync.Once
	release := func() { released.Do(func() { close(core.release) }) }
	t.Cleanup(release)

	type result struct {
		stats map[string]int64
		err   error
	}
	outbounds := make(chan result, 1)
	inbounds := make(chan result, 1)
	go func() {
		stats, err := ledgerRead(sb, common.StatType_Outbounds, "", true)
		outbounds <- result{stats, err}
	}()
	go func() {
		stats, err := ledgerRead(sb, common.StatType_Inbounds, "", true)
		inbounds <- result{stats, err}
	}()

	reached := 0
	for reached < 2 {
		select {
		case <-core.arrived:
			reached++
			continue
		case <-time.After(3 * time.Second):
		}
		break
	}
	release()

	outFirst, inFirst := <-outbounds, <-inbounds
	if reached < 2 {
		t.Fatalf("only %d of 2 concurrent drains reached the core; the second waited behind the ledger lock", reached)
	}
	if outFirst.err != nil || inFirst.err != nil {
		t.Fatalf("reads failed: outbounds %v, inbounds %v", outFirst.err, inFirst.err)
	}

	outTotal := sumLedger(outFirst.stats) + sumLedger(mustLedgerReadAfterRelease(t, sb, common.StatType_Outbounds, core))
	inTotal := sumLedger(inFirst.stats) + sumLedger(mustLedgerReadAfterRelease(t, sb, common.StatType_Inbounds, core))
	if outTotal != 1000 || inTotal != 1000 {
		t.Fatalf("each series must report the 1000 bytes exactly once, got outbounds %d, inbounds %d", outTotal, inTotal)
	}
}

func mustLedgerReadAfterRelease(t *testing.T, sb *SingBox, statType common.StatType, core *blockingLedgerCore) map[string]int64 {
	t.Helper()
	done := make(chan map[string]int64, 1)
	go func() { done <- mustLedgerRead(t, sb, statType, "", true) }()
	select {
	case <-core.arrived:
	case <-time.After(3 * time.Second):
		t.Fatalf("follow-up %s read never reached the core", statType)
	}
	return <-done
}

func sumLedger(stats map[string]int64) int64 {
	var total int64
	for _, value := range stats {
		total += value
	}
	return total
}
