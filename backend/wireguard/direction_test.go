package wireguard

import (
	"context"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	pkgstats "github.com/pasarguard/node/pkg/stats"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type directionCounters struct {
	peerReceived int64
	peerSent     int64
	ifaceRx      uint64
	ifaceTx      uint64
}

func directionWireGuard(t *testing.T, counters *directionCounters) (*WireGuard, string) {
	t.Helper()
	_, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	key, err := wgtypes.ParseKey(pub)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}

	manager := &Manager{
		iFaceName: "wg-dir",
		client: &fakeWGClient{
			deviceFn: func(_ string) (*wgtypes.Device, error) {
				return &wgtypes.Device{Peers: []wgtypes.Peer{{
					PublicKey:         key,
					LastHandshakeTime: time.Now(),
					ReceiveBytes:      counters.peerReceived,
					TransmitBytes:     counters.peerSent,
				}}}, nil
			},
		},
		nl: mockNetlinkOps{
			linkByName: func(name string) (netlink.Link, error) {
				return &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{
					Name:       name,
					Statistics: &netlink.LinkStatistics{RxBytes: counters.ifaceRx, TxBytes: counters.ifaceTx},
				}}, nil
			},
		},
	}

	peerStore := NewPeerStore()
	peerStore.Init([]*PeerInfo{{Email: "dir@example.com", PublicKey: key}})

	return &WireGuard{
		config:         &Config{InterfaceName: "wg-dir"},
		cfg:            &config.Config{},
		manager:        manager,
		peerStore:      peerStore,
		statsTracker:   pkgstats.New(),
		interfaceStats: pkgstats.NewInterfaceCountersTracker(),
		state:          lifecycleRunning,
	}, "dir@example.com"
}

func directionRead(t *testing.T, wg *WireGuard, request *common.StatRequest) (map[string]int64, map[string]int64) {
	t.Helper()
	resp, err := wg.GetStats(context.Background(), request)
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

func TestWireGuardUserUplinkIsWhatThePeerSent(t *testing.T) {
	counters := &directionCounters{peerReceived: 10, peerSent: 90}
	wg, email := directionWireGuard(t, counters)

	wg.updateConnectedPeers(context.Background())

	got, sum := directionRead(t, wg, &common.StatRequest{Type: common.StatType_UserStat, Name: email})
	if got["uplink"] != 10 || got["downlink"] != 90 {
		t.Fatalf("UserStat: peer sent 10 and received 90, got %v", got)
	}
	if sum[email] != 100 {
		t.Fatalf("UserStat: per-user sum %v, want 100", sum)
	}

	got, sum = directionRead(t, wg, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if got["uplink"] != 10 || got["downlink"] != 90 {
		t.Fatalf("UsersStat: peer sent 10 and received 90, got %v", got)
	}
	if sum[email] != 100 {
		t.Fatalf("UsersStat: per-user sum %v, want 100", sum)
	}
}

func TestWireGuardInterfaceUplinkIsWhatClientsSent(t *testing.T) {
	counters := &directionCounters{ifaceRx: 5000, ifaceTx: 7000}
	wg, _ := directionWireGuard(t, counters)

	directionRead(t, wg, &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true})

	counters.ifaceRx += 10
	counters.ifaceTx += 90

	got, sum := directionRead(t, wg, &common.StatRequest{Type: common.StatType_Outbound, Name: "wg-dir"})
	if got["uplink"] != 10 || got["downlink"] != 90 {
		t.Fatalf("Outbound: interface rx=10 tx=90, got %v", got)
	}
	if sum["wg-dir"] != 100 {
		t.Fatalf("Outbound: sum %v, want 100", sum)
	}

	got, sum = directionRead(t, wg, &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true})
	if got["uplink"] != 10 || got["downlink"] != 90 {
		t.Fatalf("Outbounds: interface rx=10 tx=90, got %v", got)
	}
	if sum["wg-dir"] != 100 {
		t.Fatalf("Outbounds: sum %v, want 100", sum)
	}
}
