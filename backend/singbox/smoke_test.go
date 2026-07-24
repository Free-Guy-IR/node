//go:build linux

package singbox

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

// discoverSingBoxBinary looks for a real sing-box binary (built with the
// with_v2ray_api build tag per the project's build recipe) to run the live
// smoke test against. It skips the test rather than failing when none is
// found, since this test needs a real subprocess and real network ports and
// has no business running in an ordinary CI environment that doesn't have
// sing-box built.
func discoverSingBoxBinary(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("SINGBOX_SMOKE_BINARY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	candidates := []string{
		"/root/singbox-spike/sing-box-v2api", // dev-box proof-of-concept build
		"/usr/local/bin/sing-box",            // default SingBoxExecutablePath
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	t.Skip("no sing-box binary found for live smoke test; set SINGBOX_SMOKE_BINARY=/path/to/sing-box to run it")
	return ""
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free TCP port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free UDP port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// writeSelfSignedCert generates a throwaway self-signed cert/key pair sing-box
// can load for its hysteria2 inbound's TLS. No client ever connects in this
// test, so trust/chain validity doesn't matter - sing-box just needs to be
// able to read a valid cert+key at startup.
func writeSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPath = filepath.Join(dir, "smoke-cert.pem")
	keyPath = filepath.Join(dir, "smoke-key.pem")

	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode cert: %v", err)
	}
	certOut.Close()

	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		t.Fatalf("encode key: %v", err)
	}
	keyOut.Close()

	return certPath, keyPath
}

// TestLiveSmoke_SingBoxProcessStartsAndReportsStats is the strongest evidence
// in this package that the whole chain works: it spawns a real sing-box
// binary via New(), with a real generated Hysteria2 config and a real test
// user injected, then confirms the process reports itself started and that
// GetSysStats returns live data through the hand-written v2ray_api gRPC
// client (api/). This proves config generation, process spawn, startup health
// polling, and the gRPC stats client all work end-to-end against a real
// sing-box process - not just against mocks.
//
// It does NOT exercise real Hysteria2 client traffic (no QUIC client is
// driven against the inbound), so per-user/per-inbound traffic counters are
// not validated live here - see api/stats_test.go's
// TestQueryStats_PopulatesPatternsFieldNotDeprecatedPattern for a targeted,
// fully-automated regression test of that specific wire-format behavior
// against a fake server standing in for sing-box.
func TestLiveSmoke_SingBoxProcessStartsAndReportsStats(t *testing.T) {
	bin := discoverSingBoxBinary(t)

	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir)

	hyPort := freeUDPPort(t)
	apiPort := freeTCPPort(t)

	raw := fmt.Sprintf(`{
		"log": {"level": "debug"},
		"inbounds": [{
			"type": "hysteria2",
			"tag": "hy2-in",
			"listen": "::",
			"listen_port": %d,
			"users": [],
			"tls": {"enabled": true, "certificate_path": %q, "key_path": %q}
		}],
		"outbounds": [{"type": "direct"}]
	}`, hyPort, certPath, keyPath)

	sbConfig, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}

	testUser := &common.User{
		Email:    "smoketest@example.com",
		Inbounds: []string{"hy2-in"},
		Proxies: &common.Proxy{
			Hysteria2: &common.Hysteria2{Password: "smoketestpass"},
		},
	}

	cfg := &config.Config{
		SingBoxExecutablePath: bin,
		GeneratedConfigPath:   dir,
		LogBufferSize:         1000,
		StartupLogTailSize:    200,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sb, err := New(ctx, sbConfig, []*common.User{testUser}, apiPort, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer sb.Shutdown()

	if !sb.Started() {
		t.Fatal("expected sing-box to report Started() == true")
	}
	t.Logf("sing-box version: %q", sb.Version())

	statsCtx, statsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer statsCancel()

	stats, err := sb.GetSysStats(statsCtx)
	if err != nil {
		t.Fatalf("GetSysStats() error = %v", err)
	}

	t.Logf("live sys stats: NumGoroutine=%d Uptime=%d Alloc=%d NumGC=%d",
		stats.GetNumGoroutine(), stats.GetUptime(), stats.GetAlloc(), stats.GetNumGc())

	if stats.GetNumGoroutine() == 0 {
		t.Error("expected NumGoroutine > 0 from a live sing-box process")
	}

	// Also exercise the full common.User email injection was picked up: the
	// generated config file on disk should list our test user under the
	// hysteria2 inbound.
	generated, err := os.ReadFile(filepath.Join(dir, "singbox.json"))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	for _, needle := range []string{"smoketest@example.com", "smoketestpass", "hy2-in"} {
		if !strings.Contains(string(generated), needle) {
			t.Errorf("expected generated config to contain %q; got:\n%s", needle, string(generated))
		}
	}
}
