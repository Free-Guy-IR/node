//go:build linux

package singbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

// TestLiveSmoke_ConcurrentSyncUserCoalescesIntoOneRestart is the regression
// test for the production incident this fix addresses: every SyncUser call
// used to trigger its own full sing-box restart, so a busy panel calling
// SyncUser every few minutes (new signups, subscription refreshes, etc.) was
// disconnecting every other connected user for a few seconds each time -
// confirmed live via production logs showing restarts seconds apart, each
// immediately preceded by exactly one SyncUser call.
//
// This measures wall-clock time rather than counting restarts directly
// (there is no exported restart counter, and adding one purely for a test
// would be its own small risk) - it establishes a single-call baseline, then
// fires N calls concurrently and asserts the batch completes in close to
// that same baseline rather than N times it, which is only possible if the
// N calls were coalesced into one restart instead of N sequential ones.
func TestLiveSmoke_ConcurrentSyncUserCoalescesIntoOneRestart(t *testing.T) {
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

	cfg := &config.Config{
		SingBoxExecutablePath: bin,
		GeneratedConfigPath:   dir,
		LogBufferSize:         1000,
		StartupLogTailSize:    200,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sb, err := New(ctx, sbConfig, nil, apiPort, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer sb.Shutdown()

	syncCtx, syncCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer syncCancel()

	// Baseline: one isolated SyncUser call, letting its debounce window
	// elapse on its own (nothing else queued behind it).
	baselineUser := &common.User{
		Email:    "baseline@example.com",
		Inbounds: []string{"hy2-in"},
		Proxies:  &common.Proxy{Hysteria2: &common.Hysteria2{Password: "baselinepass"}},
	}
	baselineStart := time.Now()
	if err := sb.SyncUser(syncCtx, baselineUser); err != nil {
		t.Fatalf("baseline SyncUser() error = %v", err)
	}
	baseline := time.Since(baselineStart)
	t.Logf("baseline single SyncUser duration: %v", baseline)

	// Let things settle before firing the concurrent batch, so this batch's
	// debounce window starts cleanly rather than possibly still overlapping
	// the baseline's.
	time.Sleep(200 * time.Millisecond)

	const concurrentCalls = 5
	var wg sync.WaitGroup
	errs := make([]error, concurrentCalls)

	batchStart := time.Now()
	for i := 0; i < concurrentCalls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := &common.User{
				Email:    fmt.Sprintf("concurrent%d@example.com", i),
				Inbounds: []string{"hy2-in"},
				Proxies:  &common.Proxy{Hysteria2: &common.Hysteria2{Password: fmt.Sprintf("pass%d", i)}},
			}
			errs[i] = sb.SyncUser(syncCtx, u)
		}(i)
	}
	wg.Wait()
	batchDuration := time.Since(batchStart)

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent SyncUser[%d]() error = %v", i, err)
		}
	}

	t.Logf("%d concurrent SyncUser calls total duration: %v (baseline was %v)", concurrentCalls, batchDuration, baseline)

	// If each call restarted sing-box separately, batchDuration would be on
	// the order of concurrentCalls*baseline. Coalesced into one restart, it
	// should stay close to a single baseline plus the debounce wait. 2.5x is
	// a safe margin: comfortably above normal variance, comfortably below
	// what N separate sequential restarts would produce.
	if maxAllowed := baseline * 5 / 2; batchDuration > maxAllowed {
		t.Errorf("concurrent batch took %v, want <= %v (%.1fx baseline) - restarts were not coalesced",
			batchDuration, maxAllowed, float64(batchDuration)/float64(baseline))
	}

	// All five users from the batch must actually be present in the final
	// config - coalescing must not drop any caller's change.
	generatedBytes, err := os.ReadFile(filepath.Join(dir, "singbox.json"))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	generated := string(generatedBytes)
	for i := 0; i < concurrentCalls; i++ {
		email := fmt.Sprintf("concurrent%d@example.com", i)
		if !strings.Contains(generated, email) {
			t.Errorf("expected generated config to contain batched user %q; got:\n%s", email, generated)
		}
	}
	if !strings.Contains(generated, "baseline@example.com") {
		t.Errorf("expected generated config to still contain earlier-synced baseline user; got:\n%s", generated)
	}
}

// TestQueueUser_TightSequentialCallsCoalesceIntoOneRestart is the regression
// test for the real production call pattern this fix targets: the panel's
// node bridge sends every pending user for a node over ONE client-streaming
// SyncUser RPC, and controller/rpc's handler only reads the next message
// once the previous one's backend call has returned - so a debounce window
// built on a *blocking* SyncUser can never actually see more than one
// message at a time, no matter how large the window is (confirmed
// empirically against a real panel: 8 users synced back-to-back on one
// stream still produced 7 restarts with the blocking version of this fix).
//
// QueueUser exists so that receive loop can dispatch a message and
// immediately go back to reading the next one instead of blocking. This
// test simulates that loop directly - tight sequential calls, no
// goroutines, no delay, exactly how controller/rpc's SyncUser drains an
// already-buffered stream - and asserts the batch still ends up with every
// user and still costs exactly one restart's wait, not N of them.
func TestQueueUser_TightSequentialCallsCoalesceIntoOneRestart(t *testing.T) {
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

	cfg := &config.Config{
		SingBoxExecutablePath: bin,
		GeneratedConfigPath:   dir,
		LogBufferSize:         1000,
		StartupLogTailSize:    200,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sb, err := New(ctx, sbConfig, nil, apiPort, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer sb.Shutdown()

	syncCtx, syncCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer syncCancel()

	const n = 6
	var b *singBoxPendingBatch
	for i := 0; i < n; i++ {
		u := &common.User{
			Email:    fmt.Sprintf("queued%d@example.com", i),
			Inbounds: []string{"hy2-in"},
			Proxies:  &common.Proxy{Hysteria2: &common.Hysteria2{Password: fmt.Sprintf("qpass%d", i)}},
		}
		if err := sb.QueueUser(syncCtx, u); err != nil {
			t.Fatalf("QueueUser[%d]() error = %v", i, err)
		}
		if i == 0 {
			sb.batchMu.Lock()
			b = sb.batch
			sb.batchMu.Unlock()
			if b == nil {
				t.Fatal("expected a pending batch to exist right after the first QueueUser call")
			}
		}
	}

	select {
	case <-b.done:
	case <-time.After(15 * time.Second):
		t.Fatal("batch did not flush within 15s - QueueUser calls did not coalesce as expected")
	}
	if b.err != nil {
		t.Fatalf("batch flush error = %v", b.err)
	}

	generatedBytes, err := os.ReadFile(filepath.Join(dir, "singbox.json"))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	generated := string(generatedBytes)
	for i := 0; i < n; i++ {
		email := fmt.Sprintf("queued%d@example.com", i)
		if !strings.Contains(generated, email) {
			t.Errorf("expected generated config to contain queued user %q; got:\n%s", email, generated)
		}
	}
}

// TestSyncUser_UnchangedUserSetSkipsRestart is the regression test for the
// Hysteria2 "won't connect" incident: the panel syncs users constantly, but
// most syncs are traffic/online updates that leave every hysteria2 inbound's
// user set unchanged. Restarting on those dropped every Hysteria2 (QUIC)
// session for no reason. The fix skips the restart when the user set is
// identical - proven here by the generated config file's mtime, which the
// restart rewrites and a skipped sync leaves untouched.
func TestSyncUser_UnchangedUserSetSkipsRestart(t *testing.T) {
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
	cfg := &config.Config{
		SingBoxExecutablePath: bin,
		GeneratedConfigPath:   dir,
		LogBufferSize:         1000,
		StartupLogTailSize:    200,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sb, err := New(ctx, sbConfig, nil, apiPort, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer sb.Shutdown()

	cfgPath := filepath.Join(dir, "singbox.json")
	user := &common.User{
		Email:    "steady@example.com",
		Inbounds: []string{"hy2-in"},
		Proxies:  &common.Proxy{Hysteria2: &common.Hysteria2{Password: "steadypass"}},
	}

	// First sync installs the user and restarts (writes the config file).
	if err := sb.SyncUser(ctx, user); err != nil {
		t.Fatalf("first SyncUser() error = %v", err)
	}
	st1, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat after first sync: %v", err)
	}

	// Syncing the identical user again changes no hysteria2 user set, so it
	// must NOT restart - the config file's mtime must be unchanged.
	time.Sleep(300 * time.Millisecond)
	if err := sb.SyncUser(ctx, user); err != nil {
		t.Fatalf("no-op SyncUser() error = %v", err)
	}
	st2, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat after no-op sync: %v", err)
	}
	if !st2.ModTime().Equal(st1.ModTime()) {
		t.Errorf("no-op sync restarted the core (config mtime changed %v -> %v); the identical user set should skip the restart",
			st1.ModTime(), st2.ModTime())
	}

	// A genuine change (a new user) must still restart - mtime advances.
	other := &common.User{
		Email:    "newcomer@example.com",
		Inbounds: []string{"hy2-in"},
		Proxies:  &common.Proxy{Hysteria2: &common.Hysteria2{Password: "newpass"}},
	}
	time.Sleep(300 * time.Millisecond)
	if err := sb.SyncUser(ctx, other); err != nil {
		t.Fatalf("changing SyncUser() error = %v", err)
	}
	st3, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat after changing sync: %v", err)
	}
	if st3.ModTime().Equal(st2.ModTime()) {
		t.Errorf("adding a new hysteria2 user did not restart the core (config mtime unchanged) - a real change must apply")
	}
}
