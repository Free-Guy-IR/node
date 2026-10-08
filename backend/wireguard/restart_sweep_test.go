package wireguard

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	nodeconfig "github.com/pasarguard/node/config"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const sweepInterface = "wg-sweep"

type deviceEvent struct {
	restart bool
	peers   []string
}

type deviceModel struct {
	mu         sync.Mutex
	peers      map[string]struct{}
	events     []deviceEvent
	closed     bool
	afterClose int
}

func (d *deviceModel) configure(cfg wgtypes.Config) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		d.afterClose++
	}
	if cfg.ReplacePeers {
		d.peers = make(map[string]struct{})
	}
	for _, peer := range cfg.Peers {
		if peer.Remove {
			delete(d.peers, peer.PublicKey.String())
			continue
		}
		d.peers[peer.PublicKey.String()] = struct{}{}
	}
	d.events = append(d.events, deviceEvent{restart: cfg.PrivateKey != nil, peers: d.sortedPeersLocked()})
	return nil
}

func (d *deviceModel) sortedPeersLocked() []string {
	keys := make([]string, 0, len(d.peers))
	for key := range d.peers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (d *deviceModel) close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return nil
}

func (d *deviceModel) snapshot() ([]deviceEvent, []string, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.events), d.sortedPeersLocked(), d.afterClose
}

func (d *deviceModel) eventCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.events)
}

func newSweepUsers(t *testing.T, count int) []*common.User {
	t.Helper()

	users := make([]*common.User, 0, count)
	for i := range count {
		_, publicKey, err := GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, &common.User{
			Email:    fmt.Sprintf("%d.sweep", i),
			Inbounds: []string{sweepInterface},
			Proxies: &common.Proxy{
				Wireguard: &common.Wireguard{PublicKey: publicKey, PeerIps: []string{fmt.Sprintf("10.93.0.%d/32", i+2)}},
			},
		})
	}
	return users
}

func newSweepWireGuard(t *testing.T, users []*common.User) (*WireGuard, *deviceModel) {
	t.Helper()

	privateKey, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	wgCfg := mustNewWireGuardConfigForLifecycle(t, fmt.Sprintf(`{
		"interface_name":%q,
		"listen_port":51820,
		"address":["10.93.0.1/24"],
		"private_key":%q
	}`, sweepInterface, privateKey))

	device := &deviceModel{peers: make(map[string]struct{})}
	managerFactory := func(string) (*Manager, error) {
		return &Manager{
			iFaceName: sweepInterface,
			client: &fakeWGClient{
				configureDeviceFn: func(_ string, cfg wgtypes.Config) error { return device.configure(cfg) },
				closeFn:           device.close,
			},
			nl: mockNetlinkOps{
				parseAddrFn: func(_ string) (*netlink.Addr, error) { return nil, nil },
				linkAddFn:   func(_ netlink.Link) error { return nil },
				linkByName: func(name string) (netlink.Link, error) {
					return &netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: name}}, nil
				},
				addrAddFn:   func(_ netlink.Link, _ *netlink.Addr) error { return nil },
				linkSetUpFn: func(_ netlink.Link) error { return nil },
				linkDelFn:   func(_ netlink.Link) error { return nil },
			},
		}, nil
	}

	wg, err := newWithManagerFactory(&nodeconfig.Config{
		LogBufferSize:               64,
		StatsUpdateIntervalSeconds:  3600,
		StatsCleanupIntervalSeconds: 3600,
	}, wgCfg, users, managerFactory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wg.Shutdown)
	return wg, device
}

func TestWireGuardRestartAfterShutdownIsRefusedAndConfiguresNothing(t *testing.T) {
	users := newSweepUsers(t, 3)
	wg, device := newSweepWireGuard(t, []*common.User{users[0], users[1]})

	wg.Shutdown()
	before := device.eventCount()

	if err := wg.Restart(); err == nil {
		t.Error("a restart after shutdown must be refused")
	}
	if err := wg.UpdateUsersAndRestart(context.Background(), []*common.User{users[2]}); err == nil {
		t.Error("an update-and-restart after shutdown must be refused")
	}
	if err := wg.SyncUsers(context.Background(), []*common.User{users[2]}); err == nil {
		t.Error("a full sync after shutdown must be refused")
	}
	if err := wg.SyncUser(context.Background(), users[2]); err == nil {
		t.Error("a single sync after shutdown must be refused")
	}

	if after := device.eventCount(); after != before {
		t.Fatalf("the device was configured %d time(s) after shutdown", after-before)
	}
	if _, _, afterClose := device.snapshot(); afterClose != 0 {
		t.Fatalf("the closed device was configured %d time(s)", afterClose)
	}
	if wg.Started() {
		t.Fatal("the backend reports started after shutdown")
	}
}

func TestWireGuardRestartsRacingSyncsAndShutdownStopAtShutdown(t *testing.T) {
	users := newSweepUsers(t, 6)
	wg, device := newSweepWireGuard(t, []*common.User{users[0]})
	ctx := context.Background()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed>>7))
	shutdownAfter := time.Duration(rng.Int64N(int64(40 * time.Millisecond)))

	stop := make(chan struct{})
	var workers sync.WaitGroup
	worker := func(step func(i int)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				step(i)
			}
		}()
	}
	worker(func(int) { _ = wg.Restart() })
	worker(func(i int) { _ = wg.SyncUser(ctx, users[i%len(users)]) })
	worker(func(i int) {
		subset := []*common.User{users[i%len(users)], users[(i+2)%len(users)]}
		_ = wg.SyncUsers(ctx, subset)
	})
	worker(func(i int) {
		_ = wg.UpdateUsers(ctx, []*common.User{{Email: users[i%len(users)].GetEmail()}})
	})
	worker(func(i int) { _ = wg.UpdateUsersAndRestart(ctx, []*common.User{users[(i+1)%len(users)]}) })

	time.Sleep(shutdownAfter)
	wg.Shutdown()
	atShutdown := device.eventCount()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	workers.Wait()

	if after := device.eventCount(); after != atShutdown {
		t.Fatalf("the device was configured %d time(s) after Shutdown returned", after-atShutdown)
	}
	if _, _, afterClose := device.snapshot(); afterClose != 0 {
		t.Fatalf("the closed device was configured %d time(s)", afterClose)
	}
	if wg.Started() {
		t.Fatal("the backend reports started after shutdown")
	}
	if err := wg.Restart(); err == nil {
		t.Fatal("a restart after shutdown must be refused")
	}
}

func (wg *WireGuard) committedPeerKeys() []string {
	keys := make([]string, 0)
	for _, peer := range wg.peerStore.GetAll() {
		keys = append(keys, peer.PublicKey.String())
	}
	sort.Strings(keys)
	return keys
}

func fireRestarts(wg *WireGuard, rng *rand.Rand, count int, within time.Duration) <-chan error {
	delays := make([]time.Duration, count)
	for i := range delays {
		delays[i] = time.Duration(rng.Int64N(int64(within)))
	}
	errs := make(chan error, count)
	var inner sync.WaitGroup
	for _, delay := range delays {
		inner.Add(1)
		go func() {
			defer inner.Done()
			time.Sleep(delay)
			errs <- wg.Restart()
		}()
	}
	go func() {
		inner.Wait()
		close(errs)
	}()
	return errs
}

func TestWireGuardRestartNeverAppliesAPeerSetOlderThanACommittedSync(t *testing.T) {
	users := newSweepUsers(t, 6)
	wg, device := newSweepWireGuard(t, []*common.User{users[0]})
	ctx := context.Background()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed>>3))

	restartTotal := 0
	for round := range 60 {
		restarts := fireRestarts(wg, rng, 3, 2*time.Millisecond)

		var err error
		switch rng.IntN(3) {
		case 0:
			var subset []*common.User
			for _, user := range users {
				if rng.IntN(2) == 0 {
					subset = append(subset, user)
				}
			}
			err = wg.SyncUsers(ctx, subset)
		case 1:
			err = wg.SyncUser(ctx, users[rng.IntN(len(users))])
		default:
			err = wg.UpdateUsers(ctx, []*common.User{{Email: users[rng.IntN(len(users))].GetEmail()}})
		}
		if err != nil {
			t.Fatalf("round %d sync failed: %v", round, err)
		}
		for e := range restarts {
			if e != nil {
				t.Fatalf("round %d: a health restart failed: %v", round, e)
			}
		}

		_, devicePeers, _ := device.snapshot()
		committed := wg.committedPeerKeys()
		if !slices.Equal(devicePeers, committed) {
			t.Fatalf("round %d: the running device serves %v while the committed peer store holds %v", round, devicePeers, committed)
		}
		restartTotal += 3
	}

	events, _, _ := device.snapshot()
	restartEvents := 0
	for _, event := range events {
		if event.restart {
			restartEvents++
		}
	}
	if restartEvents < restartTotal {
		t.Fatalf("only %d restart configure calls reached the device out of %d restarts fired; restarts are being dropped", restartEvents, restartTotal)
	}
}
