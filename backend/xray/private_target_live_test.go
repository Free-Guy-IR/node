package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const (
	privateGuardHostsRequired = "PG_NODE_PRIVATE_GUARD_REQUIRED"
	systemPrivateName         = "private-target.pgnode.test"
	systemCGNATName           = "cgnat-target.pgnode.test"
	xrayPrivateName           = "dns-private.pgnode.test"
	xrayCGNATName             = "dns-cgnat.pgnode.test"
)

func runXrayConfigTest(binary, dir, name, raw string) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		return err
	}
	out, err := exec.Command(binary, "run", "-test", "-c", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func readsPrivateAllow(t *testing.T, binary string) bool {
	t.Helper()
	dir := t.TempDir()
	if err := runXrayConfigTest(binary, dir, "control.json", `{"outbounds": [{"protocol": "freedom", "settings": {}}]}`); err != nil {
		t.Fatalf("%s refuses a bare freedom outbound, so whether it guards private targets is unknown: %v", binary, err)
	}
	return runXrayConfigTest(binary, dir, "probe.json", `{"outbounds": [{"protocol": "freedom", "settings": {"privateAllow": [{}]}}]}`) != nil
}

func installedXrayBinary() string {
	if path := os.Getenv("XRAY_EXECUTABLE_PATH"); path != "" {
		return path
	}
	return executablePath
}

type guardedBinary struct {
	path    string
	guarded bool
}

func forEachXrayBinaryWithItsGuard(t *testing.T, run func(t *testing.T, binary string, guarded bool)) {
	t.Helper()
	binaries := map[string]guardedBinary{}
	installed := installedXrayBinary()
	if _, err := os.Stat(installed); err == nil {
		binaries["installed"] = guardedBinary{path: installed, guarded: true}
	}
	if stock := os.Getenv("XRAY_STOCK_EXECUTABLE_PATH"); stock != "" && stock != installed {
		binaries["stock"] = guardedBinary{path: stock, guarded: readsPrivateAllow(t, stock)}
	}
	if len(binaries) == 0 {
		t.Skip("no xray binary found")
	}
	for name, binary := range binaries {
		t.Run(name, func(t *testing.T) {
			if !binary.guarded {
				t.Logf("%s does not read privateAllow: expecting the exposure a core without the private-target guard has", binary.path)
			}
			run(t, binary.path, binary.guarded)
		})
	}
}

func TestTheInstalledXrayGuardsPrivateTargets(t *testing.T) {
	installed := installedXrayBinary()
	if _, err := os.Stat(installed); err != nil {
		t.Skipf("no installed xray at %s", installed)
	}
	if !readsPrivateAllow(t, installed) {
		t.Fatalf("%s does not read privateAllow: the xray the node ships must carry the private-target guard", installed)
	}
}

func privateGuardHostOrSkip(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv(privateGuardHostsRequired) == "1" {
		t.Fatalf(format+" (required by "+privateGuardHostsRequired+")", args...)
	}
	t.Skipf(format, args...)
}

func isPrivateTargetAddr(addr netip.Addr) bool {
	for _, prefix := range privateTargetPrefixes {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

func localIPv4Where(match func(netip.Addr) bool) net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLoopback() {
				continue
			}
			parsed, ok := netip.AddrFromSlice(ipNet.IP.To4())
			if ok && match(parsed) && selfReachable(ipNet.IP.String()) {
				return ipNet.IP.To4()
			}
		}
	}
	return nil
}

func cgnatPrefix() netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10)
}

func cgnatHostOrSkip(t *testing.T) net.IP {
	t.Helper()
	ip := localIPv4Where(cgnatPrefix().Contains)
	if ip == nil {
		privateGuardHostOrSkip(t, "this host has no reachable address in %s", cgnatPrefix())
	}
	return ip
}

func publicHostOrSkip(t *testing.T) net.IP {
	t.Helper()
	ip := localIPv4Where(func(addr netip.Addr) bool { return !isPrivateTargetAddr(addr) })
	if ip == nil {
		privateGuardHostOrSkip(t, "this host has no reachable address outside the private set")
	}
	return ip
}

func systemNameOrSkip(t *testing.T, name string, match func(netip.Addr) bool) net.IP {
	t.Helper()
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), name)
	if err == nil {
		for _, addr := range addrs {
			parsed, ok := netip.AddrFromSlice(addr.IP)
			if ok && match(parsed.Unmap()) && selfReachable(addr.IP.String()) {
				return addr.IP.To4()
			}
		}
	}
	privateGuardHostOrSkip(t, "the system resolver gives %s no reachable matching address (%v, %v)", name, addrs, err)
	return nil
}

func privateTargetHost(t *testing.T) net.IP {
	t.Helper()
	if addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), systemPrivateName); err == nil {
		for _, addr := range addrs {
			parsed, ok := netip.AddrFromSlice(addr.IP)
			if ok && isPrivateTargetAddr(parsed.Unmap()) && !parsed.Unmap().IsLoopback() && selfReachable(addr.IP.String()) {
				return addr.IP.To4()
			}
		}
	}
	return net.ParseIP(userTrafficHost(t)).To4()
}

func interfaceOwning(t *testing.T, ip net.IP) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
				return iface.Name
			}
		}
	}
	t.Fatalf("no interface holds %s", ip)
	return ""
}

type countingSource struct {
	addr     *net.TCPAddr
	accepted atomic.Int32
}

func startCountingSource(t *testing.T, ip net.IP) *countingSource {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	source := &countingSource{addr: listener.Addr().(*net.TCPAddr)}
	go func() {
		chunk := make([]byte, 32*1024)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			source.accepted.Add(1)
			go func() {
				defer conn.Close()
				for {
					if _, err := conn.Write(chunk); err != nil {
						return
					}
				}
			}()
		}
	}()
	return source
}

func privateGuardUser(id uuid.UUID, inbounds ...string) *common.User {
	return &common.User{Email: "1.alice", Inbounds: inbounds, Proxies: &common.Proxy{Vless: &common.Vless{Id: id.String()}}}
}

func startPrivateGuardBackend(t *testing.T, binary string, shape map[string]any, users ...*common.User) {
	t.Helper()
	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	xrayConfig, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	back, err := New(context.Background(), xrayConfig, users, netutil.FindFreePort(), netutil.FindFreePort(), &config.Config{
		XrayExecutablePath:  binary,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(back.Shutdown)
	t.Logf("core under test: %s", back.Version())
}

func liveVless(tag string, port int) map[string]any {
	return map[string]any{"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "vless", "settings": map[string]any{"clients": []any{}, "decryption": "none"}}
}

func receivePayload(conn net.Conn, window time.Duration) (int64, error) {
	return receive(&vlessResponseConn{Conn: conn}, window)
}

func requireNothingArrives(t *testing.T, conn net.Conn, source *countingSource, what string) {
	t.Helper()
	received, _ := receivePayload(conn, time.Second)
	if received != 0 {
		t.Errorf("%s: the user received %d bytes from %s", what, received, source.addr)
	}
	if accepted := source.accepted.Load(); accepted != 0 {
		t.Errorf("%s: %s accepted %d connections", what, source.addr, accepted)
	}
}

func requireGuarded(t *testing.T, guarded bool, conn net.Conn, source *countingSource, what string) {
	t.Helper()
	if guarded {
		requireNothingArrives(t, conn, source, what)
		return
	}
	requireDataArrives(t, conn, what+" on a core without the guard")
	if source.accepted.Load() == 0 {
		t.Errorf("%s on a core without the guard: %s accepted no connection", what, source.addr)
	}
}

func requireDataArrives(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	if received, err := receivePayload(conn, time.Second); received == 0 {
		t.Errorf("%s: the user received nothing (err %v)", what, err)
	}
}

func requireRawDataArrives(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	if received, err := receive(conn, time.Second); received == 0 {
		t.Errorf("%s: received nothing (err %v)", what, err)
	}
}

func vlessTo(t *testing.T, port int, id uuid.UUID, target *net.TCPAddr) net.Conn {
	t.Helper()
	return openVlessStream(t, port, id, target)
}

func vlessToName(t *testing.T, port int, id uuid.UUID, name string, targetPort int) net.Conn {
	t.Helper()
	conn, err := dialVlessStreamDomain(port, id, name, targetPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

var coreOneCGNATRule = map[string]any{"type": "field", "ip": []any{"100.64.0.0/10"}, "outboundTag": "DIRECT"}

func coreOneLive(ports [3]int, egressInterface string, hosts map[string]any) map[string]any {
	return coreOneLiveWith(ports, egressInterface, hosts, []any{coreOneCGNATRule}, nil)
}

func coreOneLiveWith(ports [3]int, egressInterface string, hosts map[string]any, beforePrivateBlock, afterPrivateBlock []any) map[string]any {
	rules := []any{
		map[string]any{"type": "field", "port": "25", "outboundTag": "BLOCK"},
		map[string]any{"type": "field", "network": "udp", "port": "443", "ip": []any{"1.1.1.1/32"}, "outboundTag": "BLOCK"},
	}
	rules = append(rules, beforePrivateBlock...)
	rules = append(rules, map[string]any{"type": "field", "ip": []any{"geoip:private"}, "outboundTag": "BLOCK"})
	rules = append(rules, afterPrivateBlock...)
	rules = append(rules,
		map[string]any{"type": "field", "inboundTag": []any{"egress-in"}, "outboundTag": "egress_wireguard"},
		map[string]any{"type": "field", "inboundTag": []any{"direct-in"}, "outboundTag": "DIRECT"},
	)
	return map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"dns":      map[string]any{"hosts": hosts},
		"inbounds": []any{liveVless("VLESS TCP SESSION", ports[0]), liveVless("egress-in", ports[1]), liveVless("direct-in", ports[2])},
		"outbounds": []any{
			map[string]any{"tag": "DIRECT", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIPv4"}},
			map[string]any{"tag": "egress_wireguard", "protocol": "freedom", "settings": map[string]any{}, "streamSettings": map[string]any{"sockopt": map[string]any{"interface": egressInterface}}},
			map[string]any{"tag": "BLOCK", "protocol": "blackhole", "settings": map[string]any{}},
		},
		"routing": map[string]any{"domainStrategy": "IPIfNonMatch", "rules": rules},
	}
}

func TestLiveCoreOneShapeKeepsUsersOffPrivateTargetsAndKeepsCGNATOnDIRECT(t *testing.T) {
	forEachXrayBinaryWithItsGuard(t, coreOneShapeKeepsUsersOffPrivateTargets)
}

func coreOneShapeKeepsUsersOffPrivateTargets(t *testing.T, binary string, guarded bool) {
	privateHost := privateTargetHost(t)
	ports := [3]int{netutil.FindFreePort(), netutil.FindFreePort(), netutil.FindFreePort()}
	id := uuid.New()
	hosts := map[string]any{xrayPrivateName: privateHost.String()}
	cgnat := localIPv4Where(cgnatPrefix().Contains)
	if cgnat != nil {
		hosts[xrayCGNATName] = cgnat.String()
	}
	startPrivateGuardBackend(t, binary, coreOneLive(ports, interfaceOwning(t, privateHost), hosts), privateGuardUser(id, "VLESS TCP SESSION", "egress-in", "direct-in"))

	t.Run("a name for a private address in the domain pass of a DIRECT rule", func(t *testing.T) {
		source := startCountingSource(t, privateHost)
		requireGuarded(t, guarded, vlessToName(t, ports[2], id, xrayPrivateName, source.addr.Port), source, "direct-in to "+xrayPrivateName)
	})

	t.Run("a private literal is still blocked by the core's own rule", func(t *testing.T) {
		for _, port := range ports {
			source := startCountingSource(t, privateHost)
			requireNothingArrives(t, vlessTo(t, port, id, source.addr), source, fmt.Sprintf("the inbound on %d to %s", port, source.addr))
		}
	})

	t.Run("CGNAT through DIRECT", func(t *testing.T) {
		host := cgnatHostOrSkip(t)
		source := startCountingSource(t, host)
		for i, inbound := range []string{"an ordinary user", "egress-in", "direct-in"} {
			requireDataArrives(t, vlessTo(t, ports[i], id, source.addr), inbound+" to "+source.addr.String()+" through the CGNAT rule")
		}
		requireDataArrives(t, vlessToName(t, ports[2], id, xrayCGNATName, source.addr.Port), "direct-in to "+xrayCGNATName)
	})

	t.Run("a system name for a private address through the interface-bound AsIs outbound", func(t *testing.T) {
		host := systemNameOrSkip(t, systemPrivateName, func(addr netip.Addr) bool { return isPrivateTargetAddr(addr) && !addr.IsLoopback() })
		source := startCountingSource(t, host)
		requireGuarded(t, guarded, vlessToName(t, ports[1], id, systemPrivateName, source.addr.Port), source, "egress-in to "+systemPrivateName)
	})

	t.Run("a system name for a CGNAT address through the interface-bound AsIs outbound", func(t *testing.T) {
		host := systemNameOrSkip(t, systemCGNATName, cgnatPrefix().Contains)
		source := startCountingSource(t, host)
		requireGuarded(t, guarded, vlessToName(t, ports[1], id, systemCGNATName, source.addr.Port), source, "egress-in to "+systemCGNATName+": the CGNAT allow belongs to DIRECT only")
	})

	t.Run("a public target", func(t *testing.T) {
		source := startCountingSource(t, publicHostOrSkip(t))
		requireDataArrives(t, vlessTo(t, ports[0], id, source.addr), "an ordinary user to "+source.addr.String())
		requireDataArrives(t, vlessTo(t, ports[1], id, source.addr), "egress-in to "+source.addr.String())
	})

}

func startCoreOneVariant(t *testing.T, binary string, beforePrivateBlock, afterPrivateBlock []any) ([3]int, uuid.UUID, net.IP) {
	t.Helper()
	privateHost := privateTargetHost(t)
	ports := [3]int{netutil.FindFreePort(), netutil.FindFreePort(), netutil.FindFreePort()}
	id := uuid.New()
	hosts := map[string]any{xrayPrivateName: privateHost.String()}
	shape := coreOneLiveWith(ports, interfaceOwning(t, privateHost), hosts, beforePrivateBlock, afterPrivateBlock)
	startPrivateGuardBackend(t, binary, shape, privateGuardUser(id, "VLESS TCP SESSION", "egress-in", "direct-in"))
	return ports, id, privateHost
}

func TestLiveACGNATRouteWithoutADerivableAllowStaysRefused(t *testing.T) {
	forEachXrayBinaryWithItsGuard(t, func(t *testing.T, binary string, guarded bool) {
		source := startCountingSource(t, cgnatHostOrSkip(t))
		ports, id, _ := startCoreOneVariant(t, binary, []any{
			map[string]any{"type": "field", "ruleTag": "shared", "port": "25", "outboundTag": "BLOCK"},
			map[string]any{"type": "field", "ruleTag": "shared", "ip": []any{"100.64.0.0/10"}, "outboundTag": "DIRECT"},
		}, nil)
		requireGuarded(t, guarded, vlessTo(t, ports[0], id, source.addr), source, "an ordinary user to "+source.addr.String()+" through a CGNAT rule under a ruleTag another rule shares, which the node cannot turn into an allow")
	})
}

func TestLiveAPrivateRouteAnEarlierBlockShadowsDoesNotOpenItsAddressByName(t *testing.T) {
	forEachXrayBinaryWithItsGuard(t, func(t *testing.T, binary string, guarded bool) {
		privateHost := privateTargetHost(t)
		ports, id, _ := startCoreOneVariant(t, binary, []any{coreOneCGNATRule}, []any{
			map[string]any{"type": "field", "ip": []any{privateHost.String() + "/32"}, "outboundTag": "DIRECT"},
		})
		literal := startCountingSource(t, privateHost)
		requireNothingArrives(t, vlessTo(t, ports[2], id, literal.addr), literal, "direct-in to the literal "+literal.addr.String()+", which the earlier block decides")
		byName := startCountingSource(t, privateHost)
		requireGuarded(t, guarded, vlessToName(t, ports[2], id, xrayPrivateName, byName.addr.Port), byName, "direct-in to "+xrayPrivateName+" through DIRECT under a shadowed private route")
	})
}

func TestLiveAnInboundRestrictedPrivateRouteOpensItsAddressForItsInboundOnly(t *testing.T) {
	forEachXrayBinaryWithItsGuard(t, func(t *testing.T, binary string, guarded bool) {
		privateHost := privateTargetHost(t)
		ports, id, _ := startCoreOneVariant(t, binary, []any{
			coreOneCGNATRule,
			map[string]any{"type": "field", "inboundTag": []any{"egress-in"}, "ip": []any{privateHost.String() + "/32"}, "outboundTag": "DIRECT"},
		}, nil)
		requireDataArrives(t, vlessTo(t, ports[1], id, startCountingSource(t, privateHost).addr), "egress-in to the literal private address its own rule sends to DIRECT")
		leak := startCountingSource(t, privateHost)
		requireGuarded(t, guarded, vlessToName(t, ports[2], id, xrayPrivateName, leak.addr.Port), leak, "direct-in to "+xrayPrivateName+" through DIRECT, where only egress-in's rule sends that address")
		ordinary := startCountingSource(t, privateHost)
		requireNothingArrives(t, vlessToName(t, ports[0], id, xrayPrivateName, ordinary.addr.Port), ordinary, "an ordinary user to "+xrayPrivateName+", which routing blocks")
	})
}

func dialSocksStream(t *testing.T, port int, target *net.TCPAddr) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 10)
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, reply[:2]); err != nil || reply[1] != 0 {
		t.Fatalf("the socks inbound refused the greeting: %v %v", reply[:2], err)
	}
	request := append([]byte{5, 1, 0, 1}, target.IP.To4()...)
	request = append(request, byte(target.Port>>8), byte(target.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		t.Fatalf("the socks inbound refused the connect: %v %v", reply, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

func TestLiveCoreSixteenShapeKeepsIndiaUsersOffPrivateTargets(t *testing.T) {
	forEachXrayBinaryWithItsGuard(t, coreSixteenShapeKeepsUsersOffPrivateTargets)
}

func coreSixteenShapeKeepsUsersOffPrivateTargets(t *testing.T, binary string, guarded bool) {
	privateHost := privateTargetHost(t)
	tunneled := startCountingSource(t, privateHost)
	indiaPort, socksPort, tunnelPort := netutil.FindFreePort(), netutil.FindFreePort(), netutil.FindFreePort()
	id := uuid.New()
	shape := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			liveVless("india", indiaPort),
			map[string]any{"tag": "SOCKS IN", "listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}},
			map[string]any{"tag": "PANEL TUNNEL", "listen": "127.0.0.1", "port": tunnelPort, "protocol": "dokodemo-door", "settings": map[string]any{"address": tunneled.addr.IP.String(), "port": tunneled.addr.Port, "network": "tcp"}},
		},
		"outbounds": []any{
			map[string]any{"tag": "india_wireguard", "protocol": "freedom", "settings": map[string]any{}, "streamSettings": map[string]any{"sockopt": map[string]any{"interface": interfaceOwning(t, privateHost)}}},
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "block", "protocol": "blackhole"},
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{"india"}, "outboundTag": "india_wireguard"},
		}},
	}
	startPrivateGuardBackend(t, binary, shape, privateGuardUser(id, "india"))

	t.Run("a private literal", func(t *testing.T) {
		source := startCountingSource(t, privateHost)
		requireGuarded(t, guarded, vlessTo(t, indiaPort, id, source.addr), source, "india to "+source.addr.String())
	})

	t.Run("socks and dokodemo-door inbounds still reach the private target", func(t *testing.T) {
		source := startCountingSource(t, privateHost)
		requireRawDataArrives(t, dialSocksStream(t, socksPort, source.addr), "the socks inbound to "+source.addr.String())
		tunnel, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnelPort)), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tunnel.Close() })
		requireRawDataArrives(t, tunnel, "the dokodemo-door tunnel to "+tunneled.addr.String())
	})

	t.Run("a system name for a private address", func(t *testing.T) {
		host := systemNameOrSkip(t, systemPrivateName, func(addr netip.Addr) bool { return isPrivateTargetAddr(addr) && !addr.IsLoopback() })
		source := startCountingSource(t, host)
		requireGuarded(t, guarded, vlessToName(t, indiaPort, id, systemPrivateName, source.addr.Port), source, "india to "+systemPrivateName)
	})

	t.Run("a CGNAT literal, which no rule of this core allows", func(t *testing.T) {
		source := startCountingSource(t, cgnatHostOrSkip(t))
		requireGuarded(t, guarded, vlessTo(t, indiaPort, id, source.addr), source, "india to "+source.addr.String())
	})

	t.Run("a public target", func(t *testing.T) {
		source := startCountingSource(t, publicHostOrSkip(t))
		requireDataArrives(t, vlessTo(t, indiaPort, id, source.addr), "india to "+source.addr.String())
	})
}
