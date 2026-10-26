package xray

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const fullSyncReadyTimeout = 3 * time.Minute

type Xray struct {
	config      *Config
	cfg         *config.Config
	core        *Core
	handler     *api.XrayHandler
	metricPort  int
	degradation backend.StartupDegradation
	cancelFunc  context.CancelFunc
	mu          sync.RWMutex
	syncMu      sync.Mutex
	syncEntered func(context.Context)
	holderMu    sync.Mutex
	syncHolder  *userSync
	stuckGrace  time.Duration
	shutDown    bool
}

var errShutDown = errors.New("xray has been shut down")

const (
	defaultStuckGrace   = 60 * time.Second
	stuckSyncPoll       = 250 * time.Millisecond
	restartReadyTimeout = 30 * time.Second
	healthProbeTimeout  = 3 * time.Second
)

func New(ctx context.Context, xrayConfig *Config, users []*common.User, apiPort, metricPort int, cfg *config.Config) (*Xray, error) {
	executableAbsolutePath, err := filepath.Abs(cfg.XrayExecutablePath)
	if err != nil {
		return nil, err
	}

	assetsAbsolutePath, err := filepath.Abs(cfg.XrayAssetsPath)
	if err != nil {
		return nil, err
	}

	configAbsolutePath, err := filepath.Abs(cfg.GeneratedConfigPath)
	if err != nil {
		return nil, err
	}

	xCtx, xCancel := context.WithCancel(context.Background())

	xray := &Xray{
		cancelFunc: xCancel,
		cfg:        cfg,
		metricPort: metricPort,
	}

	start := time.Now()

	if err = xrayConfig.ApplyAPI(apiPort, metricPort); err != nil {
		return nil, err
	}

	if len(users) > 0 {
		log.Printf("syncing %d users on startup", len(users))
		xrayConfig.syncUsers(users)
		// Verify users were synced by counting clients in all inbounds
		totalClients := 0
		for _, inbound := range xrayConfig.InboundConfigs {
			if !inbound.exclude && inbound.clients != nil {
				totalClients += len(inbound.clients)
			}
		}
		log.Printf("synced %d users on startup, total clients in config: %d", len(users), totalClients)
	} else {
		log.Println("no users provided on startup")
	}

	xray.config = xrayConfig

	log.Println("config generated in", time.Since(start).Seconds(), "second.")

	core, err := NewXRayCore(executableAbsolutePath, assetsAbsolutePath, configAbsolutePath, cfg.LogBufferSize, cfg.StartupLogTailSize)
	if err != nil {
		return nil, err
	}

	xray.core = core

	handler, err := api.NewXrayAPI(apiPort)
	if err != nil {
		xCancel()
		return nil, err
	}
	xray.handler = handler

	if err = xray.startCoreWithFilterFallback(ctx, xrayConfig, xray.startCoreAndWait, core.Stop); err != nil {
		xray.Shutdown()
		return nil, err
	}

	// Wait a bit for Xray to fully initialize before starting health checks
	// This prevents false positives during startup
	go xray.checkXrayHealth(xCtx)

	log.Println("xray started, Version:", xray.Version())

	return xray, nil
}

func (x *Xray) Logs() <-chan string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.core.Logs()
}

func (x *Xray) Version() string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.core.Version()
}

func (x *Xray) Started() bool {
	return x.core.Started()
}

func (x *Xray) Restart() error {
	unlock, _ := x.lockSyncForRestart()
	defer unlock()
	return x.restartLocked(context.Background())
}

func (x *Xray) RestartUnlessReplaced(ctx context.Context, observed *coreState) (bool, error) {
	unlock, cancelledSync := x.lockSyncForRestart()
	defer unlock()
	if !cancelledSync && x.coreServesSince(ctx, observed) {
		return false, nil
	}
	return true, x.restartLocked(ctx)
}

func (x *Xray) coreServesSince(ctx context.Context, observed *coreState) bool {
	current := x.core.state.Load()
	if !startedFromState(current) {
		return false
	}
	return x.probe(ctx) == nil
}

func (x *Xray) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	_, err := x.GetSysStats(ctx)
	return err
}

func (x *Xray) restartLocked(ctx context.Context) error {
	if err := x.restartCoreWithConfig(x.activeConfig()); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, restartReadyTimeout)
	defer cancel()
	if err := x.checkXrayStatus(ctx); err != nil {
		return fmt.Errorf("xray API not ready within %s after restart: %w", restartReadyTimeout, err)
	}
	return nil
}

func (x *Xray) restartCoreWithConfig(config *Config) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.shutDown {
		return errShutDown
	}
	if err := x.core.Restart(config, x.cfg.Debug); err != nil {
		return err
	}
	return nil
}

func (x *Xray) setConfig(config *Config) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.config = config
}

func (x *Xray) activeConfig() *Config {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.config
}

func (x *Xray) Shutdown() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.shutDown = true

	// Cancel context first to stop health checks and other goroutines
	x.cancelFunc()

	// Stop core (this now waits for process termination)
	if x.core != nil {
		x.core.Stop()
	}

	// Close API handler
	if x.handler != nil {
		x.handler.Close()
	}

	// Shutdown is now complete - all resources are cleaned up
}
