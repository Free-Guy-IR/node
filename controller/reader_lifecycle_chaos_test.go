package controller

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

type readableLedgerBackend struct {
	*ledgerBackend
}

func (r *readableLedgerBackend) GetSysStats(context.Context) (*common.BackendStatsResponse, error) {
	return &common.BackendStatsResponse{Uptime: 1}, nil
}

func (r *readableLedgerBackend) GetOutboundsLatency(context.Context, *common.LatencyRequest) (*common.LatencyResponse, error) {
	return &common.LatencyResponse{}, nil
}

func (r *readableLedgerBackend) GetUserOnlineStats(context.Context, string) (*common.OnlineStatResponse, error) {
	return &common.OnlineStatResponse{}, nil
}

func (r *readableLedgerBackend) GetUserOnlineIpListStats(context.Context, string) (*common.StatsOnlineIpListResponse, error) {
	return &common.StatsOnlineIpListResponse{}, nil
}

func TestReadersAndSyncsRacingLifecycleChangesNeverDeadlock(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		ledger := newPanelLedger()
		c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
		c.syncDrainGrace = 50 * time.Millisecond
		c.newBackend = func(context.Context, *common.Backend, int, int) (backend.Backend, error) {
			snapshot := ledger.startSnapshot()
			return &readableLedgerBackend{ledgerBackend: newLedgerBackend(ledger, snapshot)}, nil
		}

		stop := make(chan struct{})
		var workers sync.WaitGroup
		for i := 0; i < 4; i++ {
			w := &syncWriter{id: i, users: []string{fmt.Sprintf("w%d", i)}, ledger: ledger, c: c, rng: rand.New(rand.NewPCG(seed, uint64(i))), errs: &errorLog{}}
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					w.step(context.Background())
				}
			}()
		}
		readers := []func(){
			func() { _ = c.BaseInfoResponse() },
			func() { _ = c.SystemStats(context.Background()) },
			func() { _, _ = c.PrimarySysStats(context.Background()) },
			func() { _, _ = c.StatsAll(context.Background(), &common.StatRequest{}) },
			func() { _, _ = c.UserOnlineStatsAll(context.Background(), "w0") },
			func() { _, _ = c.OutboundsLatency(context.Background(), &common.LatencyRequest{}) },
			func() { _ = c.BackendTypes() },
			func() { _ = c.IsCurrentClient(panelIP) },
			func() { c.NewRequest() },
		}
		for _, read := range readers {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					read()
				}
			}()
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			rng := rand.New(rand.NewPCG(seed, 999))
			for i := 0; i < 30; i++ {
				switch rng.IntN(6) {
				case 0:
					panelStopsTheNode(c)
				case 1:
					panelAddsABackend(c, ledger, shareableTypes[rng.IntN(len(shareableTypes))])
				case 2:
					panelRemovesABackend(c, shareableTypes[rng.IntN(len(shareableTypes))])
				case 3:
					keepAliveWatchdogFires(c)
				default:
					_ = panelStartsTheNode(c, ledger)
				}
			}
			c.Close()
		}()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			panic(fmt.Sprintf("seed %d: lifecycle changes did not finish within 60s while readers and syncs ran", seed))
		}
		close(stop)
		workers.Wait()
	}
}
