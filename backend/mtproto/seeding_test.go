package mtproto

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func newSeedTestBackend(t *testing.T, users []*common.User) *Backend {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free TCP port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	mtCfg, err := NewConfig(fmt.Sprintf(`{"instances": [{"tag": "main", "port": %d, "fake_tls_domain": "example.com"}]}`, port))
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}

	b, err := New(context.Background(), mtCfg, users, &config.Config{LogBufferSize: 100, StatsUpdateIntervalSeconds: 1})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b
}

func TestNewSeedsInitialUsers(t *testing.T) {
	b := newSeedTestBackend(t, []*common.User{mtprotoUser("seed@x", "main")})

	b.mu.RLock()
	entry, ok := b.secretsByID["seed@x"]
	b.mu.RUnlock()
	if !ok {
		t.Fatal("a user passed to New must be authorized immediately, without waiting for a sync")
	}
	if entry.email != "seed@x" {
		t.Fatalf("expected email seed@x, got %q", entry.email)
	}
}

func TestNewDropsUsersOnForeignInbounds(t *testing.T) {
	b := newSeedTestBackend(t, []*common.User{mtprotoUser("foreign@x", "hy2-in")})

	b.mu.RLock()
	_, ok := b.secretsByID["foreign@x"]
	b.mu.RUnlock()
	if ok {
		t.Fatal("a user not on any mtproto instance tag must not be authorized")
	}
}

func TestRestartConcurrentWithReaders(t *testing.T) {
	b := newSeedTestBackend(t, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			b.Started()
		}
	}()

	if err := b.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	<-done

	if !b.Started() {
		t.Fatal("backend must report started after a successful restart")
	}
}

func TestRestartAfterShutdownFails(t *testing.T) {
	b := newSeedTestBackend(t, nil)
	b.Shutdown()

	if err := b.Restart(); err == nil {
		t.Fatal("Restart after Shutdown must fail instead of leaking listeners")
	}
}
