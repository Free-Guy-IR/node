package wireguard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func mtuValue(mtu int) *int {
	return &mtu
}

func expectMTUSets(t *testing.T, k *accountingKernel, step string, want ...int) {
	t.Helper()

	if got := k.takeMTUSets(); !slices.Equal(got, want) {
		t.Fatalf("%s set the interface mtu to %v, want %v", step, got, want)
	}
}

func expectCurrentMTU(t *testing.T, k *accountingKernel, step string, want int) {
	t.Helper()

	if got := k.currentMTU(); got != want {
		t.Fatalf("after %s the interface mtu is %d, want %d", step, got, want)
	}
}

func expectOps(t *testing.T, k *accountingKernel, step string, want ...string) {
	t.Helper()

	if got := k.takeOps(); !slices.Equal(got, want) {
		t.Fatalf("%s touched the link with %q, want %q", step, got, want)
	}
}

func TestWireGuardConfigWithoutMTUTouchesTheLinkAsBefore(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgmtu-absent"
	privateKey, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	wgCfg := mustNewWireGuardConfigForLifecycle(t, fmt.Sprintf(`{
		"interface_name":"%s",
		"listen_port":51820,
		"address":["127.0.0.1/8"],
		"private_key":"%s"
	}`, iface, privateKey))
	users := []*common.User{accountingUser(iface, "absent@example.com", mustGeneratePublicKey(t), 2)}
	extra := accountingUser(iface, "extra@example.com", mustGeneratePublicKey(t), 3)
	ctx := context.Background()

	wg, err := newWithManagerFactory(accountingNodeConfig(), wgCfg, users, k.managerFactory)
	if err != nil {
		t.Fatalf("failed to start wireguard backend: %v", err)
	}
	t.Cleanup(func() {
		wg.Shutdown()
		takeAccounting(iface)
	})
	expectOps(t, k, "New", "parseAddr", "linkByName", "linkAdd", "configure", "linkByName", "addrAdd", "linkSetUp")

	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	expectOps(t, k, "Restart", "configure")

	if err := wg.SyncUsers(ctx, append(slices.Clone(users), extra)); err != nil {
		t.Fatalf("SyncUsers failed: %v", err)
	}
	expectOps(t, k, "SyncUsers", "configure")

	if err := wg.UpdateUsersAndRestart(ctx, []*common.User{accountingUserWithoutInbound(extra.GetEmail(), extra.GetProxies().GetWireguard().GetPublicKey(), 3)}); err != nil {
		t.Fatalf("UpdateUsersAndRestart failed: %v", err)
	}
	expectOps(t, k, "UpdateUsersAndRestart", "configure", "configure")

	tick(wg)
	expectOps(t, k, "tick")

	if _, err := wg.GetStats(ctx, &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}); err != nil {
		t.Fatalf("Outbounds read failed: %v", err)
	}
	expectOps(t, k, "Outbounds read", "linkByName")

	wg.Shutdown()
	expectOps(t, k, "Shutdown", "linkByName", "close", "linkByName", "linkDel")

	if mtu := k.currentMTU(); mtu != 0 {
		t.Fatalf("the deleted interface still reports mtu %d", mtu)
	}
}

func TestWireGuardConfigMTUIsOptionalAndValidated(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		want    *int
		wantErr bool
	}{
		{name: "absent"},
		{name: "null", field: `,"mtu":null`},
		{name: "lowest", field: `,"mtu":576`, want: mtuValue(576)},
		{name: "production", field: `,"mtu":1340`, want: mtuValue(1340)},
		{name: "highest", field: `,"mtu":9000`, want: mtuValue(9000)},
		{name: "below range", field: `,"mtu":575`, wantErr: true},
		{name: "above range", field: `,"mtu":9001`, wantErr: true},
		{name: "zero", field: `,"mtu":0`, wantErr: true},
		{name: "negative", field: `,"mtu":-1`, wantErr: true},
		{name: "string", field: `,"mtu":"1340"`, wantErr: true},
		{name: "fraction", field: `,"mtu":1340.5`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := NewConfig(`{"interface_name":"wgmtu-cfg"` + tc.field + `}`)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected %s to be rejected, got mtu %v", tc.field, cfg.MTU)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected %q to be accepted: %v", tc.field, err)
			}
			if (cfg.MTU == nil) != (tc.want == nil) || (cfg.MTU != nil && *cfg.MTU != *tc.want) {
				t.Fatalf("parsed mtu %v, want %v", cfg.MTU, tc.want)
			}
		})
	}
}

func TestWireGuardMTUIsAppliedAtNewBeforeTheInterfaceComesUp(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgmtu-new"
	startAccountingBackendWithMTU(t, k, iface, []*common.User{accountingUser(iface, "new@example.com", mustGeneratePublicKey(t), 2)}, mtuValue(1340))

	expectOps(t, k, "New", "linkByName", "linkAdd", "configure", "linkByName", "linkByName", "setMTU", "linkSetUp")
	expectMTUSets(t, k, "New", 1340)
	expectCurrentMTU(t, k, "New", 1340)
}

func TestWireGuardMTUIsReappliedAtRestart(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgmtu-restart"
	ctx := context.Background()
	extraKey := mustGeneratePublicKey(t)
	users := []*common.User{accountingUser(iface, "restart@example.com", mustGeneratePublicKey(t), 2)}
	wg := startAccountingBackendWithMTU(t, k, iface, users, mtuValue(1340))
	k.takeOps()
	expectMTUSets(t, k, "New", 1340)

	k.setExternalMTU(accountingDefaultMTU)
	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	expectOps(t, k, "Restart", "linkByName", "setMTU", "configure")
	expectMTUSets(t, k, "Restart", 1340)
	expectCurrentMTU(t, k, "Restart", 1340)

	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	expectOps(t, k, "Restart at the configured mtu", "linkByName", "configure")
	expectMTUSets(t, k, "Restart at the configured mtu")

	k.setExternalMTU(1500)
	if err := wg.UpdateUsersAndRestart(ctx, []*common.User{accountingUser(iface, "extra@example.com", extraKey, 3)}); err != nil {
		t.Fatalf("UpdateUsersAndRestart failed: %v", err)
	}
	expectMTUSets(t, k, "UpdateUsersAndRestart", 1340)
	expectCurrentMTU(t, k, "UpdateUsersAndRestart", 1340)

	k.setExternalMTU(1500)
	if err := wg.SyncUsers(ctx, users); err != nil {
		t.Fatalf("SyncUsers failed: %v", err)
	}
	expectMTUSets(t, k, "SyncUsers")
	expectCurrentMTU(t, k, "SyncUsers", 1500)
}

func TestWireGuardWithoutMTULeavesAnExternallySetMTUAlone(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgmtu-guard"
	ctx := context.Background()
	users := []*common.User{accountingUser(iface, "guard@example.com", mustGeneratePublicKey(t), 2)}
	extra := accountingUser(iface, "guard-extra@example.com", mustGeneratePublicKey(t), 3)

	wg := startAccountingBackend(t, k, iface, users)
	expectCurrentMTU(t, k, "New", accountingDefaultMTU)
	k.setExternalMTU(1340)

	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	if err := wg.SyncUsers(ctx, append(slices.Clone(users), extra)); err != nil {
		t.Fatalf("SyncUsers failed: %v", err)
	}
	if err := wg.UpdateUsersAndRestart(ctx, users); err != nil {
		t.Fatalf("UpdateUsersAndRestart failed: %v", err)
	}
	tick(wg)

	expectMTUSets(t, k, "a lifecycle without an mtu key")
	expectCurrentMTU(t, k, "a lifecycle without an mtu key", 1340)
	if ops := k.takeOps(); slices.Contains(ops, "setMTU") {
		t.Fatalf("a lifecycle without an mtu key set the mtu: %q", ops)
	}
}

func newMTUManager(t *testing.T, k *accountingKernel, interfaceName string) (*Manager, wgtypes.Config) {
	t.Helper()

	privateKey, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	key, err := wgtypes.ParseKey(privateKey)
	if err != nil {
		t.Fatalf("failed to parse private key: %v", err)
	}
	manager, err := k.managerFactory(interfaceName)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	listenPort := 51820
	return manager, wgtypes.Config{PrivateKey: &key, ListenPort: &listenPort}
}

func TestManagerMTUDefaultIsCapturedAtTheFirstOverrideAndRestoredWhenCleared(t *testing.T) {
	k := newAccountingKernel()
	manager, config := newMTUManager(t, k, "wgmtu-mgr")

	if err := manager.initializeWithPeers(*config.PrivateKey, *config.ListenPort, nil, nil, nil); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	expectMTUSets(t, k, "initialize without an mtu")
	k.takeOps()

	k.setExternalMTU(1500)
	if err := manager.applyConfig(config, nil); err != nil {
		t.Fatalf("applyConfig failed: %v", err)
	}
	expectOps(t, k, "applyConfig without an mtu", "configure")
	expectCurrentMTU(t, k, "applyConfig without an mtu", 1500)

	setAndConfigure := []string{"linkByName", "setMTU", "configure"}
	steps := []struct {
		name string
		mtu  *int
		ops  []string
		sets []int
		want int
	}{
		{name: "first override", mtu: mtuValue(1340), ops: setAndConfigure, sets: []int{1340}, want: 1340},
		{name: "second override", mtu: mtuValue(1300), ops: setAndConfigure, sets: []int{1300}, want: 1300},
		{name: "same override", mtu: mtuValue(1300), ops: []string{"linkByName", "configure"}, want: 1300},
		{name: "override cleared", ops: setAndConfigure, sets: []int{1500}, want: 1500},
		{name: "cleared again", ops: []string{"configure"}, want: 1500},
		{name: "override again", mtu: mtuValue(1280), ops: setAndConfigure, sets: []int{1280}, want: 1280},
		{name: "cleared once more", ops: setAndConfigure, sets: []int{1500}, want: 1500},
	}
	for _, step := range steps {
		if err := manager.applyConfig(config, step.mtu); err != nil {
			t.Fatalf("%s: applyConfig failed: %v", step.name, err)
		}
		expectOps(t, k, step.name, step.ops...)
		expectMTUSets(t, k, step.name, step.sets...)
		expectCurrentMTU(t, k, step.name, step.want)
	}
}

func TestManagerMTUOverrideAtInitializeIsRestoredToTheKernelDefault(t *testing.T) {
	k := newAccountingKernel()
	manager, config := newMTUManager(t, k, "wgmtu-mgr-init")

	if err := manager.initializeWithPeers(*config.PrivateKey, *config.ListenPort, nil, nil, mtuValue(1280)); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	expectMTUSets(t, k, "initialize with an mtu", 1280)

	if err := manager.applyConfig(config, nil); err != nil {
		t.Fatalf("applyConfig failed: %v", err)
	}
	expectMTUSets(t, k, "override cleared", accountingDefaultMTU)
	expectCurrentMTU(t, k, "override cleared", accountingDefaultMTU)
}

func TestManagerRejectsAnInvalidMTUBeforeTouchingTheLink(t *testing.T) {
	for _, mtu := range []int{575, 9001} {
		t.Run(fmt.Sprint(mtu), func(t *testing.T) {
			k := newAccountingKernel()
			manager, config := newMTUManager(t, k, "wgmtu-mgr-bad")

			if err := manager.initializeWithPeers(*config.PrivateKey, *config.ListenPort, nil, nil, mtuValue(mtu)); err == nil {
				t.Fatal("expected initialize to reject the mtu")
			}
			expectOps(t, k, "initialize with an invalid mtu")
			if k.linkExists() {
				t.Fatal("initialize with an invalid mtu left an interface behind")
			}

			if err := manager.initializeWithPeers(*config.PrivateKey, *config.ListenPort, nil, nil, nil); err != nil {
				t.Fatalf("initialize failed: %v", err)
			}
			k.takeOps()
			if err := manager.applyConfig(config, mtuValue(mtu)); err == nil {
				t.Fatal("expected applyConfig to reject the mtu")
			}
			expectOps(t, k, "applyConfig with an invalid mtu")
			expectCurrentMTU(t, k, "applyConfig with an invalid mtu", accountingDefaultMTU)
		})
	}
}

func TestWireGuardInvalidMTUFailsNewWithoutAnInterfaceAndKeepsTheHandover(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgmtu-handover"
	key := mustGeneratePublicKey(t)
	users := []*common.User{accountingUser(iface, "mtu@example.com", key, 2)}
	nodeRequest := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	first := startAccountingBackend(t, k, iface, users)
	k.setPeer(t, key, 1000, 400, time.Now(), loopbackEndpoint())
	k.addInterfaceTraffic(t, 600, 300)
	tick(first)
	first.Shutdown()
	k.takeOps()

	outOfRange := accountingConfig(t, iface, nil)
	outOfRange.MTU = mtuValue(100)
	factoryCalls := 0
	countingFactory := func(name string) (*Manager, error) {
		factoryCalls++
		return k.managerFactory(name)
	}
	if _, err := newWithManagerFactory(accountingNodeConfig(), outOfRange, users, countingFactory); err == nil {
		t.Fatal("expected New to reject an mtu outside 576-9000")
	}
	if factoryCalls != 0 {
		t.Fatalf("New created %d managers for an invalid mtu", factoryCalls)
	}
	expectOps(t, k, "New with an invalid mtu")

	k.failNextSetMTU(errors.New("mtu refused"))
	_, err := newAccountingBackendWithMTU(t, k, iface, users, mtuValue(1340))
	if err == nil || !strings.Contains(err.Error(), "MTU") {
		t.Fatalf("expected New to fail on the refused mtu, got %v", err)
	}
	k.failNextSetMTU(nil)
	if k.linkExists() {
		t.Fatal("New left an interface behind after the kernel refused the mtu")
	}

	second := startAccountingBackendWithMTU(t, k, iface, users, mtuValue(1340))
	expectCurrentMTU(t, k, "New", 1340)
	k.setPeer(t, key, 7, 3, time.Now(), loopbackEndpoint())
	k.addInterfaceTraffic(t, 5, 6)
	tick(second)
	expectUserTraffic(t, readAccountingStats(t, second), "mtu@example.com", 1007, 403)
	expectInterfaceTraffic(t, second, iface, nodeRequest, 605, 306)
}

func TestWireGuardMTUChangeAtRestartKeepsEveryCounter(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgmtu-counters"
	key := mustGeneratePublicKey(t)
	users := []*common.User{accountingUser(iface, "counters@example.com", key, 2)}
	nodeRequest := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	wg := startAccountingBackendWithMTU(t, k, iface, users, mtuValue(1340))
	k.takeMTUSets()

	k.setPeer(t, key, 1000, 400, time.Now(), loopbackEndpoint())
	k.addInterfaceTraffic(t, 500, 200)
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), "counters@example.com", 1000, 400)
	expectInterfaceTraffic(t, wg, iface, nodeRequest, 500, 200)

	k.setPeer(t, key, 1300, 500, time.Now(), loopbackEndpoint())
	k.addInterfaceTraffic(t, 30, 20)
	k.setExternalMTU(accountingDefaultMTU)
	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	expectMTUSets(t, k, "Restart", 1340)

	k.setPeer(t, key, 50, 30, time.Now(), loopbackEndpoint())
	k.addInterfaceTraffic(t, 7, 3)
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), "counters@example.com", 300+50, 100+30)
	expectInterfaceTraffic(t, wg, iface, nodeRequest, 37, 23)

	k.setPeer(t, key, 80, 40, time.Now(), loopbackEndpoint())
	k.addInterfaceTraffic(t, 11, 13)
	k.setExternalMTU(accountingDefaultMTU)
	k.failNextSetMTU(errors.New("mtu refused"))
	k.takeOps()
	if err := wg.Restart(); err == nil {
		t.Fatal("expected Restart to fail when the kernel refuses the mtu")
	}
	k.failNextSetMTU(nil)
	expectOps(t, k, "Restart refused at the mtu", "linkByName", "setMTU")

	k.setPeer(t, key, 90, 45, time.Now(), loopbackEndpoint())
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), "counters@example.com", 40, 15)
	expectInterfaceTraffic(t, wg, iface, nodeRequest, 11, 13)
}
