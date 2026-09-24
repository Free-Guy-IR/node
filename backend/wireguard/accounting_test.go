package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	nodeconfig "github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/stats"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type accountingKernel struct {
	mu                   sync.Mutex
	linkUp               bool
	peers                map[wgtypes.Key]*wgtypes.Peer
	failConfigure        error
	deviceReads          int
	growPerRead          int64
	produced             int64
	interfaceRx          int64
	interfaceTx          int64
	growInterfacePerRead int64
	producedInterfaceRx  int64
	producedInterfaceTx  int64
	linkReadGate         *linkReadGate
}

type linkReadGate struct {
	entered chan struct{}
	release chan struct{}
}

func newAccountingKernel() *accountingKernel {
	return &accountingKernel{peers: make(map[wgtypes.Key]*wgtypes.Peer)}
}

func (k *accountingKernel) ConfigureDevice(_ string, cfg wgtypes.Config) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.failConfigure != nil {
		return k.failConfigure
	}
	if cfg.ReplacePeers {
		k.peers = make(map[wgtypes.Key]*wgtypes.Peer)
	}
	for _, peerConfig := range cfg.Peers {
		if peerConfig.Remove {
			delete(k.peers, peerConfig.PublicKey)
			continue
		}
		if _, exists := k.peers[peerConfig.PublicKey]; !exists {
			k.peers[peerConfig.PublicKey] = &wgtypes.Peer{PublicKey: peerConfig.PublicKey}
		}
	}
	return nil
}

func (k *accountingKernel) Device(name string) (*wgtypes.Device, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.deviceReads++
	if !k.linkUp {
		return nil, fmt.Errorf("device %s does not exist", name)
	}
	device := &wgtypes.Device{Name: name}
	if k.growInterfacePerRead > 0 {
		k.interfaceRx += k.growInterfacePerRead
		k.interfaceTx += 3 * k.growInterfacePerRead
		k.producedInterfaceRx += k.growInterfacePerRead
		k.producedInterfaceTx += 3 * k.growInterfacePerRead
	}
	for _, peer := range k.peers {
		if k.growPerRead > 0 {
			peer.ReceiveBytes += k.growPerRead
			peer.TransmitBytes += 2 * k.growPerRead
			peer.LastHandshakeTime = time.Now()
			k.produced += 3 * k.growPerRead
		}
		device.Peers = append(device.Peers, *peer)
	}
	return device, nil
}

func (k *accountingKernel) producedBytes() int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.produced
}

func (k *accountingKernel) Close() error {
	return nil
}

func (k *accountingKernel) resetLink(up bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.linkUp = up
	k.peers = make(map[wgtypes.Key]*wgtypes.Peer)
	k.interfaceRx, k.interfaceTx = 0, 0
}

func (k *accountingKernel) addInterfaceTraffic(t *testing.T, rx, tx int64) {
	t.Helper()

	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.linkUp {
		t.Fatal("traffic on an interface that does not exist")
	}
	k.interfaceRx += rx
	k.interfaceTx += tx
	k.producedInterfaceRx += rx
	k.producedInterfaceTx += tx
}

func (k *accountingKernel) producedInterfaceBytes() (int64, int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.producedInterfaceRx, k.producedInterfaceTx
}

func (k *accountingKernel) gateNextLinkRead() *linkReadGate {
	gate := &linkReadGate{entered: make(chan struct{}), release: make(chan struct{})}
	k.mu.Lock()
	k.linkReadGate = gate
	k.mu.Unlock()
	return gate
}

func (k *accountingKernel) readLink(name string) (netlink.Link, error) {
	k.mu.Lock()
	gate := k.linkReadGate
	k.linkReadGate = nil
	if !k.linkUp {
		k.mu.Unlock()
		return nil, linkNotFoundError()
	}
	link := &netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{
		Name:       name,
		Statistics: &netlink.LinkStatistics{RxBytes: uint64(k.interfaceRx), TxBytes: uint64(k.interfaceTx)},
	}}
	k.mu.Unlock()

	if gate != nil {
		close(gate.entered)
		<-gate.release
	}
	return link, nil
}

func (k *accountingKernel) netlinkOps() mockNetlinkOps {
	return mockNetlinkOps{
		parseAddrFn: func(string) (*netlink.Addr, error) {
			return &netlink.Addr{}, nil
		},
		linkAddFn: func(netlink.Link) error {
			k.resetLink(true)
			return nil
		},
		linkByName: k.readLink,
		addrAddFn: func(netlink.Link, *netlink.Addr) error {
			return nil
		},
		linkSetUpFn: func(netlink.Link) error {
			return nil
		},
		linkDelFn: func(netlink.Link) error {
			k.resetLink(false)
			return nil
		},
	}
}

func (k *accountingKernel) managerFactory(interfaceName string) (*Manager, error) {
	return &Manager{
		client:    k,
		iFaceName: interfaceName,
		nl:        k.netlinkOps(),
	}, nil
}

func (k *accountingKernel) setPeer(t *testing.T, publicKey string, rx, tx int64, handshake time.Time, endpoint *net.UDPAddr) {
	t.Helper()

	key, err := wgtypes.ParseKey(publicKey)
	if err != nil {
		t.Fatalf("failed to parse public key: %v", err)
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	peer, ok := k.peers[key]
	if !ok {
		t.Fatalf("peer %s is not configured on the fake device", publicKey)
	}
	peer.ReceiveBytes = rx
	peer.TransmitBytes = tx
	peer.LastHandshakeTime = handshake
	peer.Endpoint = endpoint
}

func (k *accountingKernel) hasPeer(t *testing.T, publicKey string) bool {
	t.Helper()

	key, err := wgtypes.ParseKey(publicKey)
	if err != nil {
		t.Fatalf("failed to parse public key: %v", err)
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.peers[key]
	return ok
}

func (k *accountingKernel) reads() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.deviceReads
}

func (k *accountingKernel) failNextConfigure(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.failConfigure = err
}

func accountingPeerIP(n int) string {
	ipNet := net.IPNet{IP: net.IPv4(127, 0, 0, byte(n)).To4(), Mask: net.CIDRMask(32, 32)}
	return ipNet.String()
}

func accountingUser(interfaceName, email, publicKey string, n int) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{interfaceName},
		Proxies: &common.Proxy{
			Wireguard: &common.Wireguard{
				PublicKey: publicKey,
				PeerIps:   []string{accountingPeerIP(n)},
			},
		},
	}
}

func accountingUserWithoutInbound(email, publicKey string, n int) *common.User {
	user := accountingUser("", email, publicKey, n)
	user.Inbounds = nil
	return user
}

func accountingNodeConfig() *nodeconfig.Config {
	return &nodeconfig.Config{
		LogBufferSize:               64,
		StatsUpdateIntervalSeconds:  3600,
		StatsCleanupIntervalSeconds: 3600,
	}
}

func newAccountingBackend(t *testing.T, k *accountingKernel, interfaceName string, users []*common.User) (*WireGuard, error) {
	t.Helper()

	privateKey, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}

	wgCfg := mustNewWireGuardConfigForLifecycle(t, fmt.Sprintf(`{
		"interface_name":"%s",
		"listen_port":51820,
		"private_key":"%s"
	}`, interfaceName, privateKey))

	wg, err := newWithManagerFactory(accountingNodeConfig(), wgCfg, users, k.managerFactory)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		wg.Shutdown()
		takeAccounting(interfaceName)
	})
	return wg, nil
}

func startAccountingBackend(t *testing.T, k *accountingKernel, interfaceName string, users []*common.User) *WireGuard {
	t.Helper()

	wg, err := newAccountingBackend(t, k, interfaceName, users)
	if err != nil {
		t.Fatalf("failed to start wireguard backend: %v", err)
	}
	return wg
}

func readAccountingStats(t *testing.T, wg *WireGuard) *common.StatResponse {
	t.Helper()

	resp, err := wg.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if err != nil {
		t.Fatalf("UsersStat read failed: %v", err)
	}
	return resp
}

func expectUserTraffic(t *testing.T, resp *common.StatResponse, email string, want ...int64) {
	t.Helper()

	wantSorted := make([]int64, 0, len(want))
	var wantTotal int64
	for _, value := range want {
		wantTotal += value
		if value != 0 {
			wantSorted = append(wantSorted, value)
		}
	}
	slices.Sort(wantSorted)

	got := make([]int64, 0, 2)
	var gotTotal int64
	for _, stat := range resp.GetStats() {
		if stat.GetName() == email {
			got = append(got, stat.GetValue())
			gotTotal += stat.GetValue()
		}
	}
	slices.Sort(got)

	if gotTotal != wantTotal || !slices.Equal(got, wantSorted) {
		t.Fatalf("%s: reported %v (total %d), want %v (total %d)", email, got, gotTotal, wantSorted, wantTotal)
	}
}

func expectNoTraffic(t *testing.T, resp *common.StatResponse) {
	t.Helper()

	if len(resp.GetStats()) != 0 {
		t.Fatalf("expected no traffic, got %v", resp.GetStats())
	}
}

func loopbackEndpoint() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51000}
}

func otherLoopbackEndpoint() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv6loopback, Port: 51001}
}

func tick(wg *WireGuard) {
	wg.updateConnectedPeers(context.Background())
}

func TestAccountingSamplesPeersWhoseHandshakeIsOlderThanTheOnlineWindow(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-stale"
	staleKey := mustGeneratePublicKey(t)
	neverKey := mustGeneratePublicKey(t)

	wg := startAccountingBackend(t, k, iface, []*common.User{
		accountingUser(iface, "stale@example.com", staleKey, 2),
		accountingUser(iface, "never@example.com", neverKey, 3),
	})

	k.setPeer(t, staleKey, 700, 300, time.Now().Add(-2*time.Minute), loopbackEndpoint())
	k.setPeer(t, neverKey, 90, 10, time.Time{}, nil)
	tick(wg)

	resp := readAccountingStats(t, wg)
	expectUserTraffic(t, resp, "stale@example.com", 700, 300)
	expectUserTraffic(t, resp, "never@example.com", 90, 10)
}

func TestAccountingQuietPeersKeepTheirOnlineAnswers(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-online"
	activeKey := mustGeneratePublicKey(t)
	staleKey := mustGeneratePublicKey(t)
	neverKey := mustGeneratePublicKey(t)

	wg := startAccountingBackend(t, k, iface, []*common.User{
		accountingUser(iface, "active@example.com", activeKey, 2),
		accountingUser(iface, "stale@example.com", staleKey, 3),
		accountingUser(iface, "never@example.com", neverKey, 4),
	})
	ctx := context.Background()

	k.setPeer(t, activeKey, 100, 40, time.Now(), loopbackEndpoint())
	tick(wg)

	online, err := wg.GetUserOnlineStats(ctx, "active@example.com")
	if err != nil || online.GetValue() != 1 {
		t.Fatalf("expected the active peer online, got %v (err %v)", online.GetValue(), err)
	}
	ipsBefore, err := wg.GetUserOnlineIpListStats(ctx, "active@example.com")
	if err != nil {
		t.Fatalf("online ip list failed: %v", err)
	}

	k.setPeer(t, activeKey, 900, 400, time.Now().Add(-2*time.Minute), otherLoopbackEndpoint())
	k.setPeer(t, staleKey, 500, 200, time.Now().Add(-2*time.Minute), otherLoopbackEndpoint())
	k.setPeer(t, neverKey, 90, 10, time.Time{}, nil)
	tick(wg)

	ipsAfter, err := wg.GetUserOnlineIpListStats(ctx, "active@example.com")
	if err != nil {
		t.Fatalf("online ip list failed: %v", err)
	}
	if len(ipsAfter.GetIps()) != len(ipsBefore.GetIps()) {
		t.Fatalf("quiet sample changed the online ip list from %v to %v", ipsBefore.GetIps(), ipsAfter.GetIps())
	}
	for ip, seenAt := range ipsBefore.GetIps() {
		if ipsAfter.GetIps()[ip] != seenAt {
			t.Fatalf("quiet sample changed the online ip list from %v to %v", ipsBefore.GetIps(), ipsAfter.GetIps())
		}
	}
	online, err = wg.GetUserOnlineStats(ctx, "active@example.com")
	if err != nil || online.GetValue() != 1 {
		t.Fatalf("quiet sample changed the online state of a peer active seconds ago: %v (err %v)", online.GetValue(), err)
	}

	for _, email := range []string{"stale@example.com", "never@example.com"} {
		online, err := wg.GetUserOnlineStats(ctx, email)
		if err != nil || online.GetValue() != 0 {
			t.Fatalf("%s: expected offline, got %v (err %v)", email, online.GetValue(), err)
		}
		ips, err := wg.GetUserOnlineIpListStats(ctx, email)
		if err != nil || len(ips.GetIps()) != 0 {
			t.Fatalf("%s: expected no online ips, got %v (err %v)", email, ips.GetIps(), err)
		}
	}
}

func TestAccountingPartialSyncRemovalReportsUnsampledGrowth(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-remove"
	key := mustGeneratePublicKey(t)

	wg := startAccountingBackend(t, k, iface, []*common.User{accountingUser(iface, "removed@example.com", key, 2)})

	k.setPeer(t, key, 100, 40, time.Now(), loopbackEndpoint())
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), "removed@example.com", 100, 40)

	k.setPeer(t, key, 400, 240, time.Now(), loopbackEndpoint())

	if err := wg.SyncUser(context.Background(), accountingUserWithoutInbound("removed@example.com", key, 2)); err != nil {
		t.Fatalf("SyncUser failed: %v", err)
	}
	if k.hasPeer(t, key) {
		t.Fatal("expected the peer to be removed from the device")
	}

	expectUserTraffic(t, readAccountingStats(t, wg), "removed@example.com", 300, 200)
	expectNoTraffic(t, readAccountingStats(t, wg))
}

func TestAccountingPeerReplacementKeepsTheTailAndCountsFromZero(t *testing.T) {
	cases := []struct {
		name    string
		replace func(wg *WireGuard, users []*common.User, extra *common.User) error
	}{
		{
			name: "full sync",
			replace: func(wg *WireGuard, users []*common.User, extra *common.User) error {
				return wg.SyncUsers(context.Background(), append(slices.Clone(users), extra))
			},
		},
		{
			name: "restart",
			replace: func(wg *WireGuard, _ []*common.User, _ *common.User) error {
				return wg.Restart()
			},
		},
		{
			name: "update users and restart",
			replace: func(wg *WireGuard, _ []*common.User, extra *common.User) error {
				return wg.UpdateUsersAndRestart(context.Background(), []*common.User{extra})
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := newAccountingKernel()
			iface := fmt.Sprintf("wgacct-repl%d", i)
			bigKey := mustGeneratePublicKey(t)
			smallKey := mustGeneratePublicKey(t)
			extraKey := mustGeneratePublicKey(t)

			users := []*common.User{
				accountingUser(iface, "big@example.com", bigKey, 2),
				accountingUser(iface, "small@example.com", smallKey, 3),
			}
			wg := startAccountingBackend(t, k, iface, users)

			k.setPeer(t, bigKey, 1000, 400, time.Now(), loopbackEndpoint())
			k.setPeer(t, smallKey, 10, 30, time.Now(), loopbackEndpoint())
			tick(wg)
			readAccountingStats(t, wg)

			k.setPeer(t, bigKey, 1300, 500, time.Now(), loopbackEndpoint())
			k.setPeer(t, smallKey, 15, 32, time.Now(), loopbackEndpoint())

			if err := tc.replace(wg, users, accountingUser(iface, "extra@example.com", extraKey, 4)); err != nil {
				t.Fatalf("replace failed: %v", err)
			}

			k.setPeer(t, bigKey, 5000, 2000, time.Now(), loopbackEndpoint())
			k.setPeer(t, smallKey, 3, 7, time.Now(), loopbackEndpoint())
			tick(wg)

			resp := readAccountingStats(t, wg)
			expectUserTraffic(t, resp, "big@example.com", 300+5000, 100+2000)
			expectUserTraffic(t, resp, "small@example.com", 5+3, 2+7)

			k.setPeer(t, bigKey, 5100, 2050, time.Now(), loopbackEndpoint())
			tick(wg)
			resp = readAccountingStats(t, wg)
			expectUserTraffic(t, resp, "big@example.com", 100, 50)
			expectUserTraffic(t, resp, "small@example.com")
		})
	}
}

func TestAccountingFailedReplacementRestartsNothing(t *testing.T) {
	cases := []struct {
		name    string
		replace func(wg *WireGuard, users []*common.User, extra *common.User) error
	}{
		{
			name: "full sync",
			replace: func(wg *WireGuard, users []*common.User, extra *common.User) error {
				return wg.SyncUsers(context.Background(), append(slices.Clone(users), extra))
			},
		},
		{
			name: "restart",
			replace: func(wg *WireGuard, _ []*common.User, _ *common.User) error {
				return wg.Restart()
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := newAccountingKernel()
			iface := fmt.Sprintf("wgacct-fail%d", i)
			key := mustGeneratePublicKey(t)
			extraKey := mustGeneratePublicKey(t)

			users := []*common.User{accountingUser(iface, "kept@example.com", key, 2)}
			wg := startAccountingBackend(t, k, iface, users)

			k.setPeer(t, key, 1000, 400, time.Now(), loopbackEndpoint())
			tick(wg)
			readAccountingStats(t, wg)

			k.setPeer(t, key, 1300, 500, time.Now(), loopbackEndpoint())
			k.failNextConfigure(errors.New("configure refused"))
			if err := tc.replace(wg, users, accountingUser(iface, "extra@example.com", extraKey, 3)); err == nil {
				t.Fatal("expected the replacement to fail")
			}
			k.failNextConfigure(nil)

			k.setPeer(t, key, 1400, 550, time.Now(), loopbackEndpoint())
			tick(wg)
			expectUserTraffic(t, readAccountingStats(t, wg), "kept@example.com", 400, 150)
		})
	}
}

func TestAccountingHandoverAcrossShutdownAndNew(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-handover"
	keptKey := mustGeneratePublicKey(t)
	droppedKey := mustGeneratePublicKey(t)

	first := startAccountingBackend(t, k, iface, []*common.User{
		accountingUser(iface, "kept@example.com", keptKey, 2),
		accountingUser(iface, "dropped@example.com", droppedKey, 3),
	})

	k.setPeer(t, keptKey, 1000, 400, time.Now(), loopbackEndpoint())
	k.setPeer(t, droppedKey, 70, 30, time.Now(), loopbackEndpoint())
	tick(first)
	k.setPeer(t, keptKey, 1500, 600, time.Now().Add(-2*time.Minute), loopbackEndpoint())

	first.Shutdown()

	second := startAccountingBackend(t, k, iface, []*common.User{
		accountingUser(iface, "kept@example.com", keptKey, 2),
	})

	k.setPeer(t, keptKey, 20, 10, time.Now(), loopbackEndpoint())
	tick(second)
	tick(first)

	resp := readAccountingStats(t, second)
	expectUserTraffic(t, resp, "kept@example.com", 1500+20, 600+10)
	expectUserTraffic(t, resp, "dropped@example.com", 70, 30)

	expectNoTraffic(t, readAccountingStats(t, second))
	if entries := second.statsTracker.GetStatsEntries([]string{droppedKey}); len(entries) != 0 {
		t.Fatal("expected the entry of a peer that is no longer configured to be released after it was reported")
	}

	k.setPeer(t, keptKey, 50, 30, time.Now(), loopbackEndpoint())
	tick(second)
	expectUserTraffic(t, readAccountingStats(t, second), "kept@example.com", 30, 20)
}

func TestAccountingOldBackendCannotTouchTheAdoptedTracker(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-late"
	key := mustGeneratePublicKey(t)
	user := accountingUser(iface, "late@example.com", key, 2)
	ctx := context.Background()

	first := startAccountingBackend(t, k, iface, []*common.User{user})
	k.setPeer(t, key, 1000, 400, time.Now(), loopbackEndpoint())
	tick(first)
	first.Shutdown()

	second := startAccountingBackend(t, k, iface, []*common.User{user})
	if first.statsTracker == second.statsTracker {
		t.Fatal("the stopped backend still holds the tracker the new backend adopted")
	}

	if err := first.SyncUser(ctx, accountingUserWithoutInbound("late@example.com", key, 2)); err == nil {
		t.Fatal("expected a sync on the stopped backend to fail")
	}
	if err := first.SyncUsers(ctx, nil); err == nil {
		t.Fatal("expected a full sync on the stopped backend to fail")
	}
	if err := first.UpdateUsers(ctx, []*common.User{accountingUserWithoutInbound("late@example.com", key, 2)}); err == nil {
		t.Fatal("expected an update on the stopped backend to fail")
	}
	if err := first.Restart(); err == nil {
		t.Fatal("expected a restart of the stopped backend to fail")
	}
	tick(first)
	first.cleanupOfflineUsers(ctx)
	first.statsTracker.RemoveStats(key)
	first.statsTracker.UpdateStatsBatch([]stats.Sample{{PublicKey: key, Email: "late@example.com", Rx: 5000, Tx: 5000}})

	k.setPeer(t, key, 20, 10, time.Now(), loopbackEndpoint())
	tick(second)
	expectUserTraffic(t, readAccountingStats(t, second), "late@example.com", 1020, 410)
}

func TestAccountingHandoverIsKeyedByInterfaceName(t *testing.T) {
	kA := newAccountingKernel()
	kB := newAccountingKernel()
	ifaceA := "wgacct-key-a"
	ifaceB := "wgacct-key-b"
	key := mustGeneratePublicKey(t)

	first := startAccountingBackend(t, kA, ifaceA, []*common.User{accountingUser(ifaceA, "keyed@example.com", key, 2)})
	kA.setPeer(t, key, 800, 200, time.Now(), loopbackEndpoint())
	tick(first)
	first.Shutdown()

	other := startAccountingBackend(t, kB, ifaceB, []*common.User{accountingUser(ifaceB, "keyed@example.com", key, 2)})
	kB.setPeer(t, key, 5, 6, time.Now(), loopbackEndpoint())
	tick(other)
	expectUserTraffic(t, readAccountingStats(t, other), "keyed@example.com", 5, 6)

	second := startAccountingBackend(t, kA, ifaceA, []*common.User{accountingUser(ifaceA, "keyed@example.com", key, 2)})
	expectUserTraffic(t, readAccountingStats(t, second), "keyed@example.com", 800, 200)
}

func TestAccountingHandoverSurvivesAFailedNew(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-failnew"
	key := mustGeneratePublicKey(t)
	users := []*common.User{accountingUser(iface, "survivor@example.com", key, 2)}

	first := startAccountingBackend(t, k, iface, users)
	k.setPeer(t, key, 1000, 400, time.Now(), loopbackEndpoint())
	tick(first)
	first.Shutdown()

	k.failNextConfigure(errors.New("configure refused"))
	if _, err := newAccountingBackend(t, k, iface, users); err == nil {
		t.Fatal("expected the backend start to fail")
	}
	k.failNextConfigure(nil)

	second := startAccountingBackend(t, k, iface, users)
	k.setPeer(t, key, 7, 3, time.Now(), loopbackEndpoint())
	tick(second)
	expectUserTraffic(t, readAccountingStats(t, second), "survivor@example.com", 1007, 403)
}

func TestAccountingSamplerWaitsForARunningSync(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-serial"
	key := mustGeneratePublicKey(t)

	wg := startAccountingBackend(t, k, iface, []*common.User{accountingUser(iface, "serial@example.com", key, 2)})
	k.setPeer(t, key, 100, 40, time.Now(), loopbackEndpoint())

	wg.syncMu.Lock()
	readsBefore := k.reads()
	done := make(chan struct{})
	go func() {
		tick(wg)
		close(done)
	}()

	select {
	case <-done:
		wg.syncMu.Unlock()
		t.Fatal("the sampler ran while a user sync held the peer list")
	case <-time.After(150 * time.Millisecond):
	}
	if k.reads() != readsBefore {
		wg.syncMu.Unlock()
		t.Fatal("the sampler read the device while a user sync held the peer list")
	}

	wg.syncMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sampler did not run after the user sync finished")
	}
	expectUserTraffic(t, readAccountingStats(t, wg), "serial@example.com", 100, 40)
}

func TestAccountingShutdownWaitsForARunningSync(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-shutwait"
	key := mustGeneratePublicKey(t)

	wg := startAccountingBackend(t, k, iface, []*common.User{accountingUser(iface, "wait@example.com", key, 2)})

	wg.syncMu.Lock()
	done := make(chan struct{})
	go func() {
		wg.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		wg.syncMu.Unlock()
		t.Fatal("Shutdown ran while a user sync held the peer list")
	case <-time.After(150 * time.Millisecond):
	}

	wg.syncMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not finish after the user sync finished")
	}
}

func sumTraffic(resp *common.StatResponse) int64 {
	var total int64
	for _, stat := range resp.GetStats() {
		total += stat.GetValue()
	}
	return total
}

func TestAccountingConcurrentSamplingSyncsAndShutdown(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-race"
	keys := []string{mustGeneratePublicKey(t), mustGeneratePublicKey(t), mustGeneratePublicKey(t)}
	users := make([]*common.User, 0, len(keys))
	for i, key := range keys {
		users = append(users, accountingUser(iface, fmt.Sprintf("race%d@example.com", i), key, i+2))
	}

	wg := startAccountingBackend(t, k, iface, users)
	ctx := context.Background()
	k.mu.Lock()
	k.growPerRead = 7
	k.mu.Unlock()

	var reportedMu sync.Mutex
	var reported int64
	readAndCount := func() {
		resp, err := wg.GetStats(ctx, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
		if err != nil {
			return
		}
		reportedMu.Lock()
		reported += sumTraffic(resp)
		reportedMu.Unlock()
	}

	var workers sync.WaitGroup
	run := func(fn func()) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 25 {
				fn()
			}
		}()
	}
	run(func() { tick(wg) })
	run(func() { _ = wg.SyncUsers(ctx, users) })
	run(func() { _ = wg.SyncUsers(ctx, users[:2]) })
	run(func() { _ = wg.Restart() })
	run(func() { _ = wg.SyncUser(ctx, users[2]) })
	run(func() {
		_ = wg.UpdateUsers(ctx, []*common.User{accountingUserWithoutInbound(users[1].GetEmail(), keys[1], 3)})
	})
	run(func() { _ = wg.UpdateUsers(ctx, []*common.User{users[1]}) })
	run(readAndCount)
	run(func() { wg.cleanupOfflineUsers(ctx) })

	finished := make(chan struct{})
	go func() {
		workers.Wait()
		readAndCount()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("sampling and syncs deadlocked")
	}

	if produced := k.producedBytes(); produced == 0 || reported != produced {
		t.Fatalf("reported %d bytes, the device counted %d", reported, produced)
	}

	shutdown := make(chan struct{})
	go func() {
		tick(wg)
		wg.Shutdown()
		close(shutdown)
	}()
	select {
	case <-shutdown:
	case <-time.After(20 * time.Second):
		t.Fatal("Shutdown deadlocked")
	}
}
