//go:build linux

package mtproto

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free TCP port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestLiveSmoke_MTProtoStartsAndDomainFronts is the mtproto analogue of
// backend/openvpn/smoke_test.go's live smoke test, adapted for what's
// practical to automate here: driving a REAL second openvpn process as a
// client (like that test does) has no equivalent for MTProto without a real
// Telegram account, so this instead exercises every piece of the chain that
// CAN be driven headlessly against a real network - listener startup, the
// real mtglib.Network/IPAllowlist/EventStream wiring, and (crucially) the
// domain-fronting fallback path any unauthenticated connection takes,
// against a real internet domain - plus live SyncUsers/UpdateUsers and a
// clean Shutdown. The multi-secret handshake-matching path itself (this
// package's actual reason for existing) is verified against a real
// Telegram client as part of the project's end-to-end test phase, not here.
func TestLiveSmoke_MTProtoStartsAndDomainFronts(t *testing.T) {
	port := freeTCPPort(t)

	raw := fmt.Sprintf(`{"instances": [{"tag": "main", "port": %d, "fake_tls_domain": "httpbin.org"}]}`, port)

	mtCfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}

	nodeCfg := &config.Config{
		LogBufferSize:              1000,
		StatsUpdateIntervalSeconds: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	backend, err := New(ctx, mtCfg, nil, nodeCfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer backend.Shutdown()

	if !backend.Started() {
		t.Fatal("expected mtproto backend to report Started() == true")
	}
	t.Logf("mtproto version: %q", backend.Version())

	// An unauthenticated HTTPS request (no valid MTProto secret at all)
	// should fall through doFakeTLSHandshake -> doDomainFronting and get
	// proxied to the real fronting domain - exercising the full real-network
	// chain (Network dialer, IPAllowlist, listener, EventStream wiring)
	// without needing a real MTProto client.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint: gosec
		},
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/get", port)) //nolint: noctx
	if err != nil {
		t.Fatalf("domain-fronted request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from domain-fronted request, got %d", resp.StatusCode)
	}
	t.Log("unauthenticated connection was correctly domain-fronted to a real site")

	// Live user sync must not error or restart anything.
	testSecret := make([]byte, 16)
	for i := range testSecret {
		testSecret[i] = byte(i)
	}
	testUser := &common.User{
		Email:    "smoketest@example.com",
		Inbounds: []string{"main"},
		Proxies: &common.Proxy{
			Mtproto: &common.MtprotoUser{Username: "7", Secret: hex.EncodeToString(testSecret)},
		},
	}

	if err := backend.SyncUsers(context.Background(), []*common.User{testUser}); err != nil {
		t.Fatalf("SyncUsers() error = %v", err)
	}

	backend.mu.RLock()
	_, ok := backend.secretsByID["7"]
	backend.mu.RUnlock()
	if !ok {
		t.Error("expected secretsByID to contain the synced user after SyncUsers")
	}

	if err := backend.UpdateUsers(context.Background(), []*common.User{testUser}); err != nil {
		t.Fatalf("UpdateUsers() error = %v", err)
	}

	statsCtx, statsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statsCancel()
	sysStats, err := backend.GetSysStats(statsCtx)
	if err != nil {
		t.Fatalf("GetSysStats() error = %v", err)
	}
	if sysStats.GetNumGoroutine() == 0 {
		t.Error("expected NumGoroutine > 0 from a live backend")
	}

	backend.Shutdown()

	// Port should be free again after Shutdown.
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Errorf("expected port %d to be free after Shutdown, got: %v", port, err)
	} else {
		l.Close()
	}
}
