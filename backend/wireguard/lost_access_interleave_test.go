package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type allowedIPDevice struct {
	mu       sync.Mutex
	linkUp   bool
	peers    map[wgtypes.Key]*allowedIPPeer
	produced int64
}

type allowedIPPeer struct {
	allowed []net.IPNet
	rx      int64
	tx      int64
}

func newAllowedIPDevice() *allowedIPDevice {
	return &allowedIPDevice{peers: make(map[wgtypes.Key]*allowedIPPeer)}
}

func (d *allowedIPDevice) ConfigureDevice(name string, cfg wgtypes.Config) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.linkUp {
		return fmt.Errorf("device %s does not exist", name)
	}
	if cfg.ReplacePeers {
		d.peers = make(map[wgtypes.Key]*allowedIPPeer)
	}
	for _, peerConfig := range cfg.Peers {
		if peerConfig.Remove {
			delete(d.peers, peerConfig.PublicKey)
			continue
		}
		peer, exists := d.peers[peerConfig.PublicKey]
		if !exists {
			if peerConfig.UpdateOnly {
				continue
			}
			peer = &allowedIPPeer{}
			d.peers[peerConfig.PublicKey] = peer
		}
		if peerConfig.ReplaceAllowedIPs {
			peer.allowed = nil
		}
		for _, allowed := range peerConfig.AllowedIPs {
			for _, other := range d.peers {
				other.allowed = slices.DeleteFunc(other.allowed, func(held net.IPNet) bool {
					return held.String() == allowed.String()
				})
			}
			peer.allowed = append(peer.allowed, allowed)
		}
	}
	return nil
}

func (d *allowedIPDevice) Device(name string) (*wgtypes.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.linkUp {
		return nil, fmt.Errorf("device %s does not exist", name)
	}
	device := &wgtypes.Device{Name: name}
	for key, peer := range d.peers {
		peer.rx += 7
		peer.tx += 13
		d.produced += 20
		device.Peers = append(device.Peers, wgtypes.Peer{
			PublicKey:     key,
			ReceiveBytes:  peer.rx,
			TransmitBytes: peer.tx,
			AllowedIPs:    slices.Clone(peer.allowed),
		})
	}
	return device, nil
}

func (d *allowedIPDevice) Close() error {
	return nil
}

func (d *allowedIPDevice) setLink(up bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.linkUp = up
	d.peers = make(map[wgtypes.Key]*allowedIPPeer)
}

func (d *allowedIPDevice) readLink(name string) (netlink.Link, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.linkUp {
		return nil, linkNotFoundError()
	}
	return &netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{
		Name:       name,
		MTU:        accountingDefaultMTU,
		Statistics: &netlink.LinkStatistics{},
	}}, nil
}

func (d *allowedIPDevice) managerFactory(interfaceName string) (*Manager, error) {
	return &Manager{
		client:    d,
		iFaceName: interfaceName,
		nl: mockNetlinkOps{
			parseAddrFn: func(string) (*netlink.Addr, error) { return &netlink.Addr{}, nil },
			linkAddFn: func(netlink.Link) error {
				d.setLink(true)
				return nil
			},
			linkByName:  d.readLink,
			addrAddFn:   func(netlink.Link, *netlink.Addr) error { return nil },
			linkSetUpFn: func(netlink.Link) error { return nil },
			linkDelFn: func(netlink.Link) error {
				d.setLink(false)
				return nil
			},
		},
	}, nil
}

func (d *allowedIPDevice) allowedIPsByKey() map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()

	result := make(map[string][]string, len(d.peers))
	for key, peer := range d.peers {
		result[key.String()] = sortedIPNets(peer.allowed)
	}
	return result
}

func (d *allowedIPDevice) producedBytes() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.produced
}

func sortedIPNets(ipNets []net.IPNet) []string {
	ips := make([]string, 0, len(ipNets))
	for _, ipNet := range ipNets {
		ips = append(ips, ipNet.String())
	}
	sort.Strings(ips)
	return ips
}

type lostAccessPanel struct {
	order sync.RWMutex
	mu    sync.Mutex
	iface string
	users map[string]*common.User
	keys  map[string]struct{}
}

type lostAccessPeer struct {
	email string
	ips   []string
}

func newLostAccessPanel(iface string) *lostAccessPanel {
	return &lostAccessPanel{
		iface: iface,
		users: make(map[string]*common.User),
		keys:  make(map[string]struct{}),
	}
}

func (p *lostAccessPanel) set(users ...*common.User) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, user := range users {
		p.users[user.GetEmail()] = user
		if key := user.GetProxies().GetWireguard().GetPublicKey(); key != "" {
			p.keys[key] = struct{}{}
		}
	}
}

func (p *lostAccessPanel) snapshot() []*common.User {
	p.mu.Lock()
	defer p.mu.Unlock()

	users := make([]*common.User, 0, len(p.users))
	for _, user := range p.users {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].GetEmail() < users[j].GetEmail() })
	return users
}

func (p *lostAccessPanel) desired() (map[string]lostAccessPeer, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	desired := make(map[string]lostAccessPeer)
	for email, user := range p.users {
		wireguard := user.GetProxies().GetWireguard()
		if !slices.Contains(user.GetInbounds(), p.iface) || wireguard.GetPublicKey() == "" || len(wireguard.GetPeerIps()) == 0 {
			continue
		}
		ips := slices.Clone(wireguard.GetPeerIps())
		sort.Strings(ips)
		desired[wireguard.GetPublicKey()] = lostAccessPeer{email: email, ips: ips}
	}
	keys := make([]string, 0, len(p.keys))
	for key := range p.keys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return desired, keys
}

type lostAccessStep struct {
	group int
	lost  *common.User
	next  *common.User
	send  int
}

func lostAccessHolder(iface, email, publicKey, peerIP string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{iface},
		Proxies: &common.Proxy{
			Wireguard: &common.Wireguard{PublicKey: publicKey, PeerIps: []string{peerIP}},
		},
	}
}

func lostAccessShape(iface string, holder *common.User, shape int) *common.User {
	email := holder.GetEmail()
	wireguard := holder.GetProxies().GetWireguard()
	switch shape % 4 {
	case 0:
		return &common.User{
			Email:    email,
			Inbounds: []string{iface},
			Proxies:  &common.Proxy{Wireguard: &common.Wireguard{PublicKey: wireguard.GetPublicKey(), PeerIps: []string{}}},
		}
	case 1:
		return &common.User{Email: email, Inbounds: []string{iface}, Proxies: &common.Proxy{}}
	case 2:
		return &common.User{Email: email, Inbounds: []string{iface}}
	default:
		return &common.User{
			Email: email,
			Proxies: &common.Proxy{Wireguard: &common.Wireguard{
				PublicKey: wireguard.GetPublicKey(),
				PeerIps:   slices.Clone(wireguard.GetPeerIps()),
			}},
		}
	}
}

func planLostAccess(t *testing.T, iface string, holders []*common.User, steps int) [][]lostAccessStep {
	t.Helper()

	plan := make([][]lostAccessStep, steps)
	current := slices.Clone(holders)
	for step := range steps {
		for group, holder := range current {
			wireguard := holder.GetProxies().GetWireguard()
			publicKey := wireguard.GetPublicKey()
			if group%2 == 1 {
				publicKey = mustGeneratePublicKey(t)
			}
			next := lostAccessHolder(iface, fmt.Sprintf("g%d-s%d@example.com", group, step+1), publicKey, wireguard.GetPeerIps()[0])
			plan[step] = append(plan[step], lostAccessStep{
				group: group,
				lost:  lostAccessShape(iface, holder, group+step),
				next:  next,
				send:  (group/2 + step) % 4,
			})
			current[group] = next
		}
	}
	return plan
}

func sendLostAccessStep(ctx context.Context, wg *WireGuard, step lostAccessStep) error {
	switch step.send {
	case 0:
		if err := wg.SyncUser(ctx, step.lost); err != nil {
			return err
		}
		return wg.SyncUser(ctx, step.next)
	case 1:
		return wg.UpdateUsers(ctx, []*common.User{step.next, step.lost})
	default:
		return wg.UpdateUsersAndRestart(ctx, []*common.User{step.lost, step.next})
	}
}

func checkLostAccessSyncError(t *testing.T, label string, wg *WireGuard, err error, tolerateStopped bool) {
	if err == nil {
		return
	}
	if tolerateStopped && errors.Is(err, errWireGuardNotStarted) && !wg.Started() {
		return
	}
	t.Errorf("%s: %v", label, err)
}

func driveLostAccessStep(t *testing.T, workers *sync.WaitGroup, panel *lostAccessPanel, steps []lostAccessStep, target func() *WireGuard, label string, tolerateStopped bool) {
	ctx := context.Background()
	var chunked []lostAccessStep
	for _, step := range steps {
		if step.send == 2 {
			chunked = append(chunked, step)
			continue
		}
		workers.Go(func() {
			panel.order.RLock()
			defer panel.order.RUnlock()

			panel.set(step.lost, step.next)
			wg := target()
			err := sendLostAccessStep(ctx, wg, step)
			checkLostAccessSyncError(t, fmt.Sprintf("%s group %d send %d", label, step.group, step.send), wg, err, tolerateStopped)
		})
	}
	if len(chunked) == 0 {
		return
	}
	workers.Go(func() {
		panel.order.RLock()
		defer panel.order.RUnlock()

		users := make([]*common.User, 0, 2*len(chunked))
		for _, step := range chunked {
			panel.set(step.lost, step.next)
			users = append(users, step.lost, step.next)
		}
		wg := target()
		checkLostAccessSyncError(t, label+" chunked update", wg, wg.UpdateUsers(ctx, users), tolerateStopped)
	})
}

func expectLostAccessConverged(t *testing.T, label string, wg *WireGuard, device *allowedIPDevice, panel *lostAccessPanel) {
	t.Helper()

	desired, seenKeys := panel.desired()
	var problems []string

	deviceIPs := make(map[string][]string)
	for key, ips := range device.allowedIPsByKey() {
		want, ok := desired[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("device holds peer %s with %v, which no user should have", key, ips))
		case !slices.Equal(ips, want.ips):
			problems = append(problems, fmt.Sprintf("device peer %s of %s holds %v, want %v", key, want.email, ips, want.ips))
		}
		for _, ip := range ips {
			deviceIPs[ip] = append(deviceIPs[ip], key)
		}
	}

	storeIPs := make(map[string][]string)
	storeKeys := make(map[string]struct{})
	storeEmails := make(map[string]string)
	for _, peer := range wg.peerStore.GetAll() {
		key := peer.PublicKey.String()
		ips := sortedIPNets(peer.AllowedIPs)
		storeKeys[key] = struct{}{}
		want, ok := desired[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("peer store holds %s for %s with %v, which no user should have", key, peer.Email, ips))
		case peer.Email != want.email || !slices.Equal(ips, want.ips):
			problems = append(problems, fmt.Sprintf("peer store has %s as %s %v, want %s %v", key, peer.Email, ips, want.email, want.ips))
		}
		if other, dup := storeEmails[peer.Email]; dup {
			problems = append(problems, fmt.Sprintf("peer store gives %s two keys, %s and %s", peer.Email, other, key))
		}
		storeEmails[peer.Email] = key
		for _, ip := range ips {
			storeIPs[ip] = append(storeIPs[ip], peer.Email)
		}
	}

	devicePeers := device.allowedIPsByKey()
	for key, want := range desired {
		if _, ok := devicePeers[key]; !ok {
			problems = append(problems, fmt.Sprintf("device is missing peer %s of %s", key, want.email))
		}
		if _, ok := storeKeys[key]; !ok {
			problems = append(problems, fmt.Sprintf("peer store is missing %s of %s", key, want.email))
		}
		if peer := wg.peerStore.GetByEmail(want.email); peer == nil || peer.PublicKey.String() != key {
			problems = append(problems, fmt.Sprintf("peer store does not map %s to %s", want.email, key))
		}
	}
	for ip, holders := range deviceIPs {
		if len(holders) > 1 {
			problems = append(problems, fmt.Sprintf("device gives %s to %d peers: %v", ip, len(holders), holders))
		}
	}
	for ip, holders := range storeIPs {
		if len(holders) > 1 {
			problems = append(problems, fmt.Sprintf("peer store gives %s to %d users: %v", ip, len(holders), holders))
		}
	}

	tick(wg)
	entries := wg.statsTracker.GetStatsEntries(seenKeys)
	for _, key := range seenKeys {
		entry := entries[key]
		want, live := desired[key]
		switch {
		case live && (entry == nil || entry.IsDeleted || entry.Email != want.email):
			problems = append(problems, fmt.Sprintf("stats of live peer %s of %s are %+v", key, want.email, entry))
		case !live && entry != nil && !entry.IsDeleted:
			problems = append(problems, fmt.Sprintf("stats of removed peer %s of %s are not marked deleted", key, entry.Email))
		}
	}

	sort.Strings(problems)
	for _, problem := range problems {
		t.Errorf("%s: %s", label, problem)
	}
	if len(problems) > 0 {
		t.FailNow()
	}
}

func TestLosingWireGuardAccessConvergesUnderConcurrentSyncsFullSyncsAndStarts(t *testing.T) {
	const (
		iface  = "wg99-lost"
		groups = 8
		rounds = 6
	)

	device := newAllowedIPDevice()
	panel := newLostAccessPanel(iface)

	privateKey, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	wgConfig := mustNewWireGuardConfigForLifecycle(t, fmt.Sprintf(`{
		"interface_name":"%s",
		"listen_port":51820,
		"address":["10.99.0.1/24"],
		"private_key":"%s"
	}`, iface, privateKey))

	for n := range 3 {
		panel.set(lostAccessHolder(iface, fmt.Sprintf("stable%d@example.com", n), mustGeneratePublicKey(t), fmt.Sprintf("10.99.0.%d/32", 200+n)))
	}
	holders := make([]*common.User, 0, groups)
	for group := range groups {
		holders = append(holders, lostAccessHolder(iface, fmt.Sprintf("g%d-s0@example.com", group), mustGeneratePublicKey(t), fmt.Sprintf("10.99.0.%d/32", 10+group)))
	}
	panel.set(holders...)
	plan := planLostAccess(t, iface, holders, 3*rounds)

	var instancesMu sync.Mutex
	var instances []*WireGuard
	t.Cleanup(func() {
		instancesMu.Lock()
		defer instancesMu.Unlock()
		for _, wg := range instances {
			wg.Shutdown()
		}
		takeAccounting(iface)
	})
	start := func() (*WireGuard, error) {
		wg, err := newWithManagerFactory(accountingNodeConfig(), wgConfig, panel.snapshot(), device.managerFactory)
		if err != nil {
			return nil, err
		}
		instancesMu.Lock()
		instances = append(instances, wg)
		instancesMu.Unlock()
		return wg, nil
	}

	first, err := start()
	if err != nil {
		t.Fatalf("failed to start wireguard backend: %v", err)
	}
	var current atomic.Pointer[WireGuard]
	current.Store(first)
	expectLostAccessConverged(t, "first start", first, device, panel)

	var reported atomic.Int64
	read := func(wg *WireGuard) {
		resp, err := wg.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
		if err != nil {
			return
		}
		reported.Add(sumTraffic(resp))
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	var stopOnce sync.Once
	stopReaders := func() {
		stopOnce.Do(func() {
			close(stop)
			readers.Wait()
		})
	}
	defer stopReaders()
	for range 2 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				read(current.Load())
			}
		})
	}

	ctx := context.Background()
	for round := range rounds {
		wg := current.Load()

		var workers sync.WaitGroup
		label := fmt.Sprintf("round %d with full syncs", round)
		driveLostAccessStep(t, &workers, panel, plan[3*round], func() *WireGuard { return wg }, label, false)
		workers.Go(func() {
			for range 3 {
				panel.order.Lock()
				err := wg.SyncUsers(ctx, panel.snapshot())
				panel.order.Unlock()
				if err != nil {
					t.Errorf("%s: full sync: %v", label, err)
				}
			}
		})
		workers.Go(func() {
			for range 3 {
				if err := wg.Restart(); err != nil {
					t.Errorf("%s: restart: %v", label, err)
				}
			}
		})
		workers.Go(func() {
			for range 5 {
				tick(wg)
			}
		})
		workers.Wait()
		expectLostAccessConverged(t, label, wg, device, panel)

		label = fmt.Sprintf("round %d with partial syncs only", round)
		driveLostAccessStep(t, &workers, panel, plan[3*round+1], func() *WireGuard { return wg }, label, false)
		workers.Go(func() {
			for range 2 {
				if err := wg.Restart(); err != nil {
					t.Errorf("%s: restart: %v", label, err)
				}
			}
		})
		workers.Go(func() {
			for range 5 {
				tick(wg)
			}
		})
		workers.Wait()
		expectLostAccessConverged(t, label, wg, device, panel)

		label = fmt.Sprintf("round %d across a start", round)
		driveLostAccessStep(t, &workers, panel, plan[3*round+2], current.Load, label, true)
		workers.Go(func() {
			wg.Shutdown()
			panel.order.Lock()
			defer panel.order.Unlock()
			next, err := start()
			if err != nil {
				t.Errorf("%s: start failed: %v", label, err)
				return
			}
			current.Store(next)
		})
		workers.Go(func() {
			panel.order.Lock()
			target := current.Load()
			err := target.SyncUsers(ctx, panel.snapshot())
			panel.order.Unlock()
			checkLostAccessSyncError(t, label+" full sync", target, err, true)
		})
		workers.Go(func() {
			checkLostAccessSyncError(t, label+" restart", wg, wg.Restart(), true)
		})
		workers.Go(func() {
			for range 3 {
				tick(wg)
			}
		})
		workers.Wait()
		if current.Load() == wg {
			t.Fatalf("%s: the backend was not replaced", label)
		}
		expectLostAccessConverged(t, label, current.Load(), device, panel)
	}

	stopReaders()
	final := current.Load()
	read(final)
	read(final)
	if produced := device.producedBytes(); produced == 0 || reported.Load() != produced {
		t.Fatalf("reported %d bytes, the device counted %d", reported.Load(), produced)
	}
}
