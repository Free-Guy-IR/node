//go:build linux

package openvpn

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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

// discoverOpenVPNBinary looks for a real openvpn binary to run the live
// smoke test against. It skips the test rather than failing when none is
// found, mirroring backend/singbox/smoke_test.go's discoverSingBoxBinary -
// this needs a real subprocess, a real tun device, and root, and has no
// business running in an ordinary CI environment.
func discoverOpenVPNBinary(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("OPENVPN_SMOKE_BINARY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	candidates := []string{
		"/usr/sbin/openvpn",
		"/usr/local/sbin/openvpn",
		"/usr/local/bin/openvpn",
		"/usr/bin/openvpn",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	t.Skip("no openvpn binary found for live smoke test; set OPENVPN_SMOKE_BINARY=/path/to/openvpn to run it")
	return ""
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("live openvpn smoke test needs root (tun device creation, iptables)")
	}
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

// generateSmokePKI builds a throwaway CA + server cert/key pair and a real
// tls-crypt static key - the latter via the discovered openvpn binary's own
// "--genkey secret", so the format is exactly what real openvpn produces,
// not a hand-rolled approximation - matching the shape the panel is expected
// to send in Backend.config's "pki" object (see config.go's PKI doc
// comment).
func generateSmokePKI(t *testing.T, bin string) PKI {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "PasarGuard-OpenVPN-Smoke-CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "smoke-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"smoke-server"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}

	pemBlock := func(typ string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
	}
	pemKey := func(key *rsa.PrivateKey) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	}

	keyPath := filepath.Join(t.TempDir(), "tls-crypt.key")
	if out, err := exec.Command(bin, "--genkey", "secret", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("openvpn --genkey secret: %v: %s", err, string(out))
	}
	rawKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read generated tls-crypt key: %v", err)
	}
	begin := strings.Index(string(rawKey), "-----BEGIN OpenVPN Static key V1-----")
	if begin < 0 {
		t.Fatalf("unexpected --genkey output: %s", string(rawKey))
	}
	tlsCrypt := strings.TrimSpace(string(rawKey)[begin:])

	return PKI{
		CACert:      pemBlock("CERTIFICATE", caDER),
		ServerCert:  pemBlock("CERTIFICATE", serverDER),
		ServerKey:   pemKey(serverKey),
		TLSCryptKey: tlsCrypt,
	}
}

// TestLiveSmoke_OpenVPNServerStartsAndAcceptsRealClient is the strongest
// evidence in this package that the whole chain works: it renders a real
// config, spawns a real openvpn 2.x server process via New(), then drives a
// second, real openvpn process as a client against it - exercising config
// rendering, process spawn, the management-socket handshake, live
// CLIENT:CONNECT -> client-auth authentication against the in-memory
// authStore, CLIENT:ESTABLISHED online tracking, and CLIENT:DISCONNECT
// teardown, all against genuine openvpn binaries on both ends (not mocks -
// see management_test.go for the protocol-conformance tests driven against a
// fake server). Finishes by verifying NAT rule cleanup on Shutdown.
func TestLiveSmoke_OpenVPNServerStartsAndAcceptsRealClient(t *testing.T) {
	requireRoot(t)
	bin := discoverOpenVPNBinary(t)

	dir := t.TempDir()
	port := freeUDPPort(t)
	pki := generateSmokePKI(t, bin)
	const network = "10.250.250.0/24"

	raw := fmt.Sprintf(`{
		"instances": [{
			"tag": "udp-main",
			"protocol": "udp",
			"port": %d,
			"network": %q,
			"verb": 3
		}],
		"pki": {
			"ca_cert": %q,
			"server_cert": %q,
			"server_key": %q,
			"tls_crypt_key": %q
		}
	}`, port, network, pki.CACert, pki.ServerCert, pki.ServerKey, pki.TLSCryptKey)

	ovConfig, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}

	testUser := &common.User{
		Email:    "smoketest@example.com",
		Inbounds: []string{"udp-main"},
		Proxies: &common.Proxy{
			OpenVpn: &common.OpenVpnUser{Username: "smoketest", Password: "smoketestpass"},
		},
	}

	nodeCfg := &config.Config{
		OpenVPNExecutablePath:      bin,
		GeneratedConfigPath:        dir,
		LogBufferSize:              1000,
		StartupLogTailSize:         200,
		StatsUpdateIntervalSeconds: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	backend, err := New(ctx, ovConfig, []*common.User{testUser}, nodeCfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer backend.Shutdown()

	if !backend.Started() {
		t.Fatal("expected openvpn backend to report Started() == true")
	}
	t.Logf("openvpn version: %q", backend.Version())

	if !natRuleExists(network) {
		t.Error("expected NAT MASQUERADE rule to be present after Start")
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

	// Drive a REAL openvpn client against the real server to exercise the
	// full CLIENT:CONNECT -> client-auth -> CLIENT:ESTABLISHED chain, not
	// just a fake protocol probe.
	userpassPath := filepath.Join(dir, "userpass.txt")
	if err := os.WriteFile(userpassPath, []byte("smoketest\nsmoketestpass\n"), 0o600); err != nil {
		t.Fatalf("write userpass file: %v", err)
	}

	clientConfig := fmt.Sprintf(`client
dev tun
proto udp
remote 127.0.0.1 %d
resolv-retry infinite
nobind
persist-key
explicit-exit-notify 1
auth-user-pass %s
remote-cert-tls server
verb 3
<ca>
%s
</ca>
<tls-crypt>
%s
</tls-crypt>
`, port, userpassPath, strings.TrimSpace(pki.CACert), strings.TrimSpace(pki.TLSCryptKey))

	clientConfigPath := filepath.Join(dir, "client.ovpn")
	if err := os.WriteFile(clientConfigPath, []byte(clientConfig), 0o600); err != nil {
		t.Fatalf("write client config: %v", err)
	}

	clientCmd := exec.Command(bin, "--config", clientConfigPath)
	var clientLog strings.Builder
	clientCmd.Stdout = &clientLog
	clientCmd.Stderr = &clientLog
	if err := clientCmd.Start(); err != nil {
		t.Fatalf("start openvpn client: %v", err)
	}
	clientDone := make(chan error, 1)
	go func() { clientDone <- clientCmd.Wait() }()

	// sync.Once: this helper is called once explicitly (to trigger and
	// observe a clean disconnect) and once more via defer as a safety net for
	// early-failure (t.Fatal) paths - without the Once, the second call would
	// pointlessly block for the full 5s timeout below, since clientDone only
	// ever delivers its one value once.
	var stopOnce sync.Once
	stopClient := func() {
		stopOnce.Do(func() {
			if clientCmd.Process == nil {
				return
			}
			_ = clientCmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-clientDone:
			case <-time.After(5 * time.Second):
				_ = clientCmd.Process.Kill()
			}
		})
	}
	defer stopClient()

	onlineCtx, onlineCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer onlineCancel()
waitEstablished:
	for {
		stat, statErr := backend.GetUserOnlineStats(context.Background(), "smoketest@example.com")
		if statErr == nil && stat.GetValue() > 0 {
			break waitEstablished
		}
		select {
		case <-onlineCtx.Done():
			t.Fatalf("timed out waiting for real openvpn client to establish; client log:\n%s", clientLog.String())
		case err := <-clientDone:
			t.Fatalf("openvpn client exited early: %v; client log:\n%s", err, clientLog.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Log("real openvpn client successfully authenticated and established a session")

	stopClient()

	offlineCtx, offlineCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer offlineCancel()
waitOffline:
	for {
		stat, statErr := backend.GetUserOnlineStats(context.Background(), "smoketest@example.com")
		if statErr == nil && stat.GetValue() == 0 {
			break waitOffline
		}
		select {
		case <-offlineCtx.Done():
			t.Fatal("timed out waiting for online count to drop to 0 after client disconnect")
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Log("server correctly observed the client disconnect")

	backend.Shutdown()

	if natRuleExists(network) {
		t.Error("expected NAT MASQUERADE rule to be removed after Shutdown")
	}
}
