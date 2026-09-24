//go:build integration

package wireguard

import (
	"context"
	"fmt"
	"testing"

	"github.com/vishvananda/netlink"
)

const kernelDefaultWireGuardMTU = 1420

func kernelLinkMTU(t *testing.T, iface string) int {
	t.Helper()

	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatalf("link %s: %v", iface, err)
	}
	return link.Attrs().MTU
}

func setKernelLinkMTU(t *testing.T, iface string, mtu int) {
	t.Helper()

	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatalf("link %s: %v", iface, err)
	}
	if err := netlink.LinkSetMTU(link, mtu); err != nil {
		t.Fatalf("set mtu %d on %s: %v", mtu, iface, err)
	}
}

func expectKernelLinkMTU(t *testing.T, iface, step string, want int) {
	t.Helper()

	if got := kernelLinkMTU(t, iface); got != want {
		t.Fatalf("after %s %s runs at mtu %d, want %d", step, iface, got, want)
	}
}

func expectKernelLinkGone(t *testing.T, iface string) {
	t.Helper()

	if _, err := netlink.LinkByName(iface); err == nil {
		t.Fatalf("%s still exists after Shutdown", iface)
	}
}

func TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue(t *testing.T) {
	const (
		iface  = "wg-e2e-mtu0"
		wgPort = 51941
	)
	ctx := context.Background()

	for _, mtuField := range []string{"", `"mtu":null,`} {
		wg := createTestWireGuard(t, fmt.Sprintf(`{"interface_name":"%s","listen_port":%d,%s"private_key":"%s"}`, iface, wgPort, mtuField, mustGeneratePrivateKey(t)))
		expectKernelLinkMTU(t, iface, "New", kernelDefaultWireGuardMTU)

		if err := wg.Restart(); err != nil {
			t.Fatalf("Restart failed: %v", err)
		}
		expectKernelLinkMTU(t, iface, "Restart", kernelDefaultWireGuardMTU)

		setKernelLinkMTU(t, iface, 1340)
		if err := wg.Restart(); err != nil {
			t.Fatalf("Restart failed: %v", err)
		}
		if err := wg.SyncUsers(ctx, nil); err != nil {
			t.Fatalf("SyncUsers failed: %v", err)
		}
		if err := wg.UpdateUsersAndRestart(ctx, nil); err != nil {
			t.Fatalf("UpdateUsersAndRestart failed: %v", err)
		}
		expectKernelLinkMTU(t, iface, "Restart and syncs after a host set the mtu", 1340)

		wg.Shutdown()
		takeAccounting(iface)
		expectKernelLinkGone(t, iface)
	}
}

func TestIntegrationMTUFromConfigIsAppliedAndReapplied(t *testing.T) {
	const (
		iface  = "wg-e2e-mtu1"
		wgPort = 51942
	)

	wg := createTestWireGuard(t, fmt.Sprintf(`{"interface_name":"%s","listen_port":%d,"mtu":1340,"private_key":"%s"}`, iface, wgPort, mustGeneratePrivateKey(t)))
	t.Cleanup(func() {
		wg.Shutdown()
		takeAccounting(iface)
	})
	expectKernelLinkMTU(t, iface, "New", 1340)

	setKernelLinkMTU(t, iface, kernelDefaultWireGuardMTU)
	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	expectKernelLinkMTU(t, iface, "Restart after the mtu drifted", 1340)

	setKernelLinkMTU(t, iface, 1340)
	if err := wg.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	expectKernelLinkMTU(t, iface, "Restart with a host guard at the same mtu", 1340)

	wg.Shutdown()
	expectKernelLinkGone(t, iface)
}
