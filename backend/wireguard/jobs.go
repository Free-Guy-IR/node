package wireguard

import (
	"context"
	"log"
	"time"

	"github.com/pasarguard/node/pkg/stats"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const statsDeviceErrorLogInterval = time.Minute

// initStatsTickers initializes stats update tickers
func (wg *WireGuard) initStatsTickers(ctx context.Context) {
	wg.updateInterval = time.Duration(wg.cfg.StatsUpdateIntervalSeconds) * time.Second
	cleanupInterval := time.Duration(wg.cfg.StatsCleanupIntervalSeconds) * time.Second

	wg.updateTicker = time.NewTicker(wg.updateInterval)
	wg.cleanupTicker = time.NewTicker(cleanupInterval)

	go wg.runStatsUpdateLoop(ctx)
}

// runStatsUpdateLoop runs the stats update loop
func (wg *WireGuard) runStatsUpdateLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-wg.updateTicker.C:
			wg.updateConnectedPeers(ctx)
		case <-wg.cleanupTicker.C:
			wg.cleanupOfflineUsers(ctx)
		}
	}
}

// updateConnectedPeers updates stats for connected users only
func (wg *WireGuard) updateConnectedPeers(ctx context.Context) {
	wg.syncMu.Lock()
	defer wg.syncMu.Unlock()

	if ctx.Err() != nil {
		return
	}

	wg.mu.RLock()
	mgr := wg.manager
	cfg := wg.config
	wg.mu.RUnlock()

	if mgr == nil || cfg == nil {
		return
	}

	if err := wg.samplePeers(mgr); err != nil {
		wg.logStatsDeviceReadError(err)
	}
}

func (wg *WireGuard) samplePeers(mgr *Manager) error {
	device, err := mgr.GetDevice()
	if err != nil {
		return err
	}

	activeHandshakeCutoff := time.Now().Add(-onlineActivityThreshold)

	emailByKey := wg.peerStore.GetEmailMap()
	samples := make([]stats.Sample, 0, len(device.Peers))

	for _, peer := range device.Peers {
		peerKey := peer.PublicKey.String()
		email, ok := emailByKey[peerKey]
		if !ok {
			continue // unknown peer, skip
		}

		endpointIP := ""
		if peer.Endpoint != nil {
			endpointIP = peer.Endpoint.IP.String()
		}

		samples = append(samples, stats.Sample{
			PublicKey:  peerKey,
			Email:      email,
			Rx:         peer.ReceiveBytes,
			Tx:         peer.TransmitBytes,
			EndpointIP: endpointIP,
			Quiet:      peer.LastHandshakeTime.IsZero() || peer.LastHandshakeTime.Before(activeHandshakeCutoff),
		})
	}

	wg.statsTracker.UpdateStatsBatch(samples)
	return nil
}

func (wg *WireGuard) flushPeerStats(mgr *Manager) {
	if err := wg.samplePeers(mgr); err != nil {
		wg.logStatsDeviceReadError(err)
	}
}

func peerConfigKeys(peerConfigs []wgtypes.PeerConfig) []string {
	keys := make([]string, 0, len(peerConfigs))
	for _, peerConfig := range peerConfigs {
		keys = append(keys, peerConfig.PublicKey.String())
	}
	return keys
}

func (wg *WireGuard) logStatsDeviceReadError(err error) {
	now := time.Now()

	wg.mu.Lock()
	if !wg.lastStatsErrAt.IsZero() && now.Sub(wg.lastStatsErrAt) < statsDeviceErrorLogInterval {
		wg.mu.Unlock()
		return
	}
	wg.lastStatsErrAt = now
	wg.mu.Unlock()

	log.Printf("wireguard stats update skipped: failed to read device: %v", err)
	wg.emitWarningLogf("wireguard stats update skipped: failed to read device: %v", err)
}

// cleanupOfflineUsers removes deleted stats entries once their traffic has been reported.
func (wg *WireGuard) cleanupOfflineUsers(ctx context.Context) {
	wg.syncMu.Lock()
	defer wg.syncMu.Unlock()

	select {
	case <-ctx.Done():
		return
	default:
		wg.statsTracker.CleanupDeletedEntries()
	}
}
