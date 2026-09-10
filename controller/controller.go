package controller

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/l2tp"
	"github.com/pasarguard/node/backend/mtproto"
	"github.com/pasarguard/node/backend/openvpn"
	"github.com/pasarguard/node/backend/singbox"
	"github.com/pasarguard/node/backend/wireguard"
	"github.com/pasarguard/node/backend/xray"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
	"github.com/pasarguard/node/pkg/sysstats"
)

const NodeVersion = "0.6.13"

var supportedBackends = []string{"xray", "wireguard", "sing_box", "open_vpn", "mtproto", "l2tp"}

type Service interface {
	Disconnect()
}

type Controller struct {
	backend     backend.Backend
	primaryType common.BackendType
	extras      []extraBackend
	cfg         *config.Config
	apiPort     int
	metricPort  int
	clientIP    string
	lastRequest time.Time
	stats       *common.SystemStatsResponse
	cancelFunc  context.CancelFunc
	mu          sync.RWMutex
	controlMu   sync.Mutex

	ownRxTotal   int64
	ownTxTotal   int64
	ownRxSpeed   uint64
	ownTxSpeed   uint64
	ownSampledAt time.Time
	ownMeasured  bool
}

func New(cfg *config.Config) *Controller {
	_, cancel := context.WithCancel(context.Background())
	return &Controller{
		cfg:        cfg,
		apiPort:    netutil.FindFreePort(),
		metricPort: netutil.FindFreePort(),
		cancelFunc: cancel,
	}
}

func (c *Controller) ApiKey() uuid.UUID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.ApiKey
}

func (c *Controller) Connect(ip string, keepAlive uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastRequest = time.Now()
	c.clientIP = ip

	// Cancel the previous connection's collectors before starting new ones,
	// otherwise each reconnect leaks a recordSystemStats/keepAliveTracker
	// goroutine bound to the old context (upstream fix, kept with our client-IP tracking).
	if c.cancelFunc != nil {
		c.cancelFunc()
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.cancelFunc = cancel
	go c.recordSystemStats(ctx)
	if keepAlive > 0 {
		go c.keepAliveTracker(ctx, time.Duration(keepAlive)*time.Second)
	}
}

func (c *Controller) Disconnect() {
	c.cancelFunc()

	c.shutdownExtras()

	c.mu.Lock()
	backend := c.backend
	c.mu.Unlock()

	// Shutdown backend outside of lock to avoid deadlock
	// Shutdown() will wait for process termination to complete
	if backend != nil {
		backend.Shutdown()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.backend = nil
	c.primaryType = common.BackendType_XRAY
	c.apiPort = netutil.FindFreePort()
	c.metricPort = netutil.FindFreePort()
	c.clientIP = ""
}

func (c *Controller) Ip() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientIP
}

func (c *Controller) IsCurrentClient(ip string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientIP == "" || c.clientIP == ip
}

func (c *Controller) LockControl() {
	c.controlMu.Lock()
}

func (c *Controller) UnlockControl() {
	c.controlMu.Unlock()
}

func (c *Controller) NewRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastRequest = time.Now()
}

func (c *Controller) buildBackend(ctx context.Context, b *common.Backend, apiPort, metricPort int) (backend.Backend, error) {
	switch b.GetType() {
	case common.BackendType_XRAY:
		config, err := xray.NewConfig(b.GetConfig(), b.GetExcludeInbounds())
		if err != nil {
			return nil, err
		}
		return xray.New(ctx, config, b.GetUsers(), apiPort, metricPort, c.cfg)

	case common.BackendType_WIREGUARD:
		config, err := wireguard.NewConfig(b.GetConfig())
		if err != nil {
			return nil, err
		}
		return wireguard.New(c.cfg, config, b.GetUsers())

	case common.BackendType_SING_BOX:
		config, err := singbox.NewConfig(b.GetConfig())
		if err != nil {
			return nil, err
		}
		return singbox.New(ctx, config, b.GetUsers(), apiPort, c.cfg)

	case common.BackendType_OPEN_VPN:
		config, err := openvpn.NewConfig(b.GetConfig())
		if err != nil {
			return nil, err
		}
		return openvpn.New(ctx, config, b.GetUsers(), c.cfg)

	case common.BackendType_MTPROTO:
		config, err := mtproto.NewConfig(b.GetConfig())
		if err != nil {
			return nil, err
		}
		return mtproto.New(ctx, config, b.GetUsers(), c.cfg)

	case common.BackendType_L2TP:
		if err := l2tp.CheckDeps(); err != nil {
			return nil, err
		}
		config, err := l2tp.NewConfig(b.GetConfig())
		if err != nil {
			return nil, err
		}
		return l2tp.New(ctx, config, b.GetUsers(), c.cfg)

	default:
		return nil, errors.New("invalid backend type")
	}
}

func (c *Controller) StartBackend(ctx context.Context, b *common.Backend) error {
	c.shutdownExtras()

	c.mu.Lock()
	defer c.mu.Unlock()

	newBackend, err := c.buildBackend(ctx, b, c.apiPort, c.metricPort)
	if err != nil {
		return err
	}

	c.backend = newBackend
	c.primaryType = b.GetType()
	return nil
}

func (c *Controller) Backend() backend.Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.backend
}

func (c *Controller) keepAliveTracker(ctx context.Context, keepAlive time.Duration) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.RLock()
			lastRequest := c.lastRequest
			c.mu.RUnlock()
			if time.Since(lastRequest) >= keepAlive {
				log.Println("disconnect automatically due to keep alive timeout")
				c.Disconnect()
			}
		}
	}
}

func (c *Controller) recordSystemStats(ctx context.Context) {
	interval := 1500 * time.Millisecond

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	collect := func() {
		stats, err := sysstats.GetSystemStats(ctx)
		if err != nil {
			log.Printf("Failed to get system stats: %v", err)
			return
		}

		if rx, tx, ok := c.sampleOwnThroughput(ctx); ok {
			stats.IncomingBandwidthSpeed = rx
			stats.OutgoingBandwidthSpeed = tx
		}

		c.mu.Lock()
		c.stats = stats
		c.mu.Unlock()
	}

	collect()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

func (c *Controller) sampleOwnThroughput(ctx context.Context) (uint64, uint64, bool) {
	c.mu.RLock()
	b := c.backend
	lastRequest := c.lastRequest
	lastSample := c.ownSampledAt
	rxSpeed, txSpeed, measured := c.ownRxSpeed, c.ownTxSpeed, c.ownMeasured
	c.mu.RUnlock()
	if b == nil || !b.Started() {
		c.mu.Lock()
		c.ownMeasured = false
		c.mu.Unlock()
		return 0, 0, false
	}

	if time.Since(lastRequest) > 30*time.Second {
		return rxSpeed, txSpeed, measured
	}
	if measured && time.Since(lastSample) < 3*time.Second {
		return rxSpeed, txSpeed, measured
	}

	statsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := b.GetStats(statsCtx, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: false})
	if err != nil || resp == nil {
		c.mu.RLock()
		rx, tx, measured := c.ownRxSpeed, c.ownTxSpeed, c.ownMeasured
		c.mu.RUnlock()
		return rx, tx, measured
	}

	var rxTotal, txTotal int64
	for _, stat := range resp.GetStats() {
		if stat == nil {
			continue
		}
		switch stat.GetType() {
		case "downlink":
			rxTotal += stat.GetValue()
		case "uplink":
			txTotal += stat.GetValue()
		}
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	prevRx, prevTx, prevAt, had := c.ownRxTotal, c.ownTxTotal, c.ownSampledAt, c.ownMeasured
	c.ownRxTotal, c.ownTxTotal, c.ownSampledAt = rxTotal, txTotal, now

	elapsed := now.Sub(prevAt).Seconds()
	if !had || elapsed <= 0 || rxTotal < prevRx || txTotal < prevTx {
		c.ownMeasured = true
		return c.ownRxSpeed, c.ownTxSpeed, true
	}

	c.ownRxSpeed = uint64(float64(rxTotal-prevRx) / elapsed)
	c.ownTxSpeed = uint64(float64(txTotal-prevTx) / elapsed)
	c.ownMeasured = true
	return c.ownRxSpeed, c.ownTxSpeed, true
}

func (c *Controller) SystemStats(ctx context.Context) *common.SystemStatsResponse {
	c.mu.RLock()
	statsSnapshot := c.stats
	backendSnapshot := c.backend
	c.mu.RUnlock()

	response := &common.SystemStatsResponse{}
	if statsSnapshot != nil {
		response = &common.SystemStatsResponse{
			MemTotal:               statsSnapshot.GetMemTotal(),
			MemUsed:                statsSnapshot.GetMemUsed(),
			CpuCores:               statsSnapshot.GetCpuCores(),
			CpuUsage:               statsSnapshot.GetCpuUsage(),
			IncomingBandwidthSpeed: statsSnapshot.GetIncomingBandwidthSpeed(),
			OutgoingBandwidthSpeed: statsSnapshot.GetOutgoingBandwidthSpeed(),
			Uptime:                 statsSnapshot.GetUptime(),
		}
	}

	if backendSnapshot == nil {
		return response
	}

	// Backend uptime is owned by each backend implementation; controller only forwards it here.
	backendStats, err := backendSnapshot.GetSysStats(ctx)
	if err != nil {
		log.Printf("Failed to get backend uptime for system stats: %v", err)
		return response
	}

	response.Uptime = uint64(backendStats.GetUptime())
	return response
}

func (c *Controller) BaseInfoResponse() *common.BaseInfoResponse {
	c.mu.Lock()
	defer c.mu.Unlock()

	response := &common.BaseInfoResponse{
		Started:           false,
		CoreVersion:       "",
		NodeVersion:       NodeVersion,
		SupportedBackends: append([]string(nil), supportedBackends...),
	}

	if c.backend != nil {
		response.Started = c.backend.Started()
		response.CoreVersion = c.backend.Version()
	}

	return response
}

func (c *Controller) OutboundsLatency(ctx context.Context, request *common.LatencyRequest) (*common.LatencyResponse, error) {
	c.mu.RLock()
	backendSnapshot := c.backend
	c.mu.RUnlock()

	if backendSnapshot == nil {
		return &common.LatencyResponse{Latencies: []*common.Latency{}}, nil
	}

	return backendSnapshot.GetOutboundsLatency(ctx, request)
}
