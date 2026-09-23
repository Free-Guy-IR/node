package mtproto

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib"
	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const (
	initialSecretA = "0123456789abcdef0123456789abcdef"
	initialSecretB = "fedcba9876543210fedcba9876543210"
	initialSecretC = "00112233445566778899aabbccddeeff"
	testAdTag      = "0123456789abcdef0123456789abcdef"
	unknownDC      = 999

	handshakeAccepted = "accepted"
	handshakeRefused  = "refused"
)

const (
	firstTestPort = 20000
	testPortSpan  = 10000
)

func unusedListenPorts(t *testing.T, n int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()

	ports := make([]int, 0, n)
	for attempt := 0; len(ports) < n; attempt++ {
		if attempt == 1000 {
			t.Fatal("cannot find an unused listen port")
		}
		port := firstTestPort + rand.IntN(testPortSpan)
		loopback, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		loopback.Close()
		l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		listeners = append(listeners, l)
		ports = append(ports, port)
	}
	return ports
}

func credentialedUser(email, secret string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Mtproto: &common.MtprotoUser{Username: email, Secret: secret},
		},
	}
}

func secretKey(t *testing.T, secret string) [mtglib.SecretKeyLength]byte {
	t.Helper()
	raw, err := hex.DecodeString(secret)
	if err != nil || len(raw) != mtglib.SecretKeyLength {
		t.Fatalf("bad test secret %q: %v", secret, err)
	}
	var key [mtglib.SecretKeyLength]byte
	copy(key[:], raw)
	return key
}

func startBackend(t *testing.T, raw string, users []*common.User) *Backend {
	t.Helper()
	mtCfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	backend, err := New(context.Background(), mtCfg, users, &config.Config{
		LogBufferSize:              4096,
		StatsUpdateIntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return backend
}

func authorisedEntries(backend *Backend) map[string]secretEntry {
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	out := make(map[string]secretEntry, len(backend.secretsByID))
	for id, entry := range backend.secretsByID {
		out[id] = entry
	}
	return out
}

func sortedIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func assertAuthorised(t *testing.T, backend *Backend, email, secret string) {
	t.Helper()
	entries := authorisedEntries(backend)
	entry, ok := entries[email]
	if !ok {
		t.Fatalf("%s must be authorised right after New without any user sync, authorised ids: %v", email, sortedIDs(entries))
	}
	if entry.email != email || entry.keyBytes != secretKey(t, secret) {
		t.Fatalf("%s is authorised with the wrong credential: email=%q", email, entry.email)
	}
}

func assertRefused(t *testing.T, backend *Backend, email string) {
	t.Helper()
	if _, ok := authorisedEntries(backend)[email]; ok {
		t.Fatalf("%s carries mtproto credentials but no instance tag of this backend and must stay refused", email)
	}
}

func assertInstanceSecret(t *testing.T, backend *Backend, domain, email, secret string) {
	t.Helper()
	secrets := backend.secretsForInstance(domain)
	got, ok := secrets[email]
	if !ok {
		t.Fatalf("instance secrets for domain %q must carry %s right after New, got %v", domain, email, sortedIDs(secrets))
	}
	if got.Host != domain || got.Key != secretKey(t, secret) {
		t.Fatalf("instance secret for %s is wrong: host=%q", email, got.Host)
	}
}

func assertInstanceSecretAbsent(t *testing.T, backend *Backend, domain, email string) {
	t.Helper()
	if _, ok := backend.secretsForInstance(domain)[email]; ok {
		t.Fatalf("instance secrets for domain %q must not carry %s", domain, email)
	}
}

func awaitServing(t *testing.T, backend *Backend, tag string, port int) {
	t.Helper()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	marker := "[mtproto." + tag + ".proxy] "
	logs := backend.Logs()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			conn.Close()
		}
		wait := time.After(200 * time.Millisecond)
	drain:
		for {
			select {
			case line := <-logs:
				if strings.HasPrefix(line, marker) &&
					(strings.Contains(line, "Stream has been started") || strings.Contains(line, "ip was rejected by allowlist")) {
					return
				}
			case <-wait:
				break drain
			}
		}
	}
	t.Fatalf("instance %q never accepted a connection", tag)
}

func handshakeOutcomeFromLogs(backend *Backend) string {
	logs := backend.Logs()
	for {
		select {
		case line := <-logs:
			switch {
			case strings.Contains(line, "cannot dial to telegram"):
				return handshakeAccepted
			case strings.Contains(line, "plain handshake is failed"):
				return handshakeRefused
			}
		default:
			return ""
		}
	}
}

func plainHandshakeOutcome(t *testing.T, backend *Backend, port int, secret string) string {
	t.Helper()
	key, err := hex.DecodeString(secret)
	if err != nil {
		t.Fatalf("bad test secret %q: %v", secret, err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		tcpConn := conn.(*net.TCPConn)
		if _, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(tcpConn, unknownDC); err != nil {
			conn.Close()
			t.Fatalf("cannot send the client handshake: %v", err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		io.Copy(io.Discard, conn)
		conn.Close()

		if outcome := handshakeOutcomeFromLogs(backend); outcome != "" {
			return outcome
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the proxy reported no handshake outcome within the deadline")
	return ""
}

func TestNewAuthorisesInitialUsersOnFakeTLSInstance(t *testing.T) {
	ports := unusedListenPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"}]}`, ports[0])

	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, "hy2-in", "mtproto-tg"),
		credentialedUser("b@x", initialSecretB, "hy2-in"),
	})
	defer backend.Shutdown()
	defer awaitServing(t, backend, "mtproto-tg", ports[0])

	assertAuthorised(t, backend, "a@x", initialSecretA)
	assertRefused(t, backend, "b@x")
	assertInstanceSecret(t, backend, "localhost", "a@x", initialSecretA)
	assertInstanceSecretAbsent(t, backend, "localhost", "b@x")
}

func TestNewAuthorisesInitialUsersOnMiddleProxyInstance(t *testing.T) {
	ports := unusedListenPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, ports[0], testAdTag)

	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, "mtproto-tg-ad"),
		credentialedUser("b@x", initialSecretB, "hy2-in"),
	})
	defer backend.Shutdown()

	inst, ok := backend.instances["mtproto-tg-ad"]
	if !ok || inst.mp == nil || inst.proxy != nil {
		t.Fatal("the ad_tag instance must run on the middle-proxy path")
	}

	assertAuthorised(t, backend, "a@x", initialSecretA)
	assertRefused(t, backend, "b@x")
}

func TestNewWithoutInitialUsersAuthorisesNobody(t *testing.T) {
	ports := unusedListenPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"}]}`, ports[0])

	backend := startBackend(t, raw, nil)
	defer backend.Shutdown()
	defer awaitServing(t, backend, "mtproto-tg", ports[0])

	if entries := authorisedEntries(backend); len(entries) != 0 {
		t.Fatalf("no initial users must authorise nobody, got %v", sortedIDs(entries))
	}
	if secrets := backend.secretsForInstance("localhost"); len(secrets) != 0 {
		t.Fatalf("no initial users must leave the instance secrets empty, got %v", sortedIDs(secrets))
	}
}

func TestNewAuthorisesUserListedOnlyOnTheSecondInstance(t *testing.T) {
	ports := unusedListenPorts(t, 2)
	raw := fmt.Sprintf(`{"instances":[`+
		`{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"},`+
		`{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"}]}`, ports[0], ports[1])

	backend := startBackend(t, raw, []*common.User{
		credentialedUser("c@x", initialSecretC, "hy2-in", "mtproto-tg-plain"),
		credentialedUser("b@x", initialSecretB, "hy2-in"),
	})
	defer backend.Shutdown()
	defer awaitServing(t, backend, "mtproto-tg-plain", ports[1])
	defer awaitServing(t, backend, "mtproto-tg", ports[0])

	assertAuthorised(t, backend, "c@x", initialSecretC)
	assertRefused(t, backend, "b@x")
	assertInstanceSecret(t, backend, "localhost", "c@x", initialSecretC)
	assertInstanceSecret(t, backend, "", "c@x", initialSecretC)

	if outcome := plainHandshakeOutcome(t, backend, ports[1], initialSecretC); outcome != handshakeAccepted {
		t.Fatalf("a client holding c@x's secret must pass the second instance's handshake right after New, got %s", outcome)
	}
}

func TestNewPlainInstanceAcceptsInitialUserHandshakeWithoutAResync(t *testing.T) {
	ports := unusedListenPorts(t, 1)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-plain","port":%d,"mode":"plain"}]}`, ports[0])

	backend := startBackend(t, raw, []*common.User{
		credentialedUser("a@x", initialSecretA, "mtproto-tg-plain"),
		credentialedUser("b@x", initialSecretB, "hy2-in"),
	})
	defer backend.Shutdown()
	defer awaitServing(t, backend, "mtproto-tg-plain", ports[0])

	if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretA); outcome != handshakeAccepted {
		t.Fatalf("a client holding a@x's secret must pass the handshake right after New, got %s", outcome)
	}
	if outcome := plainHandshakeOutcome(t, backend, ports[0], initialSecretB); outcome != handshakeRefused {
		t.Fatalf("a client holding b@x's secret must be refused, got %s", outcome)
	}
}
