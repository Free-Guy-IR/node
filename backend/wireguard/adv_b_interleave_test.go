package wireguard

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pasarguard/node/common"
)

func TestAdvBConcurrentReadsAcrossStartsBillEveryByteOnce(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgadvb-start"
	keys := []string{mustGeneratePublicKey(t), mustGeneratePublicKey(t), mustGeneratePublicKey(t)}
	users := make([]*common.User, 0, len(keys))
	for i, key := range keys {
		users = append(users, accountingUser(iface, fmt.Sprintf("advb%d@example.com", i), key, i+2))
	}
	k.mu.Lock()
	k.growPerRead = 11
	k.mu.Unlock()

	var current atomic.Pointer[WireGuard]
	var reported atomic.Int64
	readOnce := func(wg *WireGuard) {
		if wg == nil {
			return
		}
		resp, err := wg.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
		if err != nil {
			return
		}
		reported.Add(sumTraffic(resp))
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 3 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				readOnce(current.Load())
			}
		})
	}

	ctx := context.Background()
	for round := range 10 {
		roundUsers := users
		if round%2 == 1 {
			roundUsers = users[:2]
		}
		wg := startAccountingBackend(t, k, iface, roundUsers)
		current.Store(wg)

		var workers sync.WaitGroup
		for range 2 {
			workers.Go(func() {
				for range 5 {
					tick(wg)
				}
			})
		}
		workers.Go(func() {
			for range 5 {
				_ = wg.SyncUsers(ctx, users)
				_ = wg.SyncUser(ctx, accountingUserWithoutInbound(users[2].GetEmail(), keys[2], 4))
				_ = wg.SyncUser(ctx, users[2])
				_ = wg.Restart()
			}
		})
		workers.Wait()

		shutdownDone := make(chan struct{})
		go func() {
			wg.Shutdown()
			close(shutdownDone)
		}()
		readOnce(wg)
		<-shutdownDone
	}

	final := startAccountingBackend(t, k, iface, users)
	current.Store(final)
	close(stop)
	readers.Wait()
	readOnce(final)
	readOnce(final)

	if produced := k.producedBytes(); produced == 0 || reported.Load() != produced {
		t.Fatalf("reported %d bytes, the device counted %d", reported.Load(), produced)
	}
}
