package singbox

import (
	"bytes"
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
	"fmt"
	"maps"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const (
	credentialTag        = "hy2-credential"
	credentialWarmup     = 256 * 1024
	credentialRefusal    = 2 * time.Second
	credentialFlushSleep = singBoxSyncDebounceWindow + 2*time.Second
)

func credentialSingBoxBinary(t *testing.T) string {
	t.Helper()

	for _, candidate := range []string{os.Getenv("SINGBOX_SMOKE_BINARY"), "/usr/local/bin/sing-box"} {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no sing-box binary found; set SINGBOX_SMOKE_BINARY")
	return ""
}

func credentialPort(t *testing.T, network string) int {
	t.Helper()

	if network == "udp" {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func hysteria2ClientBinary(t *testing.T) string {
	t.Helper()

	for _, candidate := range []string{os.Getenv("XRAY_EXECUTABLE_PATH"), "/usr/local/bin/xray"} {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no xray binary to act as the hysteria2 client; set XRAY_EXECUTABLE_PATH")
	return ""
}

func pinnedHysteria2Cert(t *testing.T) (string, string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
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
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return certFile, keyFile, hex.EncodeToString(sum[:])
}

func startByteTarget(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				chunk := bytes.Repeat([]byte{'x'}, 16*1024)
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for range ticker.C {
					if _, err := conn.Write(chunk); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

type hysteria2Node struct {
	sb     *SingBox
	port   int
	pin    string
	target int
	dir    string
	xray   string
}

func startHysteria2Node(t *testing.T, users []*common.User) *hysteria2Node {
	t.Helper()

	bin := credentialSingBoxBinary(t)
	xrayBinary := hysteria2ClientBinary(t)
	certFile, keyFile, pin := pinnedHysteria2Cert(t)
	port := credentialPort(t, "udp")
	raw := fmt.Sprintf(`{
		"log": {"level": "warn"},
		"inbounds": [{
			"type": "hysteria2",
			"tag": %q,
			"listen": "127.0.0.1",
			"listen_port": %d,
			"users": [],
			"tls": {"enabled": true, "alpn": ["h3"], "certificate_path": %q, "key_path": %q}
		}],
		"outbounds": [{"type": "direct"}]
	}`, credentialTag, port, certFile, keyFile)
	sbConfig, err := NewConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := &config.Config{
		SingBoxExecutablePath: bin,
		GeneratedConfigPath:   dir,
		LogBufferSize:         1000,
		StartupLogTailSize:    200,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sb, err := New(ctx, sbConfig, users, credentialPort(t, "tcp"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sb.Shutdown)
	return &hysteria2Node{sb: sb, port: port, pin: pin, target: startByteTarget(t), dir: dir, xray: xrayBinary}
}

func (n *hysteria2Node) client(t *testing.T, password string) int {
	t.Helper()

	clientPort := credentialPort(t, "tcp")
	clientConfig := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "in",
			"listen":   "127.0.0.1",
			"port":     clientPort,
			"protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": n.target, "network": "tcp"},
		}},
		"outbounds": []any{map[string]any{
			"tag":      "out",
			"protocol": "hysteria",
			"settings": map[string]any{"version": 2, "address": "127.0.0.1", "port": n.port},
			"streamSettings": map[string]any{
				"network":          "hysteria",
				"hysteriaSettings": map[string]any{"version": 2, "auth": password},
				"security":         "tls",
				"tlsSettings": map[string]any{
					"serverName":           "localhost",
					"alpn":                 []string{"h3"},
					"pinnedPeerCertSha256": n.pin,
				},
			},
		}},
	}
	raw, err := json.Marshal(clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configFile := filepath.Join(dir, "client.json")
	if err := os.WriteFile(configFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(n.xray, "run", "-c", configFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return clientPort
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("client inbound %d never listened", clientPort)
	return 0
}

func transferred(t *testing.T, clientPort int, want int64, window time.Duration) int64 {
	t.Helper()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var received atomic.Int64
	ended := make(chan struct{})
	go func() {
		buffer := make([]byte, 64*1024)
		for {
			n, err := conn.Read(buffer)
			received.Add(int64(n))
			if err != nil {
				close(ended)
				return
			}
		}
	}()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) && received.Load() < want {
		select {
		case <-ended:
			deadline = time.Now()
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = conn.Close()
	<-ended
	return received.Load()
}

func (n *hysteria2Node) drainTraffic(t *testing.T) map[string]int64 {
	t.Helper()

	resp, err := n.sb.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
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

func (n *hysteria2Node) generatedUsers(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(n.dir, "singbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	inbound := inboundByTag(decode(t, raw), credentialTag)
	if inbound == nil {
		t.Fatalf("the generated config has no %s inbound", credentialTag)
	}
	names := make([]string, 0, len(inbound.Users))
	for _, user := range inbound.Users {
		names = append(names, user.Name)
	}
	return names
}

func TestAnEmptyHysteria2PasswordIsNeverAccepted(t *testing.T) {
	const bobSecret = "bob-own-hy2-key"
	node := startHysteria2Node(t, []*common.User{hy2User("1", "", credentialTag), hy2User("2", bobSecret, credentialTag)})

	if listed := node.generatedUsers(t); !slices.Equal(listed, []string{"2"}) {
		t.Fatalf("the config handed to sing-box lists %v on the hysteria2 inbound, want only the user with a password", listed)
	}

	node.drainTraffic(t)
	if moved := transferred(t, node.client(t, ""), credentialWarmup, credentialRefusal); moved != 0 {
		t.Fatalf("a client with an empty hysteria2 password moved %d bytes (%v)", moved, node.drainTraffic(t))
	}
	if moved := transferred(t, node.client(t, bobSecret), credentialWarmup, 15*time.Second); moved < credentialWarmup {
		t.Fatalf("the user with a password moved only %d bytes", moved)
	}

	if err := node.sb.SyncUser(context.Background(), hy2User("2", "", credentialTag)); err != nil {
		t.Fatalf("emptying the password failed the sync: %v", err)
	}
	time.Sleep(credentialFlushSleep)
	if moved := transferred(t, node.client(t, bobSecret), credentialWarmup, credentialRefusal); moved != 0 {
		t.Fatalf("the user whose password became empty still connected with the old password and moved %d bytes", moved)
	}
	if moved := transferred(t, node.client(t, ""), credentialWarmup, credentialRefusal); moved != 0 {
		t.Fatalf("a client with an empty hysteria2 password moved %d bytes after a user's password became empty", moved)
	}
}

func TestSharedHysteria2PasswordBillsTheSameUserEveryTime(t *testing.T) {
	const shared = "0123456789abcdefSHARED"
	users := []*common.User{hy2User("10", shared, credentialTag), hy2User("9", shared, credentialTag)}
	node := startHysteria2Node(t, users)

	for round := range 3 {
		if round > 0 {
			if err := node.sb.SyncUsers(context.Background(), users); err != nil {
				t.Fatalf("round %d: resync: %v", round, err)
			}
		}
		if listed := node.generatedUsers(t); !slices.Equal(listed, []string{"10", "9"}) {
			t.Fatalf("round %d: the config handed to sing-box lists %v, want them in name order", round, listed)
		}
		node.drainTraffic(t)
		if moved := transferred(t, node.client(t, shared), credentialWarmup, 15*time.Second); moved < credentialWarmup {
			t.Fatalf("round %d: the shared-password client moved only %d bytes", round, moved)
		}
		time.Sleep(1500 * time.Millisecond)
		traffic := node.drainTraffic(t)
		if billed := slices.Sorted(maps.Keys(traffic)); !slices.Equal(billed, []string{"9"}) {
			t.Fatalf("round %d: the shared-password client was billed to %v (%v), want the last user in name order every time", round, billed, traffic)
		}
		t.Logf("round %d: the shared-password client was billed to %v", round, traffic)
	}
}
