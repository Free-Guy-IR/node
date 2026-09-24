package wireguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pasarguard/node/common"
)

type interleavedLedger struct {
	t         *testing.T
	iface     string
	emails    []string
	userBytes atomic.Int64
	uplink    atomic.Int64
	downlink  atomic.Int64
}

func (l *interleavedLedger) readUsers(wg *WireGuard, request *common.StatRequest) {
	resp, err := wg.GetStats(context.Background(), request)
	if err != nil {
		return
	}
	l.userBytes.Add(sumTraffic(resp))
}

func (l *interleavedLedger) readInterface(wg *WireGuard, request *common.StatRequest) {
	resp, err := wg.GetStats(context.Background(), request)
	if err != nil {
		return
	}
	for _, stat := range resp.GetStats() {
		if stat.GetName() != l.iface || stat.GetLink() != "interface" {
			l.t.Errorf("interface stat labelled %q/%q, want %q/interface", stat.GetName(), stat.GetLink(), l.iface)
			continue
		}
		switch stat.GetType() {
		case "uplink":
			l.uplink.Add(stat.GetValue())
		case "downlink":
			l.downlink.Add(stat.GetValue())
		default:
			l.t.Errorf("unexpected interface stat type %q", stat.GetType())
		}
	}
}

func (l *interleavedLedger) read(wg *WireGuard, kind int) {
	if wg == nil {
		return
	}
	switch kind % 4 {
	case 0:
		l.readUsers(wg, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	case 1:
		l.readUsers(wg, &common.StatRequest{Type: common.StatType_UserStat, Name: l.emails[kind%len(l.emails)], Reset_: true})
	case 2:
		l.readInterface(wg, &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true})
	default:
		l.readInterface(wg, &common.StatRequest{Type: common.StatType_Outbound, Name: l.iface, Reset_: true})
	}
}

func TestAccountingEveryByteReportedOnceAtNodeAndUserLevelAcrossStarts(t *testing.T) {
	t.Run("without mtu", func(t *testing.T) {
		runInterleavedStarts(t, "wgacct-interleave", nil)
	})
	t.Run("with mtu", func(t *testing.T) {
		runInterleavedStarts(t, "wgacct-interleave-mtu", mtuValue(1340))
	})
}

func runInterleavedStarts(t *testing.T, iface string, mtu *int) {
	k := newAccountingKernel()
	keys := []string{mustGeneratePublicKey(t), mustGeneratePublicKey(t), mustGeneratePublicKey(t)}
	users := make([]*common.User, 0, len(keys))
	emails := make([]string, 0, len(keys))
	for i, key := range keys {
		email := fmt.Sprintf("interleave%d@example.com", i)
		users = append(users, accountingUser(iface, email, key, i+2))
		emails = append(emails, email)
	}
	removed := accountingUserWithoutInbound(emails[2], keys[2], 4)
	k.mu.Lock()
	k.growPerRead = 11
	k.growInterfacePerRead = 5
	k.mu.Unlock()

	ledger := &interleavedLedger{t: t, iface: iface, emails: emails}
	var current atomic.Pointer[WireGuard]
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for reader := range 4 {
		readers.Go(func() {
			for n := reader; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				ledger.read(current.Load(), n)
			}
		})
	}

	ctx := context.Background()
	for round := range 12 {
		if round%3 == 2 {
			k.failNextConfigure(errors.New("configure refused"))
			if _, err := newAccountingBackendWithMTU(t, k, iface, users, mtu); err == nil {
				t.Fatal("expected the backend start to fail")
			}
			k.failNextConfigure(nil)
		}

		roundUsers := users
		if round%2 == 1 {
			roundUsers = users[:2]
		}
		wg := startAccountingBackendWithMTU(t, k, iface, roundUsers, mtu)
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
			for range 4 {
				_ = wg.SyncUsers(ctx, users)
				_ = wg.SyncUser(ctx, removed)
				_ = wg.SyncUser(ctx, users[2])
				_ = wg.Restart()
				_ = wg.UpdateUsers(ctx, []*common.User{removed})
				_ = wg.UpdateUsersAndRestart(ctx, []*common.User{users[2]})
			}
		})
		workers.Go(func() {
			for kind := range 8 {
				ledger.read(wg, kind)
			}
		})
		if mtu != nil {
			workers.Go(func() {
				for range 4 {
					k.setExternalMTU(accountingDefaultMTU)
					_ = wg.Restart()
				}
			})
		}
		workers.Wait()

		var stopping sync.WaitGroup
		stopping.Go(wg.Shutdown)
		stopping.Go(func() { _ = wg.Restart() })
		stopping.Go(func() { _ = wg.SyncUsers(ctx, users) })
		stopping.Go(func() { tick(wg) })
		stopping.Go(func() { k.setExternalMTU(accountingDefaultMTU) })
		for kind := range 4 {
			stopping.Go(func() { ledger.read(wg, kind) })
		}
		stopping.Wait()
	}

	final := startAccountingBackendWithMTU(t, k, iface, users, mtu)
	current.Store(final)
	close(stop)
	readers.Wait()
	for kind := range 8 {
		ledger.read(final, kind)
	}
	ledger.read(final, 0)
	ledger.read(final, 2)

	if produced := k.producedBytes(); produced == 0 || ledger.userBytes.Load() != produced {
		t.Fatalf("per-user reads reported %d bytes, the peers counted %d", ledger.userBytes.Load(), produced)
	}
	producedRx, producedTx := k.producedInterfaceBytes()
	if producedRx == 0 || ledger.uplink.Load() != producedRx || ledger.downlink.Load() != producedTx {
		t.Fatalf("interface reads reported uplink %d downlink %d, the interface received %d and sent %d", ledger.uplink.Load(), ledger.downlink.Load(), producedRx, producedTx)
	}
	if mtu != nil && k.currentMTU() != *mtu {
		t.Fatalf("the final interface runs at mtu %d, want %d", k.currentMTU(), *mtu)
	}
	if sets := k.takeMTUSets(); (mtu == nil) != (len(sets) == 0) {
		t.Fatalf("mtu sets %v with mtu %v", sets, mtu)
	}
}
