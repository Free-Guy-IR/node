package l2tp

import (
	"context"
	"fmt"
	"github.com/pasarguard/node/backend"
	"os"
	"runtime"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func (o *L2TP) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(o.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.poll()
		}
	}
}

func (o *L2TP) poll() {
	tag := o.config.InboundTag
	sessions := readSessions(tag)
	finals := readFinalRecords(tag)

	perUser := make(map[string][]l2tpSession)
	present := make(map[string]struct{})
	for _, s := range sessions {
		perUser[s.user] = append(perUser[s.user], s)
		present[s.ifname] = struct{}{}
	}

	var samples []stats.Sample

	o.mu.Lock()
	growthRx := make(map[string]int64)
	growthTx := make(map[string]int64)

	for _, rec := range finals {
		last, seen := o.ifSeen[rec.ifname]
		dRx, dTx := rec.rx, rec.tx
		if seen {
			dRx, dTx = rec.rx-last[0], rec.tx-last[1]
		}
		dRx = max(dRx, 0)
		dTx = max(dTx, 0)
		delete(o.ifSeen, rec.ifname)
		growthRx[rec.user] += dRx
		growthTx[rec.user] += dTx
		if _, live := perUser[rec.user]; !live {
			perUser[rec.user] = nil
		}
		_ = os.Remove(rec.path)
	}

	for _, s := range sessions {
		rx, tx := ifaceBytes(s.ifname)
		last := o.ifSeen[s.ifname]
		dRx := max(rx-last[0], 0)
		dTx := max(tx-last[1], 0)
		o.ifSeen[s.ifname] = [2]int64{rx, tx}
		growthRx[s.user] += dRx
		growthTx[s.user] += dTx
	}
	for ifn := range o.ifSeen {
		if _, ok := present[ifn]; !ok {
			delete(o.ifSeen, ifn)
		}
	}

	now := time.Now().Unix()
	onlineIPs := make(map[string]map[string]int64, len(perUser))
	for user, list := range perUser {
		o.cumRx[user] += growthRx[user]
		o.cumTx[user] += growthTx[user]
		o.totalRx += growthRx[user]
		o.totalTx += growthTx[user]
		ips := make(map[string]int64, len(list))
		endpoint := ""
		for _, s := range list {
			ip := s.clientIP
			if ip == "" {
				ip = s.tunnelIP
			}
			if ip == "" {
				continue
			}
			ips[ip] = now
			if endpoint == "" {
				endpoint = ip
			}
		}
		onlineIPs[user] = ips
		samples = append(samples, stats.Sample{
			PublicKey:  user,
			Email:      user,
			Rx:         o.cumRx[user],
			Tx:         o.cumTx[user],
			EndpointIP: endpoint,
		})
	}
	o.onlineIPs = onlineIPs
	o.mu.Unlock()

	if len(samples) > 0 {
		o.statsTracker.UpdateStatsBatch(samples)
	}
}

func (o *L2TP) forgetUsers(usernames []string) {
	if len(usernames) == 0 {
		return
	}
	o.mu.Lock()
	for _, u := range usernames {
		delete(o.cumRx, u)
		delete(o.cumTx, u)
		delete(o.onlineIPs, u)
	}
	o.mu.Unlock()
	for _, u := range usernames {
		o.statsTracker.RemoveStats(u)
	}
	o.statsTracker.CleanupDeletedEntries()
}

func (o *L2TP) running() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.state == lifecycleRunning
}

func (o *L2TP) GetStats(ctx context.Context, request *common.StatRequest) (*common.StatResponse, error) {
	if !o.running() {
		return nil, errNotStarted
	}
	switch request.GetType() {
	case common.StatType_UserStat:
		return o.statsTracker.GetStats(ctx, []string{request.GetName()}, request.GetReset_()), nil
	case common.StatType_UsersStat:
		return o.statsTracker.GetUsersStats(ctx, request.GetReset_()), nil
	case common.StatType_Inbound, common.StatType_Inbounds:
		o.mu.Lock()
		totalRx, totalTx := o.totalRx, o.totalTx
		o.mu.Unlock()
		dRx, dTx := o.inboundStats.Delta(totalRx, totalTx, request.GetReset_())
		return &common.StatResponse{
			Stats: stats.BuildInterfaceStats(o.config.InboundTag, "inbound", dRx, dTx),
		}, nil
	case common.StatType_Outbound, common.StatType_Outbounds:
		o.mu.Lock()
		totalRx, totalTx := o.totalRx, o.totalTx
		o.mu.Unlock()
		dRx, dTx := o.interfaceStats.Delta(totalRx, totalTx, request.GetReset_())
		return &common.StatResponse{
			Stats: stats.BuildInterfaceStats(o.config.InboundTag, "outbound", dRx, dTx),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", backend.ErrStatTypeNotSupported, request.GetType())
	}
}

func (o *L2TP) GetUserOnlineStats(_ context.Context, email string) (*common.OnlineStatResponse, error) {
	if !o.running() {
		return nil, errNotStarted
	}
	o.mu.RLock()
	_, connected := o.onlineIPs[email]
	o.mu.RUnlock()
	value := int64(0)
	if connected || o.statsTracker.AnyActiveSince([]string{email}, time.Now().Add(-onlineActivityThreshold)) {
		value = 1
	}
	return &common.OnlineStatResponse{Name: email, Value: value}, nil
}

func (o *L2TP) GetUserOnlineIpListStats(_ context.Context, email string) (*common.StatsOnlineIpListResponse, error) {
	if !o.running() {
		return nil, errNotStarted
	}
	response := &common.StatsOnlineIpListResponse{Name: email, Ips: make(map[string]int64)}
	o.mu.RLock()
	for ip, ts := range o.onlineIPs[email] {
		response.Ips[ip] = ts
	}
	o.mu.RUnlock()
	return response, nil
}

func (o *L2TP) GetSysStats(_ context.Context) (*common.BackendStatsResponse, error) {
	if !o.running() {
		return nil, errNotStarted
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return &common.BackendStatsResponse{
		NumGoroutine: uint32(runtime.NumGoroutine()),
		NumGc:        m.NumGC,
		Alloc:        m.Alloc,
		TotalAlloc:   m.TotalAlloc,
		Sys:          m.Sys,
		Mallocs:      m.Mallocs,
		Frees:        m.Frees,
		LiveObjects:  m.Mallocs - m.Frees,
		PauseTotalNs: m.PauseTotalNs,
		Uptime:       uint32(time.Since(o.startTime).Seconds()),
	}, nil
}
