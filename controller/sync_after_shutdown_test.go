package controller

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

type fencedBackend struct {
	*ledgerBackend
	mu       sync.Mutex
	syncing  int
	down     bool
	breaches *atomic.Int64
}

func (f *fencedBackend) enter() {
	f.mu.Lock()
	if f.down {
		f.breaches.Add(1)
	}
	f.syncing++
	f.mu.Unlock()
	time.Sleep(20 * time.Microsecond)
}

func (f *fencedBackend) leave() {
	f.mu.Lock()
	f.syncing--
	f.mu.Unlock()
}

func (f *fencedBackend) Shutdown() {
	f.mu.Lock()
	if f.syncing > 0 {
		f.breaches.Add(1)
	}
	f.down = true
	f.mu.Unlock()
	f.ledgerBackend.Shutdown()
}

func (f *fencedBackend) SyncUser(ctx context.Context, u *common.User) error {
	f.enter()
	defer f.leave()
	return f.ledgerBackend.SyncUser(ctx, u)
}

func (f *fencedBackend) QueueUser(ctx context.Context, u *common.User) error {
	f.enter()
	defer f.leave()
	return f.ledgerBackend.QueueUser(ctx, u)
}

func (f *fencedBackend) SyncUsers(ctx context.Context, users []*common.User) error {
	f.enter()
	defer f.leave()
	return f.ledgerBackend.SyncUsers(ctx, users)
}

func (f *fencedBackend) UpdateUsers(ctx context.Context, users []*common.User) error {
	f.enter()
	defer f.leave()
	return f.ledgerBackend.UpdateUsers(ctx, users)
}

func (f *fencedBackend) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	f.enter()
	defer f.leave()
	return f.ledgerBackend.UpdateUsers(ctx, users)
}

func runFencedChaos(t *testing.T, seed uint64) int64 {
	ledger := newPanelLedger()
	var breaches atomic.Int64
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	c.newBackend = func(context.Context, *common.Backend, int, int) (backend.Backend, error) {
		return &fencedBackend{ledgerBackend: newLedgerBackend(ledger, nil), breaches: &breaches}, nil
	}

	stop := make(chan struct{})
	var writers sync.WaitGroup
	for i := 0; i < 6; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			rng := rand.New(rand.NewPCG(seed, uint64(i)))
			email := fmt.Sprintf("w%d", i)
			for {
				select {
				case <-stop:
					return
				default:
				}
				u := ledger.write(email)
				switch rng.IntN(4) {
				case 0:
					_ = c.QueueUserAll(context.Background(), u)
				case 1:
					_ = c.SyncUserAll(context.Background(), u)
				case 2:
					_ = c.SyncUsersAll(context.Background(), []*common.User{u})
				default:
					_ = c.UpdateUsersAll(context.Background(), []*common.User{u})
				}
			}
		}()
	}

	var control sync.WaitGroup
	control.Add(3)
	go func() {
		defer control.Done()
		rng := rand.New(rand.NewPCG(seed, 100))
		for i := 0; i < 5; i++ {
			if rng.IntN(3) == 0 {
				panelStopsTheNode(c)
				continue
			}
			_ = panelStartsTheNode(c, ledger)
		}
	}()
	go func() {
		defer control.Done()
		rng := rand.New(rand.NewPCG(seed, 200))
		for i := 0; i < 6; i++ {
			backendType := shareableTypes[rng.IntN(len(shareableTypes))]
			if rng.IntN(2) == 0 {
				panelAddsABackend(c, ledger, backendType)
			} else {
				panelRemovesABackend(c, backendType)
			}
		}
	}()
	go func() {
		defer control.Done()
		keepAliveWatchdogFires(c)
	}()
	control.Wait()
	c.Close()
	close(stop)
	writers.Wait()
	return breaches.Load()
}

func TestNoUserSyncReachesABackendOnceItsShutdownBegins(t *testing.T) {
	iterations := 40
	if testing.Short() {
		iterations = 8
	}
	for i := 0; i < iterations; i++ {
		if n := runFencedChaos(t, uint64(i)+1); n > 0 {
			t.Fatalf("seed %d: %d user syncs reached a backend during or after its Shutdown", i+1, n)
		}
	}
}
