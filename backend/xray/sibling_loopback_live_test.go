package xray

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const siblingTestConfig = `{
	"log": {"loglevel": "warning"},
	"inbounds": [
		{
			"tag": "VLESS TCP SESSION",
			"listen": "127.0.0.1",
			"port": %d,
			"protocol": "vless",
			"settings": {"clients": [], "decryption": "none"}
		},
		{
			"tag": "PANEL LOOPBACK TUNNEL",
			"listen": "127.0.0.1",
			"port": %d,
			"protocol": "dokodemo-door",
			"settings": {"address": "127.0.0.1", "port": %d, "network": "tcp"}
		}
	],
	"outbounds": [%s]
}`

func startLoopbackStandIn(t *testing.T, banner string) int {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte(banner))
			_ = conn.Close()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func readsBanner(conn net.Conn, banner string, window time.Duration) bool {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(window))
	got := make([]byte, len(banner))
	if _, err := io.ReadFull(conn, got); err != nil {
		return false
	}
	return string(got) == banner
}

func startSiblingBackend(t *testing.T, binary string, standInPort int, users ...*common.User) (int, int) {
	t.Helper()

	inboundPort := netutil.FindFreePort()
	tunnelPort := netutil.FindFreePort()
	xrayConfig, err := NewConfig(fmt.Sprintf(siblingTestConfig, inboundPort, tunnelPort, standInPort, directOutboundJSON(t)), nil)
	if err != nil {
		t.Fatal(err)
	}

	back, err := New(
		context.Background(),
		xrayConfig,
		users,
		netutil.FindFreePort(),
		netutil.FindFreePort(),
		&config.Config{
			XrayExecutablePath:  binary,
			XrayAssetsPath:      assetsPath,
			GeneratedConfigPath: t.TempDir(),
			LogBufferSize:       1000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(back.Shutdown)
	return inboundPort, tunnelPort
}

func TestLiveXrayUserCannotReachOtherLoopbackServices(t *testing.T) {
	forEachXrayBinary(t, xrayUserCannotReachOtherLoopbackServices)
}

func xrayUserCannotReachOtherLoopbackServices(t *testing.T, binary string) {
	const banner = "sibling-control-port"

	standInPort := startLoopbackStandIn(t, banner)
	source := startByteSource(t)
	aliceID := uuid.New()
	alice := sessionUser("1.alice", aliceID, sessionTestInbound)
	inboundPort, tunnelPort := startSiblingBackend(t, binary, standInPort, alice)

	requireFlowing(t, openVlessStream(t, inboundPort, aliceID, source), "on the user's ordinary traffic before probing")

	probes := []struct {
		name string
		dial func() (net.Conn, error)
	}{
		{"127.0.0.1", func() (net.Conn, error) {
			return dialVlessStream(inboundPort, aliceID, &net.TCPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: standInPort})
		}},
		{"0.0.0.0", func() (net.Conn, error) {
			return dialVlessStream(inboundPort, aliceID, &net.TCPAddr{IP: net.ParseIP("0.0.0.0").To4(), Port: standInPort})
		}},
		{"localhost", func() (net.Conn, error) {
			return dialVlessStreamDomain(inboundPort, aliceID, "localhost", standInPort)
		}},
	}
	for _, probe := range probes {
		conn, err := probe.dial()
		if err != nil {
			t.Fatalf("opening the tunnel for %s: %v", probe.name, err)
		}
		if readsBanner(&vlessResponseConn{Conn: conn}, banner, 2*time.Second) {
			t.Errorf("an xray user reached a loopback service that is not the node's own, by %s:%d", probe.name, standInPort)
		}
	}

	requireFlowing(t, openVlessStream(t, inboundPort, aliceID, source), "on the user's ordinary traffic after the probes")

	tunnel, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnelPort)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !readsBanner(tunnel, banner, 3*time.Second) {
		t.Errorf("the panel's dokodemo-door tunnel no longer reaches its loopback service on port %d", standInPort)
	}
}
