package xray

import (
	"net"
	"sort"
	"sync"
	"testing"
	"time"
)

var (
	trafficHostOnce sync.Once
	trafficHost     string
)

func selfReachable(ip string) bool {
	listener, err := net.Listen("tcp4", net.JoinHostPort(ip, "0"))
	if err != nil {
		return false
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			close(accepted)
		}
	}()

	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()

	select {
	case <-accepted:
		return true
	case <-time.After(time.Second):
		return false
	}
}

func findTrafficHost() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	candidates := make([]string, 0)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
				continue
			}
			candidates = append(candidates, ip.String())
		}
	}
	sort.Strings(candidates)

	for _, candidate := range candidates {
		if selfReachable(candidate) {
			return candidate
		}
	}
	return ""
}

func userTrafficHost(t *testing.T) string {
	t.Helper()

	trafficHostOnce.Do(func() { trafficHost = findTrafficHost() })
	if trafficHost == "" {
		t.Skip("this host has no reachable non-loopback IPv4 address to place user traffic targets on")
	}
	return trafficHost
}
