package mtproto

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/9seconds/mtg/v2/antireplay"
	"github.com/9seconds/mtg/v2/ipblocklist"
	"github.com/9seconds/mtg/v2/ipblocklist/files"
	"github.com/9seconds/mtg/v2/mtglib"
	"github.com/9seconds/mtg/v2/network"
	"github.com/yl2chen/cidranger"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/stats"
)

const version = "mtg-fork (Free-Guy-IR)"

// proxyInstance is one running admin-configured listener: an mtglib.Proxy
// plus the listener it Serve()s on and the accumulator its EventStream
// writes per-user traffic into.
type proxyInstance struct {
	tag         string
	domain      string
	proxy       *mtglib.Proxy
	listener    net.Listener
	accumulator *eventAccumulator
}

// Backend is the MTProto-backed implementation of backend.Backend. See the
// package doc comment (config.go) for why this shares one mtglib.Proxy per
// instance across all users, instead of spawning per-instance or per-user
// OS processes like every other backend in this repo.
type Backend struct {
	instances map[string]*proxyInstance
	order     []string

	startTime time.Time
	logsChan  chan string

	statsTracker *stats.Tracker

	mu          sync.RWMutex
	secretsByID map[string]secretEntry

	cancelFunc context.CancelFunc
}

func logBufferSizeOrDefault(cfg *config.Config) int {
	if cfg != nil && cfg.LogBufferSize > 0 {
		return cfg.LogBufferSize
	}
	return 1
}

func statsUpdateInterval(cfg *config.Config) time.Duration {
	seconds := 10
	if cfg != nil && cfg.StatsUpdateIntervalSeconds > 0 {
		seconds = cfg.StatsUpdateIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

func buildNetwork() (mtglib.Network, error) {
	dialer, err := network.NewDefaultDialer(0, 0)
	if err != nil {
		return nil, fmt.Errorf("mtproto: cannot build dialer: %w", err)
	}

	ntw, err := network.NewNetwork(dialer, "pasarguard-mtproto", "1.1.1.1", 0)
	if err != nil {
		return nil, fmt.Errorf("mtproto: cannot build network: %w", err)
	}

	return ntw, nil
}

// buildAllowAllList builds an mtglib.IPBlocklist used as ProxyOpts.IPAllowlist
// that accepts every IPv4/IPv6 address - mtg treats a non-empty allowlist as
// the sole gate on who may connect at all (see mtglib/proxy.go's Serve),
// and this backend has no per-instance IP-allowlisting feature (yet), so
// every instance is open to any client, exactly like every other backend's
// default (Xray/sing-box/OpenVPN/WireGuard listeners are not IP-gated
// either). Mirrors the exact construction upstream mtg's own CLI uses when
// its allowlist feature is disabled (internal/cli/run_proxy.go's
// makeIPAllowlist).
func buildAllowAllList(logger mtglib.Logger) (mtglib.IPBlocklist, error) {
	allowlist, err := ipblocklist.NewFireholFromFiles(
		logger,
		1,
		[]files.File{
			files.NewMem([]*net.IPNet{
				cidranger.AllIPv4,
				cidranger.AllIPv6,
			}),
		},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("mtproto: cannot build allowlist: %w", err)
	}

	go allowlist.Run(time.Hour)

	return allowlist, nil
}

// New builds and starts one mtglib.Proxy per configured instance, seeded
// with the initial user set before any listener starts accepting
// connections (so no user has to reconnect after a fresh boot).
func New(_ context.Context, mtCfg *Config, users []*common.User, nodeCfg *config.Config) (*Backend, error) {
	if mtCfg == nil {
		return nil, errors.New("mtproto config must not be nil")
	}
	if len(mtCfg.Instances) == 0 {
		return nil, errors.New("mtproto config: at least one instance is required")
	}

	bCtx, bCancel := context.WithCancel(context.Background())

	b := &Backend{
		instances:    make(map[string]*proxyInstance, len(mtCfg.Instances)),
		startTime:    time.Now(),
		logsChan:     make(chan string, logBufferSizeOrDefault(nodeCfg)),
		statsTracker: stats.New(),
		cancelFunc:   bCancel,
	}

	b.applyInitialUsers(users)

	ntw, err := buildNetwork()
	if err != nil {
		b.cancelFunc()
		return nil, err
	}

	order := make([]string, 0, len(mtCfg.Instances))

	for _, inst := range mtCfg.Instances {
		order = append(order, inst.Tag)

		instLogger := newMtgLogger("mtproto."+inst.Tag, b.recordLog)

		allowlist, err := buildAllowAllList(instLogger.Named("allowlist"))
		if err != nil {
			b.Shutdown()
			return nil, err
		}

		accumulator := newEventAccumulator()

		opts := mtglib.ProxyOpts{
			Logger:             instLogger,
			Network:            ntw,
			AntiReplayCache:    antireplay.NewStableBloomFilter(antireplay.DefaultStableBloomFilterMaxSize, antireplay.DefaultStableBloomFilterErrorRate),
			IPBlocklist:        ipblocklist.NewNoop(),
			IPAllowlist:        allowlist,
			EventStream:        newMtprotoEventStream(accumulator, b.emailForUsername),
			Secrets:            b.secretsForInstance(inst.FakeTLSDomain),
			DomainFrontingHost: inst.FakeTLSDomain,
		}

		proxy, err := mtglib.NewProxy(opts)
		if err != nil {
			b.Shutdown()
			return nil, fmt.Errorf("mtproto: failed to create proxy for instance %q: %w", inst.Tag, err)
		}

		listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: inst.Port})
		if err != nil {
			b.Shutdown()
			return nil, fmt.Errorf("mtproto: failed to listen on instance %q port %d: %w", inst.Tag, inst.Port, err)
		}

		b.instances[inst.Tag] = &proxyInstance{
			tag:         inst.Tag,
			domain:      inst.FakeTLSDomain,
			proxy:       proxy,
			listener:    listener,
			accumulator: accumulator,
		}

		go proxy.Serve(listener) //nolint: errcheck

		b.recordLog(fmt.Sprintf("mtproto instance %q started on port %d (domain=%s)", inst.Tag, inst.Port, inst.FakeTLSDomain))
	}

	b.order = order

	go b.sampleStatsPeriodically(bCtx, statsUpdateInterval(nodeCfg))

	b.recordLog(fmt.Sprintf("mtproto started, %d instance(s)", len(b.instances)))

	return b, nil
}

func (b *Backend) Started() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.instances) == 0 {
		return false
	}
	for _, inst := range b.instances {
		if inst.listener == nil {
			return false
		}
	}
	return true
}

func (b *Backend) Version() string {
	return version
}

func (b *Backend) Logs() <-chan string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.logsChan
}

// Restart re-listens every instance's Proxy on the same port. mtglib.Proxy
// itself has no restart primitive (Shutdown is terminal) - so this closes
// and re-Serve()s each listener against the SAME already-running Proxy
// object, which keeps all currently-authorized secrets intact (no need to
// rebuild ProxyOpts or replay SyncUsers).
func (b *Backend) Restart() error {
	b.mu.RLock()
	instances := make([]*proxyInstance, 0, len(b.instances))
	for _, inst := range b.instances {
		instances = append(instances, inst)
	}
	b.mu.RUnlock()

	var errs []error
	for _, inst := range instances {
		if inst.listener != nil {
			inst.listener.Close() //nolint: errcheck
		}

		port := inst.listener.Addr().(*net.TCPAddr).Port //nolint: forcetypeassert

		listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
		if err != nil {
			errs = append(errs, fmt.Errorf("instance %q: %w", inst.tag, err))
			continue
		}

		inst.listener = listener
		go inst.proxy.Serve(listener) //nolint: errcheck
	}

	return errors.Join(errs...)
}

func (b *Backend) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cancelFunc != nil {
		b.cancelFunc()
		b.cancelFunc = nil
	}

	for _, inst := range b.instances {
		if inst.listener != nil {
			inst.listener.Close() //nolint: errcheck
		}
		if inst.proxy != nil {
			inst.proxy.Shutdown()
		}
	}

	b.recordLog("mtproto shutdown complete")
}

func (b *Backend) sampleStatsPeriodically(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshUserStats()
		}
	}
}

