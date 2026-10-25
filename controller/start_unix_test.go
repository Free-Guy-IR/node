//go:build !windows

package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

func xrayStartRequest(t *testing.T) *common.Backend {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "vless",
			"listen":   "127.0.0.1",
			"port":     netutil.FindFreePort(),
			"protocol": "vless",
			"settings": map[string]any{"clients": []any{}, "decryption": "none"},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "DIRECT"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &common.Backend{
		Type:      common.BackendType_XRAY,
		Config:    string(raw),
		KeepAlive: 60,
		Users: []*common.User{{
			Email:    "1",
			Inbounds: []string{"vless"},
			Proxies:  &common.Proxy{Vless: &common.Vless{Id: uuid.NewString()}},
		}},
	}
}

func newFakeXrayController(t *testing.T) *Controller {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "xray")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray 26.1.1 (fake)'; exit 0; fi\nexec sleep 60\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewTestConfig(dir, uuid.New())
	cfg.XrayExecutablePath = executable
	cfg.XrayAssetsPath = dir
	c := New(cfg)
	t.Cleanup(c.Close)
	return c
}

func waitForStartInProgress(t *testing.T, c *Controller) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.lifecycleMu.TryRLock() {
		c.lifecycleMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("no start in progress after 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDisconnectAbortsAnXrayStartInProgress(t *testing.T) {
	c := newFakeXrayController(t)
	data := xrayStartRequest(t)

	startErr := make(chan error, 1)
	go func() {
		c.LockControl()
		defer c.UnlockControl()
		startErr <- c.ReplaceBackend(context.Background(), data)
	}()
	waitForStartInProgress(t, c)

	disconnected := make(chan struct{})
	go func() {
		c.Disconnect()
		close(disconnected)
	}()

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("a start aborted by Disconnect reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the start still runs 10s after Disconnect")
	}
	select {
	case <-disconnected:
	case <-time.After(10 * time.Second):
		t.Fatal("Disconnect still blocked 10s after the start ended")
	}
	if c.Backend() != nil {
		t.Fatal("an aborted start left a backend attached")
	}
}
