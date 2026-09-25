package xray

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const (
	rotationServerKey = "MDEyMzQ1Njc4OWFiY2RlZg=="
	rotationSS2022    = "2022-blake3-aes-128-gcm"
)

type rotationCredential struct {
	secret string
	method string
}

type rotationKind struct {
	name     string
	tag      string
	inbound  func(port int, tls map[string]any) map[string]any
	user     func(email, tag string, credential rotationCredential) *common.User
	outbound func(port int, pin string, credential rotationCredential) map[string]any
	next     func(current rotationCredential) rotationCredential
}

func rotatedSecret(current rotationCredential) rotationCredential {
	return rotationCredential{secret: uuid.NewString(), method: current.method}
}

func rotatedCipher(current rotationCredential) rotationCredential {
	method := "aes-256-gcm"
	if current.method == method {
		method = "aes-128-gcm"
	}
	return rotationCredential{secret: current.secret, method: method}
}

func tcpInbound(tag, protocol string, settings map[string]any) func(int, map[string]any) map[string]any {
	return func(port int, _ map[string]any) map[string]any {
		return map[string]any{
			"tag":            tag,
			"listen":         "127.0.0.1",
			"port":           port,
			"protocol":       protocol,
			"settings":       settings,
			"streamSettings": map[string]any{"network": "tcp"},
		}
	}
}

func rotationKinds() []rotationKind {
	vnext := func(protocol string, user map[string]any) func(int, string, rotationCredential) map[string]any {
		return func(port int, _ string, credential rotationCredential) map[string]any {
			entry := map[string]any{"id": credential.secret}
			for key, value := range user {
				entry[key] = value
			}
			return map[string]any{
				"protocol":       protocol,
				"settings":       map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{entry}}}},
				"streamSettings": map[string]any{"network": "tcp"},
			}
		}
	}
	shadowsocksOutbound := func(port int, _ string, credential rotationCredential) map[string]any {
		key := credential.secret
		if credential.method == rotationSS2022 {
			key = rotationServerKey + ":" + common.EnsureBase64Password(credential.secret, rotationSS2022)
		}
		return map[string]any{
			"protocol": Shadowsocks,
			"settings": map[string]any{"servers": []any{map[string]any{
				"address": "127.0.0.1", "port": port, "method": credential.method, "password": key,
			}}},
			"streamSettings": map[string]any{"network": "tcp"},
		}
	}
	shadowsocksUser := func(email, tag string, credential rotationCredential) *common.User {
		return &common.User{Email: email, Inbounds: []string{tag}, Proxies: &common.Proxy{
			Shadowsocks: &common.Shadowsocks{Password: credential.secret, Method: credential.method},
		}}
	}

	return []rotationKind{
		{
			name:    "vless id",
			tag:     "rotate-vless",
			inbound: tcpInbound("rotate-vless", Vless, map[string]any{"clients": []any{}, "decryption": "none"}),
			user: func(email, tag string, credential rotationCredential) *common.User {
				return &common.User{Email: email, Inbounds: []string{tag}, Proxies: &common.Proxy{Vless: &common.Vless{Id: credential.secret}}}
			},
			outbound: vnext(Vless, map[string]any{"encryption": "none"}),
			next:     rotatedSecret,
		},
		{
			name:    "vmess id",
			tag:     "rotate-vmess",
			inbound: tcpInbound("rotate-vmess", Vmess, map[string]any{"clients": []any{}}),
			user: func(email, tag string, credential rotationCredential) *common.User {
				return &common.User{Email: email, Inbounds: []string{tag}, Proxies: &common.Proxy{Vmess: &common.Vmess{Id: credential.secret}}}
			},
			outbound: vnext(Vmess, map[string]any{"security": "auto"}),
			next:     rotatedSecret,
		},
		{
			name:    "trojan password",
			tag:     "rotate-trojan",
			inbound: tcpInbound("rotate-trojan", Trojan, map[string]any{"clients": []any{}}),
			user: func(email, tag string, credential rotationCredential) *common.User {
				return &common.User{Email: email, Inbounds: []string{tag}, Proxies: &common.Proxy{Trojan: &common.Trojan{Password: credential.secret}}}
			},
			outbound: func(port int, _ string, credential rotationCredential) map[string]any {
				return map[string]any{
					"protocol":       Trojan,
					"settings":       map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port, "password": credential.secret}}},
					"streamSettings": map[string]any{"network": "tcp"},
				}
			},
			next: rotatedSecret,
		},
		{
			name:     "shadowsocks password",
			tag:      "rotate-ss",
			inbound:  tcpInbound("rotate-ss", Shadowsocks, map[string]any{"clients": []any{}, "network": "tcp"}),
			user:     shadowsocksUser,
			outbound: shadowsocksOutbound,
			next:     rotatedSecret,
		},
		{
			name:     "shadowsocks cipher",
			tag:      "rotate-ss-cipher",
			inbound:  tcpInbound("rotate-ss-cipher", Shadowsocks, map[string]any{"clients": []any{}, "network": "tcp"}),
			user:     shadowsocksUser,
			outbound: shadowsocksOutbound,
			next:     rotatedCipher,
		},
		{
			name: "shadowsocks 2022 password",
			tag:  "rotate-ss2022",
			inbound: tcpInbound("rotate-ss2022", Shadowsocks, map[string]any{
				"clients": []any{}, "method": rotationSS2022, "password": rotationServerKey, "network": "tcp",
			}),
			user:     shadowsocksUser,
			outbound: shadowsocksOutbound,
			next:     rotatedSecret,
		},
		{
			name: "hysteria auth",
			tag:  "rotate-hysteria",
			inbound: func(port int, tls map[string]any) map[string]any {
				return map[string]any{
					"tag":      "rotate-hysteria",
					"listen":   "127.0.0.1",
					"port":     port,
					"protocol": Hysteria,
					"settings": map[string]any{"version": 2, "clients": []any{}},
					"streamSettings": map[string]any{
						"network":          "hysteria",
						"hysteriaSettings": map[string]any{"version": 2},
						"security":         "tls",
						"tlsSettings":      tls,
					},
				}
			},
			user: func(email, tag string, credential rotationCredential) *common.User {
				return &common.User{Email: email, Inbounds: []string{tag}, Proxies: &common.Proxy{Hysteria: &common.Hysteria{Auth: credential.secret}}}
			},
			outbound: func(port int, pin string, credential rotationCredential) map[string]any {
				return map[string]any{
					"protocol": Hysteria,
					"settings": map[string]any{"version": 2, "address": "127.0.0.1", "port": port},
					"streamSettings": map[string]any{
						"network":          "hysteria",
						"hysteriaSettings": map[string]any{"version": 2, "auth": credential.secret},
						"security":         "tls",
						"tlsSettings": map[string]any{
							"serverName":           "localhost",
							"alpn":                 []string{"h3"},
							"pinnedPeerCertSha256": pin,
						},
					},
				}
			},
			next: rotatedSecret,
		},
	}
}

func freeTCPAndUDPPort(t *testing.T) int {
	t.Helper()

	for range 50 {
		port := netutil.FindFreePort()
		conn, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = conn.Close()
		return port
	}
	t.Fatal("no port is free for both tcp and udp")
	return 0
}

func initialCredential(kind rotationKind) rotationCredential {
	switch kind.tag {
	case "rotate-ss", "rotate-ss-cipher":
		return rotationCredential{secret: uuid.NewString(), method: "aes-128-gcm"}
	case "rotate-ss2022":
		return rotationCredential{secret: uuid.NewString(), method: rotationSS2022}
	}
	return rotationCredential{secret: uuid.NewString()}
}

type rotationNode struct {
	backend *Xray
	target  *streamingTarget
	ports   map[string]int
	pin     string
}

func startRotationNode(t *testing.T, kinds []rotationKind, users []*common.User) *rotationNode {
	t.Helper()

	target := startStreamingTarget(t)
	certFile, keyFile, pin := writeSelfSignedCertificate(t)
	tls := map[string]any{
		"alpn":         []string{"h3"},
		"certificates": []any{map[string]any{"certificateFile": certFile, "keyFile": keyFile}},
	}
	ports := make(map[string]int, len(kinds))
	inbounds := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		ports[kind.tag] = freeTCPAndUDPPort(t)
		inbounds = append(inbounds, kind.inbound(ports[kind.tag], tls))
	}
	raw, err := json.Marshal(map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	xrayConfig, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		XrayExecutablePath:  executablePath,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	}
	backend, err := New(context.Background(), xrayConfig, users, netutil.FindFreePort(), netutil.FindFreePort(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)
	return &rotationNode{backend: backend, target: target, ports: ports, pin: pin}
}

func (n *rotationNode) client(t *testing.T, kind rotationKind, credential rotationCredential) int {
	t.Helper()

	clientPort := netutil.FindFreePort()
	outbound := kind.outbound(n.ports[kind.tag], n.pin, credential)
	outbound["tag"] = "out"
	startRevokeClient(t, map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{
			"uplinkOnly":   0,
			"downlinkOnly": 0,
		}}},
		"inbounds": []any{map[string]any{
			"tag":      "in",
			"listen":   "127.0.0.1",
			"port":     clientPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": n.target.port(), "network": "tcp"},
		}},
		"outbounds": []any{outbound},
	})
	waitForListener(t, clientPort)
	return clientPort
}

func TestRotatingEveryCredentialKindClosesTheOldCredentialsSessions(t *testing.T) {
	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}

	kinds := rotationKinds()
	credentials := make(map[string]rotationCredential, len(kinds))
	users := make([]*common.User, 0, len(kinds))
	for _, kind := range kinds {
		credentials[kind.tag] = initialCredential(kind)
		users = append(users, kind.user("rotating-"+kind.tag, kind.tag, credentials[kind.tag]))
	}
	node := startRotationNode(t, kinds, users)

	for _, kind := range kinds {
		t.Run(kind.name, func(t *testing.T) {
			email := "rotating-" + kind.tag
			current := credentials[kind.tag]
			for _, step := range rotationSteps() {
				oldClient := node.client(t, kind, current)
				old := openStreamProbe(t, oldClient)
				old.waitForBytes(t, "the current credential before the rotation through "+step.name, revokeWarmupBytes)

				next := kind.next(current)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := step.rotate(ctx, node.backend, kind.user(email, kind.tag, next))
				cancel()
				if err != nil {
					t.Fatalf("rotate through %s: %v", step.name, err)
				}

				select {
				case <-old.ended:
				case <-time.After(rotationCutBound):
					before := old.received.Load()
					time.Sleep(time.Second)
					if moved := old.received.Load() - before; moved != 0 {
						t.Fatalf("the session opened with the old credential still carried %d bytes %s after the rotation through %s", moved, rotationCutBound, step.name)
					}
					t.Logf("the old credential's session stopped carrying data after the rotation through %s; the client kept its own end open", step.name)
				}
				if received := refusedBytes(t, oldClient); received != 0 {
					t.Fatalf("the old credential opened a new session after the rotation through %s and moved %d bytes", step.name, received)
				}

				fresh := openStreamProbe(t, node.client(t, kind, next))
				fresh.waitForBytes(t, "the new credential after the rotation through "+step.name, revokeWarmupBytes)
				fresh.close()
				current = next
			}
		})
	}
}
