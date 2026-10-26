//go:build !windows

package xray

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	statsCommand "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/config"
)

type bootingStatsService struct {
	statsCommand.UnimplementedStatsServiceServer
	healthy atomic.Bool
}

func (s *bootingStatsService) GetSysStats(context.Context, *statsCommand.SysStatsRequest) (*statsCommand.SysStatsResponse, error) {
	if !s.healthy.Load() {
		return nil, status.Error(codes.Unavailable, "xray is booting")
	}
	return &statsCommand.SysStatsResponse{}, nil
}

func newBootingCoreXray(t *testing.T, stats *bootingStatsService) *Xray {
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

	cfg, err := NewConfig(`{"log":{"loglevel":"warning"},"inbounds":[]}`, nil)
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

func bootingCorePID(x *Xray) int {
	x.core.mu.Lock()
	defer x.core.mu.Unlock()
	return x.core.processPID
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSyncUsersOutlivesTheCaller(t *testing.T) {
	stats := &bootingStatsService{}
	x := newBootingCoreXray(t, stats)
	previous := x.config

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncErr := make(chan error, 1)
	go func() { syncErr <- x.SyncUsers(ctx, nil) }()
	waitFor(t, "the full sync starts its candidate core", x.core.Started)
	pid := bootingCorePID(x)

	cancel()
	time.Sleep(1500 * time.Millisecond)
	stats.healthy.Store(true)

	select {
	case err := <-syncErr:
		if err != nil {
			t.Fatalf("SyncUsers failed after its caller went away: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SyncUsers still running 10s after the core answered")
	}
	if got := bootingCorePID(x); got != pid {
		t.Fatalf("xray was restarted again after the caller went away (pid %d, now %d)", pid, got)
	}
	x.mu.RLock()
	kept := x.config == previous
	x.mu.RUnlock()
	if kept {
		t.Fatal("the full sync left xray on the previous config")
	}
}

func TestAFullSyncWhoseCallerLeftStillYieldsToALifecycleAbort(t *testing.T) {
	stats := &bootingStatsService{}
	x := newBootingCoreXray(t, stats)
	previous := x.config

	abort, cancelAbort := context.WithCancel(context.Background())
	defer cancelAbort()
	ctx, cancel := context.WithCancel(backend.WithSyncAbort(context.Background(), abort))
	defer cancel()
	syncErr := make(chan error, 1)
	go func() { syncErr <- x.SyncUsers(ctx, nil) }()
	waitFor(t, "the full sync starts its candidate core", x.core.Started)
	candidate := bootingCorePID(x)

	cancel()
	time.Sleep(1500 * time.Millisecond)
	select {
	case err := <-syncErr:
		t.Fatalf("the full sync ended when only its caller left: %v", err)
	default:
	}

	cancelAbort()
	waitFor(t, "the aborted sync restores the previous core", func() bool {
		pid := bootingCorePID(x)
		return pid != 0 && pid != candidate
	})
	stats.healthy.Store(true)

	select {
	case err := <-syncErr:
		if err == nil {
			t.Fatal("a full sync aborted for a lifecycle change reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a full sync aborted for a lifecycle change still runs 10s later")
	}
	x.mu.RLock()
	restored := x.config == previous
	x.mu.RUnlock()
	if !restored {
		t.Fatal("an aborted full sync did not restore the previous config")
	}
}
