package middleproxy

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib/obfuscation"
)

type recordingTracker struct {
	mu            sync.Mutex
	begun         map[string]bool
	ended         map[string]bool
	authenticated chan func()
}

func (r *recordingTracker) Begin(streamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begun[streamID] = true
}

func (r *recordingTracker) Authenticated(streamID, username string, closer func()) {
	r.authenticated <- closer
}

func (r *recordingTracker) End(streamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended[streamID] = true
}

func (r *recordingTracker) begunCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.begun)
}

func startSilentMiddleProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("stub middle proxy listen: %v", err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, conn := range held {
			conn.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	return listener.Addr().String()
}

func TestMiddleProxySessionIsClosedByItsRevokeCloser(t *testing.T) {
	stub := startSilentMiddleProxy(t)
	tgInitOnce.Do(func() {})
	tg.mu.Lock()
	tg.secret = make([]byte, 16)
	tg.v4Addresses = map[DC][]string{DefaultDC: {stub}}
	tg.v4Default = DefaultDC
	tg.mu.Unlock()

	key := []byte("0123456789abcdef")
	tracker := &recordingTracker{begun: map[string]bool{}, ended: map[string]bool{}, authenticated: make(chan func(), 1)}
	var begunBeforeSecrets atomic.Bool
	inst := New(Options{
		AdTag:    make([]byte, 16),
		Sessions: tracker,
		Secrets: func() []SecretEntry {
			begunBeforeSecrets.Store(tracker.begunCount() == 1)
			return []SecretEntry{{Username: "a", Email: "a@x", Key: key}}
		},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go inst.Serve(listener)
	t.Cleanup(func() {
		inst.Shutdown()
		listener.Close()
	})

	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	session, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(conn.(*net.TCPConn), int(DefaultDC))
	if err != nil {
		conn.Close()
		t.Fatalf("client handshake: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	var closer func()
	select {
	case closer = <-tracker.authenticated:
	case <-time.After(5 * time.Second):
		t.Fatal("the middle-proxy path never registered the authenticated session with its tracker")
	}
	if !begunBeforeSecrets.Load() {
		t.Fatal("the middle-proxy path read the secrets before recording the stream's start")
	}

	closer()

	session.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := session.Read(make([]byte, 16)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the revoked middle-proxy session must be closed within 2s, got %v", err)
	}
}
