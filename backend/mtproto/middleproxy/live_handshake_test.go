//go:build live

package middleproxy

import (
	"context"
	"testing"
	"time"
)

// TestLiveDialMiddleProxy is a real network test against Telegram's own
// middle-proxy infrastructure (core.telegram.org/getProxyConfig{,V6} +
// getProxySecret, then a live TCP connection to one of the returned
// servers). It verifies the nonce exchange and AES-CBC handshake this
// package ported from mtg v1 actually complete against the real service -
// the single highest-risk piece of this port, since a mistake in either the
// frame codec or the key-derivation math would show up as a handshake
// failure here, not as a Go compile error.
//
// Gated behind the "live" build tag so `go test ./...` never makes network
// calls to Telegram by default; run explicitly with
// `go test -tags live ./backend/mtproto/middleproxy/...` when verifying
// this port after a change.
func TestLiveDialMiddleProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := ensureTelegramInfo(ctx); err != nil {
		t.Fatalf("ensureTelegramInfo failed: %v", err)
	}
	t.Logf("candidate addresses for dc=2: %v, secret length: %d", tg.Addresses(DefaultDC), len(tg.Secret()))

	mp, err := dialMiddleProxy(ctx, DefaultDC)
	if err != nil {
		t.Fatalf("dialMiddleProxy failed: %v", err)
	}
	defer mp.Close()

	t.Logf("middle-proxy handshake succeeded, connected to %s", mp.raw.RemoteAddr())
}
