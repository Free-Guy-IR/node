package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
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
	udpFlowInterval   = 100 * time.Millisecond
	udpFlowGrace      = time.Second
	udpFlowObserveFor = 4 * time.Second
)

type udpFlowCase struct {
	name   string
	tag    string
	method string
}

func udpFlowCases() []udpFlowCase {
	return []udpFlowCase{
		{name: "shadowsocks aead", tag: "udp-ss", method: "aes-128-gcm"},
		{name: "shadowsocks 2022", tag: "udp-ss2022", method: rotationSS2022},
	}
}

func startUDPEcho(t *testing.T) int {
	t.Helper()

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo(buffer[:n], from)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func udpFlowUser(email string, c udpFlowCase, secret string, inbounds ...string) *common.User {
	return &common.User{Email: email, Inbounds: inbounds, Proxies: &common.Proxy{
		Shadowsocks: &common.Shadowsocks{Password: secret, Method: c.method},
	}}
}

func udpFlowInbound(c udpFlowCase, port int) map[string]any {
	settings := map[string]any{"clients": []any{}, "network": "tcp,udp"}
	if c.method == rotationSS2022 {
		settings["method"] = rotationSS2022
		settings["password"] = rotationServerKey
	}
	return map[string]any{
		"tag":      c.tag,
		"listen":   "127.0.0.1",
		"port":     port,
		"protocol": Shadowsocks,
		"settings": settings,
	}
}

func startUDPFlowClient(t *testing.T, c udpFlowCase, serverPort, echoPort int, secret string) int {
	t.Helper()

	password := secret
	if c.method == rotationSS2022 {
		password = rotationServerKey + ":" + common.EnsureBase64Password(secret, rotationSS2022)
	}
	clientPort := freeTCPAndUDPPort(t)
	startRevokeClient(t, map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "in",
			"listen":   "127.0.0.1",
			"port":     clientPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": echoPort, "network": "tcp,udp"},
		}},
		"outbounds": []any{map[string]any{
			"tag":      "out",
			"protocol": Shadowsocks,
			"settings": map[string]any{"servers": []any{map[string]any{
				"address": "127.0.0.1", "port": serverPort, "method": c.method, "password": password,
			}}},
		}},
	})
	waitForListener(t, clientPort)
	return clientPort
}

type udpFlow struct {
	conn     *net.UDPConn
	echoes   atomic.Int64
	lastEcho atomic.Int64
	stop     chan struct{}
	done     chan struct{}
}

func openUDPFlow(t *testing.T, clientPort int) *udpFlow {
	t.Helper()

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: clientPort})
	if err != nil {
		t.Fatal(err)
	}
	flow := &udpFlow{conn: conn, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				return
			}
			if n > 0 {
				flow.echoes.Add(1)
				flow.lastEcho.Store(time.Now().UnixNano())
			}
		}
	}()
	go func() {
		defer close(flow.done)
		ticker := time.NewTicker(udpFlowInterval)
		defer ticker.Stop()
		for seq := 0; ; seq++ {
			_, _ = conn.Write([]byte("probe-" + strconv.Itoa(seq)))
			select {
			case <-flow.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	t.Cleanup(func() {
		close(flow.stop)
		<-flow.done
		_ = conn.Close()
	})
	return flow
}

func (f *udpFlow) waitForEchoes(t *testing.T, label string, want int64) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for f.echoes.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the UDP flow got %d echoes, want %d", label, f.echoes.Load(), want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *udpFlow) echoesAfter(settle, observe time.Duration) int64 {
	time.Sleep(settle)
	before := f.echoes.Load()
	time.Sleep(observe)
	return f.echoes.Load() - before
}

func TestRotatingOrRemovingAShadowsocksUserCutsItsUDPFlow(t *testing.T) {
	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}

	for _, c := range udpFlowCases() {
		for _, change := range []string{"rotate", "remove"} {
			t.Run(fmt.Sprintf("%s %s", c.name, change), func(t *testing.T) {
				echoPort := startUDPEcho(t)
				serverPort := freeTCPAndUDPPort(t)
				email := "udp-flow-" + c.tag
				secret := uuid.NewString()

				raw, err := json.Marshal(map[string]any{
					"log":       map[string]any{"loglevel": "warning"},
					"inbounds":  []any{udpFlowInbound(c, serverPort)},
					"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				xrayConfig, err := NewConfig(string(raw), nil)
				if err != nil {
					t.Fatal(err)
				}
				back, err := New(context.Background(), xrayConfig, []*common.User{udpFlowUser(email, c, secret, c.tag)},
					netutil.FindFreePort(), netutil.FindFreePort(), &config.Config{
						XrayExecutablePath:  executablePath,
						XrayAssetsPath:      assetsPath,
						GeneratedConfigPath: t.TempDir(),
						LogBufferSize:       1000,
					})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(back.Shutdown)

				flow := openUDPFlow(t, startUDPFlowClient(t, c, serverPort, echoPort, secret))
				flow.waitForEchoes(t, "before the change", 5)

				next := udpFlowUser(email, c, uuid.NewString(), c.tag)
				if change == "remove" {
					next = udpFlowUser(email, c, secret)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err = back.SyncUser(ctx, next)
				cancel()
				if err != nil {
					t.Fatal(err)
				}

				if moved := flow.echoesAfter(udpFlowGrace, udpFlowObserveFor); moved != 0 {
					t.Fatalf("after SyncUser %sd the user, the UDP flow opened with the old credential still got %d echoes in %s",
						change, moved, udpFlowObserveFor)
				}

				fresh := openUDPFlow(t, startUDPFlowClient(t, c, serverPort, echoPort, secret))
				if moved := fresh.echoesAfter(udpFlowGrace, udpFlowObserveFor); moved != 0 {
					t.Fatalf("a new UDP flow with the old credential got %d echoes after the user was %sd", moved, change)
				}
			})
		}
	}
}
