// Package singbox implements backend.Backend on top of a sing-box subprocess,
// scoped to the Hysteria2 protocol for v1 (see config.go's doc comment).
//
// It follows the shape of backend/xray as closely as makes sense given two
// structural differences from xray-core:
//   - sing-box has no stdin config mode; it must be started with "-c <path>", so
//     the generated config is always written to a file (not just in debug mode
//     like xray).
//   - sing-box's experimental.v2ray_api only exposes a StatsService (no
//     HandlerService equivalent for hot user add/remove), so every user sync
//     operation goes through a full config rewrite + process restart. See
//     user.go's doc comments for the concrete implications.
package singbox

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/pasarguard/node/backend/singbox/api"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

// SingBox is the sing-box-backed implementation of backend.Backend.
type SingBox struct {
	config     *Config
	cfg        *config.Config
	core       *Core
	client     *api.Client
	apiPort    int
	cancelFunc context.CancelFunc
	mu         sync.RWMutex
	syncMu     sync.Mutex

	batchMu sync.Mutex
	batch   *singBoxPendingBatch
}

// New builds and starts a sing-box process from sbConfig, syncs the initial
// user set into it before the first start, dials its v2ray_api stats port, and
// waits for it to become healthy. Mirrors xray.New's shape/signature.
func New(ctx context.Context, sbConfig *Config, users []*common.User, apiPort int, cfg *config.Config) (*SingBox, error) {
	executableAbsolutePath, err := filepath.Abs(cfg.SingBoxExecutablePath)
	if err != nil {
		return nil, err
	}

	configAbsolutePath, err := filepath.Abs(cfg.GeneratedConfigPath)
	if err != nil {
		return nil, err
	}

	sCtx, sCancel := context.WithCancel(context.Background())

	sb := &SingBox{
		cancelFunc: sCancel,
		cfg:        cfg,
		apiPort:    apiPort,
	}

	start := time.Now()

	if err = sbConfig.ApplyAPI(apiPort); err != nil {
		sCancel()
		return nil, err
	}

	if len(users) > 0 {
		log.Printf("syncing %d users on startup", len(users))
		sbConfig.syncUsers(users)
	} else {
		log.Println("no users provided on startup")
	}

	sb.config = sbConfig

	log.Println("sing-box config generated in", time.Since(start).Seconds(), "second.")

	core, err := NewCore(executableAbsolutePath, configAbsolutePath, cfg.LogBufferSize, cfg.StartupLogTailSize)
	if err != nil {
		sCancel()
		return nil, err
	}

	if err = core.Start(sbConfig); err != nil {
		sCancel()
		return nil, err
	}
	sb.core = core

	client, err := api.NewClient(apiPort)
	if err != nil {
		sb.Shutdown()
		return nil, err
	}
	sb.client = client

	if err = sb.checkStatus(ctx); err != nil {
		sb.Shutdown()
		return nil, err
	}

	// Wait a bit for sing-box to fully initialize before starting health checks,
	// mirroring xray's startup grace period.
	go sb.checkHealth(sCtx)

	log.Println("sing-box started, Version:", sb.Version())

	return sb, nil
}

func (s *SingBox) Logs() <-chan string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.core.Logs()
}

func (s *SingBox) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.core.Version()
}

func (s *SingBox) Started() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.core.Started()
}

func (s *SingBox) Restart() error {
	return s.restartCoreWithConfig(s.config)
}

func (s *SingBox) restartCoreWithConfig(cfg *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.core.Restart(cfg)
}

func (s *SingBox) setConfig(cfg *Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = cfg
}

func (s *SingBox) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel context first to stop health checks and other goroutines.
	s.cancelFunc()

	if s.core != nil {
		s.core.Stop()
	}

	if s.client != nil {
		s.client.Close()
	}
}
