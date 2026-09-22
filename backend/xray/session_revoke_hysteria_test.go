package xray

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

func writeSelfSignedCertificate(t *testing.T) (certFile, keyFile, pin string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(der)
	return certFile, keyFile, hex.EncodeToString(sum[:])
}

func closeAndDrain(t *testing.T, probe *streamProbe) {
	t.Helper()

	probe.close()
	select {
	case <-probe.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a closed probe never ended")
	}
	time.Sleep(3 * time.Second)
}

func TestRemovedUserStreamsOnAConnectionAuthenticatedBeforeAResyncAreRefused(t *testing.T) {
	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}

	const tag = "revoke-hysteria"
	target := startStreamingTarget(t)
	certFile, keyFile, pin := writeSelfSignedCertificate(t)
	serverPort := netutil.FindFreePort()

	serverConfig := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      tag,
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
	rawServer, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	xrayConfig, err := NewConfig(string(rawServer), nil)
	if err != nil {
		t.Fatal(err)
	}

	victim := &common.User{
		Email:    "revoke-hysteria-victim",
		Inbounds: []string{tag},
		Proxies:  &common.Proxy{Hysteria: &common.Hysteria{Auth: uuid.New().String()}},
	}

	cfg := &config.Config{
		XrayExecutablePath:  executablePath,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	}
	backend, err := New(context.Background(), xrayConfig, []*common.User{victim}, netutil.FindFreePort(), netutil.FindFreePort(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)

	clientPort := netutil.FindFreePort()
	startRevokeClient(t, map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{
			"uplinkOnly":   0,
			"downlinkOnly": 0,
		}}},
		"inbounds": []any{map[string]any{
			"tag":      "in-victim",
			"listen":   "127.0.0.1",
			"port":     clientPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": target.port(), "network": "tcp"},
		}},
		"outbounds": []any{map[string]any{
			"tag":      "out-victim",
			"protocol": Hysteria,
			"settings": map[string]any{"version": 2, "address": "127.0.0.1", "port": serverPort},
			"streamSettings": map[string]any{
				"network":          "hysteria",
				"hysteriaSettings": map[string]any{"version": 2, "auth": victim.GetProxies().GetHysteria().GetAuth()},
				"security":         "tls",
				"tlsSettings": map[string]any{
					"serverName":           "localhost",
					"alpn":                 []string{"h3"},
					"pinnedPeerCertSha256": pin,
				},
			},
		}},
	})
	waitForListener(t, clientPort)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first := openStreamProbe(t, clientPort)
	first.waitForBytes(t, "first stream on the authenticated connection", revokeWarmupBytes)
	closeAndDrain(t, first)

	if err := backend.SyncUser(ctx, victim); err != nil {
		t.Fatalf("resync the staying victim: %v", err)
	}

	afterResync := openStreamProbe(t, clientPort)
	afterResync.waitForBytes(t, "stream after a resync of a staying user", revokeWarmupBytes)
	closeAndDrain(t, afterResync)

	if err := backend.SyncUser(ctx, withoutInbounds(victim)); err != nil {
		t.Fatalf("remove victim: %v", err)
	}

	late := openStreamProbe(t, clientPort)
	time.Sleep(revokeReconnectProbe)
	if got := late.received.Load(); got != 0 {
		t.Fatalf("a connection authenticated before the resync carried %d bytes after the user was removed", got)
	}
}
