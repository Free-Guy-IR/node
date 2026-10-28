//go:build !windows

package xray

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	statsCommand "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

type fakeStatsService struct {
	statsCommand.UnimplementedStatsServiceServer
	healthy atomic.Bool
	block   bool
	probes  chan struct{}
}

func (s *fakeStatsService) GetSysStats(ctx context.Context, _ *statsCommand.SysStatsRequest) (*statsCommand.SysStatsResponse, error) {
	if s.probes != nil {
		select {
		case s.probes <- struct{}{}:
		default:
		}
	}
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if !s.healthy.Load() {
		return nil, status.Error(codes.Unavailable, "xray is booting")
	}
	return &statsCommand.SysStatsResponse{}, nil
}

func newFakeCoreXray(t *testing.T, stats *fakeStatsService) *Xray {
	t.Helper()

	dir := t.TempDir()
	executable := filepath.Join(dir, "xray")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	statsCommand.RegisterStatsServiceServer(server, stats)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	handler, err := api.NewXrayAPI(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)

	cfg, err := NewConfig(`{"inbounds":[]}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	core := &Core{
		executablePath: executable,
		assetsPath:     dir,
		configPath:     dir,
		logsChan:       make(chan string, 100),
		logPhase:       logPhaseRuntime,
		startupLogSize: 200,
		runtimeLogs:    newStartupLogRing(10),
	}
	t.Cleanup(core.Stop)

	_, cancel := context.WithCancel(context.Background())
	return &Xray{config: cfg, cfg: &config.Config{}, core: core, handler: handler, cancelFunc: cancel}
}

func corePID(x *Xray) int {
	x.core.mu.Lock()
	defer x.core.mu.Unlock()
	return x.core.processPID
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSyncUsersDoesNotStartXrayAfterShutdown(t *testing.T) {
	x := newFakeCoreXray(t, &fakeStatsService{})

	syncErr := make(chan error, 1)
	go func() { syncErr <- x.SyncUsers(context.Background(), nil) }()
	waitUntil(t, "the full sync starts its candidate core", x.core.Started)

	x.Shutdown()

	for start := time.Now(); time.Since(start) < 3*time.Second; time.Sleep(10 * time.Millisecond) {
		if x.core.Started() {
			t.Fatal("the full-sync rollback started xray again after Shutdown")
		}
	}
	select {
	case err := <-syncErr:
		if err == nil {
			t.Fatal("SyncUsers reported success after Shutdown")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SyncUsers still running 10s after Shutdown")
	}
}

func TestHealthLoopStopsWhenCancelledDuringAProbe(t *testing.T) {
	stats := &fakeStatsService{block: true, probes: make(chan struct{}, 1)}
	x := newFakeCoreXray(t, stats)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		x.checkXrayHealth(ctx)
		close(done)
	}()

	select {
	case <-stats.probes:
	case <-time.After(5 * time.Second):
		t.Fatal("the health loop sent no probe")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("the health loop still runs 1s after its context was cancelled (xray restarted: %v)", x.core.Started())
	}
	if x.core.Started() {
		t.Fatal("the health loop restarted xray after its context was cancelled")
	}
}

func TestHealthRestartLeavesACoreThatAFullSyncReplaced(t *testing.T) {
	stats := &fakeStatsService{probes: make(chan struct{}, 1)}
	x := newFakeCoreXray(t, stats)

	x.syncMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go x.checkXrayHealth(ctx)

	select {
	case <-stats.probes:
	case <-time.After(5 * time.Second):
		x.syncMu.Unlock()
		t.Fatal("the health loop sent no probe")
	}
	time.Sleep(300 * time.Millisecond)
	if x.core.Started() {
		x.syncMu.Unlock()
		t.Fatal("the health loop restarted xray while a full sync held syncMu")
	}

	if err := x.restartCoreWithConfig(x.config); err != nil {
		x.syncMu.Unlock()
		t.Fatal(err)
	}
	pid := corePID(x)
	stats.healthy.Store(true)
	x.syncMu.Unlock()

	time.Sleep(500 * time.Millisecond)
	if got := corePID(x); got != pid {
		t.Fatalf("the health loop restarted the core the full sync had just started (pid %d, now %d)", pid, got)
	}
}

func realXrayUsers(n int, inbounds []string) []*common.User {
	users := make([]*common.User, 0, n)
	for i := range n {
		users = append(users, &common.User{
			Email:    strconv.Itoa(i + 1),
			Inbounds: inbounds,
			Proxies: &common.Proxy{
				Vmess:  &common.Vmess{Id: uuid.NewString()},
				Vless:  &common.Vless{Id: uuid.NewString()},
				Trojan: &common.Trojan{Password: "trojan-" + strconv.Itoa(i)},
			},
		})
	}
	return users
}

func realXrayConfig(t *testing.T, tags []string) *Config {
	t.Helper()

	inbounds := make([]map[string]any, 0, len(tags))
	for _, tag := range tags {
		settings := map[string]any{"clients": []any{}}
		if tag == Vless {
			settings["decryption"] = "none"
		}
		inbounds = append(inbounds, map[string]any{
			"tag":      tag,
			"listen":   "127.0.0.1",
			"port":     netutil.FindFreePort(),
			"protocol": tag,
			"settings": settings,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "DIRECT"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func realXrayAssets() string {
	if path := os.Getenv("XRAY_ASSETS_PATH"); path != "" {
		return path
	}
	return assetsPath
}

func startRealXray(t *testing.T, ctx context.Context, tags []string, users []*common.User) *Xray {
	t.Helper()

	nodeCfg := &config.Config{
		XrayExecutablePath:  resolveXrayBinary(t),
		XrayAssetsPath:      realXrayAssets(),
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	}
	x, err := New(ctx, realXrayConfig(t, tags), users, netutil.FindFreePort(), netutil.FindFreePort(), nodeCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Shutdown)
	return x
}

func disabled(users []*common.User) []*common.User {
	out := make([]*common.User, 0, len(users))
	for _, user := range users {
		out = append(out, &common.User{Email: user.Email, Proxies: user.Proxies})
	}
	return out
}

func liveUserInboundPairs(t *testing.T, ctx context.Context, x *Xray, users []*common.User, tags []string) int {
	t.Helper()

	waitUntil(t, "the xray API answers", func() bool {
		probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
		defer probeCancel()
		_, err := x.GetSysStats(probeCtx)
		return err == nil
	})
	live := 0
	for _, user := range users {
		for _, tag := range tags {
			if x.handler.RemoveInboundUser(ctx, tag, user.GetEmail()) == nil {
				live++
			}
		}
	}
	return live
}

func TestRestartHoldsUserSyncsUntilTheXrayAPIIsUp(t *testing.T) {
	tags := []string{Vless, Vmess, Trojan}
	users := realXrayUsers(1000, tags)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	x := startRealXray(t, ctx, tags, users)

	pid := corePID(x)
	restartErr := make(chan error, 1)
	go func() { restartErr <- x.Restart() }()
	waitUntil(t, "the restart spawns a new xray", func() bool {
		current := corePID(x)
		return current != 0 && current != pid
	})

	removed := disabled(users[:51])
	updateErr := x.UpdateUsers(ctx, removed[:50])
	syncErr := x.SyncUser(ctx, removed[50])
	select {
	case err := <-restartErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the restart never returned")
	}

	if live := liveUserInboundPairs(t, ctx, x, removed, tags); live > 0 {
		t.Fatalf("%d of %d removed user-inbound pairs are still accepted by xray (UpdateUsers: %v, SyncUser: %v)", live, len(removed)*len(tags), updateErr, syncErr)
	}
	if updateErr != nil || syncErr != nil {
		t.Fatalf("the removals sent while xray booted failed (UpdateUsers: %v, SyncUser: %v)", updateErr, syncErr)
	}
}

const restartRemovalRounds = 4

func TestHealthRestartsRacingRemovalsAndReadersLeaveNoRemovedUserLive(t *testing.T) {
	tags := []string{Vless, Vmess, Trojan}
	users := realXrayUsers(400, tags)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	x := startRealXray(t, ctx, tags, users)

	readersDone := make(chan struct{})
	var readers sync.WaitGroup
	for range 2 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-readersDone:
					return
				default:
				}
				_, _ = x.activeConfig().ToBytes()
				probeCtx, probeCancel := context.WithTimeout(ctx, 200*time.Millisecond)
				_, _ = x.GetSysStats(probeCtx)
				probeCancel()
				_ = x.Started()
			}
		}()
	}
	defer func() {
		close(readersDone)
		readers.Wait()
	}()

	var removed []*common.User
	for round := range restartRemovalRounds {
		pid := corePID(x)
		restartErr := make(chan error, 1)
		go func() { restartErr <- x.Restart() }()
		waitUntil(t, "the restart spawns a new xray", func() bool {
			current := corePID(x)
			return current != 0 && current != pid
		})

		batch := disabled(users[round*20 : round*20+20])
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = x.UpdateUsers(ctx, batch[:19])
		}()
		go func() {
			defer wg.Done()
			_ = x.SyncUser(ctx, batch[19])
		}()
		wg.Wait()
		removed = append(removed, batch...)

		select {
		case err := <-restartErr:
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		case <-time.After(time.Minute):
			t.Fatalf("round %d: the restart never returned", round)
		}
	}

	if live := liveUserInboundPairs(t, ctx, x, removed, tags); live > 0 {
		t.Fatalf("%d of %d removed user-inbound pairs are still accepted by xray after %d health restarts raced their removals", live, len(removed)*len(tags), restartRemovalRounds)
	}
}
