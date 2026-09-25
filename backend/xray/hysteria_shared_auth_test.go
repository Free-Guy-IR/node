package xray

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const (
	sharedAuthTag      = "hysteria-shared-auth"
	sharedAuthRestarts = 6
)

type hysteriaNode struct {
	backend    *Xray
	serverPort int
	pin        string
	target     *streamingTarget
	configDir  string
}

func startHysteriaNode(t *testing.T, users []*common.User) *hysteriaNode {
	t.Helper()

	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}

	target := startStreamingTarget(t)
	certFile, keyFile, pin := writeSelfSignedCertificate(t)
	serverPort := netutil.FindFreePort()
	serverConfig := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      sharedAuthTag,
			"listen":   "127.0.0.1",
			"port":     serverPort,
			"protocol": Hysteria,
			"settings": map[string]any{"version": 2, "clients": []any{}},
			"streamSettings": map[string]any{
				"network":          "hysteria",
				"hysteriaSettings": map[string]any{"version": 2},
				"security":         "tls",
				"tlsSettings": map[string]any{
					"alpn":         []string{"h3"},
					"certificates": []any{map[string]any{"certificateFile": certFile, "keyFile": keyFile}},
				},
			},
		}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	}
	raw, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	xrayConfig, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}

	configDir := t.TempDir()
	cfg := &config.Config{
		XrayExecutablePath:  executablePath,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: configDir,
		LogBufferSize:       1000,
		Debug:               true,
	}
	backend, err := New(context.Background(), xrayConfig, users, netutil.FindFreePort(), netutil.FindFreePort(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)
	return &hysteriaNode{backend: backend, serverPort: serverPort, pin: pin, target: target, configDir: configDir}
}

func (n *hysteriaNode) client(t *testing.T, auth string) int {
	t.Helper()

	clientPort := netutil.FindFreePort()
	startRevokeClient(t, map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "in-hysteria",
			"listen":   "127.0.0.1",
			"port":     clientPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": n.target.port(), "network": "tcp"},
		}},
		"outbounds": []any{map[string]any{
			"tag":      "out-hysteria",
			"protocol": Hysteria,
			"settings": map[string]any{"version": 2, "address": "127.0.0.1", "port": n.serverPort},
			"streamSettings": map[string]any{
				"network":          "hysteria",
				"hysteriaSettings": map[string]any{"version": 2, "auth": auth},
				"security":         "tls",
				"tlsSettings": map[string]any{
					"serverName":           "localhost",
					"alpn":                 []string{"h3"},
					"pinnedPeerCertSha256": n.pin,
				},
			},
		}},
	})
	waitForListener(t, clientPort)
	return clientPort
}

func (n *hysteriaNode) generatedClients(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(n.configDir, "xray.json"))
	if err != nil {
		t.Fatal(err)
	}
	return emittedClients(t, raw)[sharedAuthTag]
}

func (n *hysteriaNode) drainTraffic(t *testing.T) map[string]int64 {
	t.Helper()

	resp, err := n.backend.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if err != nil {
		t.Fatal(err)
	}
	traffic := make(map[string]int64)
	for _, stat := range resp.GetStats() {
		if stat.GetValue() > 0 {
			traffic[stat.GetName()] += stat.GetValue()
		}
	}
	return traffic
}

func hysteriaUser(email, auth string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: []string{sharedAuthTag},
		Proxies:  &common.Proxy{Hysteria: &common.Hysteria{Auth: auth}},
	}
}

func TestSharedHysteriaAuthBillsTheSameUserOnEveryRestart(t *testing.T) {
	const shared = "0123456789abcdefSHARED"
	users := []*common.User{hysteriaUser("1", shared), hysteriaUser("2", shared)}
	node := startHysteriaNode(t, users)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for round := range sharedAuthRestarts {
		if round > 0 {
			if err := node.backend.SyncUsers(ctx, users); err != nil {
				t.Fatalf("round %d: restart: %v", round, err)
			}
		}
		if emitted := node.generatedClients(t); !slices.Equal(emitted, []string{"1", "2"}) {
			t.Fatalf("round %d: the config handed to xray lists the hysteria clients as %v, want them in email order", round, emitted)
		}

		node.drainTraffic(t)
		probe := openStreamProbe(t, node.client(t, shared))
		probe.waitForBytes(t, "shared-auth stream", revokeWarmupBytes)
		probe.close()
		<-probe.ended

		traffic := node.drainTraffic(t)
		billed := slices.Sorted(maps.Keys(traffic))
		if !slices.Equal(billed, []string{"2"}) {
			t.Fatalf("round %d: a client with the shared auth was billed to %v (%v), want the last client in email order every time", round, billed, traffic)
		}
		t.Logf("round %d: the shared-auth client was billed to %v", round, traffic)
	}
}
