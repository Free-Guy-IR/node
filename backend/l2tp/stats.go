package l2tp

import (
	"context"
	"errors"
	"fmt"
	"github.com/pasarguard/node/backend"
	"io/fs"
	"os"
	"runtime"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

var (
	beforeApplySamples = func() {}
	interfaceSampled   = func() {}
	removeFinalRecord  = os.Remove
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
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != lifecycleRunning {
		return
	}
	o.pollLocked()
}

func (o *L2TP) pollLocked() {
	samples := o.collectLocked()
	beforeApplySamples()
	o.statsTracker.UpdateStatsBatch(samples)
}

func (o *L2TP) collectLocked() []stats.Sample {
	tag := o.config.InboundTag
	listed := scanSessions(tag)
	sessions := listed.sessions
	scan := scanFinalRecords(tag)
	finals := scan.records
	if o.counted.since > 0 && !o.counted.bootKnown {
		o.counted.noteBoot(currentBootID())
	}
	if o.counted.awaitsSeen() {
		o.counted.noteSeen(scan)
	}
	resolved := o.counted.takeStartedBeforeFallback(sessions)

	perUser := make(map[string][]l2tpSession)
	for _, s := range sessions {
		perUser[s.user] = append(perUser[s.user], s)
	}

	growthRx := make(map[string]int64)
	growthTx := make(map[string]int64)
	ended := make(map[string]struct{}, len(finals))

	keep := make(map[sessionIdentity]struct{}, len(sessions))
	o.holdUnlistedLocked(listed, scan, keep)
	o.sortOutFinalRecordsLocked(scan, keep)
	removeFailed := false
	for _, rec := range finals {
		ended[rec.ifname] = struct{}{}
		if o.counted.undecided(rec) {
			resolved = false
			continue
		}
		if err := removeFinalRecord(rec.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if !removeFailed && !o.finalRemoveFailing {
				o.emitLogf("Error", "l2tp: cannot remove final record %s, it is billed once it can be removed: %v", rec.path, err)
			}
			removeFailed = true
			o.counted.hold(rec, keep)
			continue
		}
		if rec.hasPid {
			o.emitLogf("Error", "l2tp: final record %s of pppd %d carries no session start, matched by interface and pid", rec.path, rec.identity.pid)
		}
		base := o.counted.consume(rec)
		growthRx[rec.user] += max(rec.rx-base.rx, 0)
		growthTx[rec.user] += max(rec.tx-base.tx, 0)
		if _, live := perUser[rec.user]; !live {
			perUser[rec.user] = nil
		}
	}
	o.finalRemoveFailing = removeFailed

	for _, s := range sessions {
		id := s.identity()
		keep[id] = struct{}{}
		if _, gone := ended[s.ifname]; gone {
			continue
		}
		rx, tx, ok := sessionBytes(s)
		if !ok {
			continue
		}
		base, known := o.counted.lookup(id)
		if !known && s.proc == "" {
			base = o.counted.inherit(id, countedBytes{rx: rx, tx: tx})
		}
		growthRx[s.user] += max(rx-base.rx, 0)
		growthTx[s.user] += max(tx-base.tx, 0)
		o.counted.set(id, countedBytes{rx: max(rx, base.rx), tx: max(tx, base.tx)})
	}
	o.counted.settleFallback(resolved && !removeFailed && listed.complete() && scan.listErr == nil && len(scan.unreadable) == 0)
	o.counted.retain(keep)
	o.persistCountedLocked()
	o.sweepPppdRecordsLocked()

	var samples []stats.Sample
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
	return samples
}

func (o *L2TP) forgetUsers(usernames []string) {
	if len(usernames) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, u := range usernames {
		delete(o.cumRx, u)
		delete(o.cumTx, u)
		delete(o.onlineIPs, u)
		o.statsTracker.RemoveStats(u)
	}
	o.statsTracker.CleanupDeletedEntries()
}

func (o *L2TP) running() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.state == lifecycleRunning
}

func (o *L2TP) runningTracker() (bool, *stats.Tracker) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.state == lifecycleRunning, o.statsTracker
}

func (o *L2TP) GetStats(ctx context.Context, request *common.StatRequest) (*common.StatResponse, error) {
	running, tracker := o.runningTracker()
	if !running {
		return nil, errNotStarted
	}
	switch request.GetType() {
	case common.StatType_UserStat:
		return tracker.GetStats(ctx, []string{request.GetName()}, request.GetReset_()), nil
	case common.StatType_UsersStat:
		return tracker.GetUsersStats(ctx, request.GetReset_()), nil
	case common.StatType_Inbound, common.StatType_Inbounds:
		dRx, dTx := o.interfaceDelta(func() *stats.InterfaceCountersTracker { return o.inboundStats }, request.GetReset_())
		return &common.StatResponse{
			Stats: stats.BuildInterfaceStats(o.config.InboundTag, "inbound", dRx, dTx),
		}, nil
	case common.StatType_Outbound, common.StatType_Outbounds:
		dRx, dTx := o.interfaceDelta(func() *stats.InterfaceCountersTracker { return o.interfaceStats }, request.GetReset_())
		return &common.StatResponse{
			Stats: stats.BuildInterfaceStats(o.config.InboundTag, "outbound", dRx, dTx),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", backend.ErrStatTypeNotSupported, request.GetType())
	}
}

func (o *L2TP) interfaceDelta(tracker func() *stats.InterfaceCountersTracker, reset bool) (int64, int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	interfaceSampled()
	return tracker().Delta(o.totalRx, o.totalTx, reset)
}

func (o *L2TP) GetUserOnlineStats(_ context.Context, email string) (*common.OnlineStatResponse, error) {
	running, tracker := o.runningTracker()
	if !running {
		return nil, errNotStarted
	}
	o.mu.RLock()
	_, connected := o.onlineIPs[email]
	o.mu.RUnlock()
	value := int64(0)
	if connected || tracker.AnyActiveSince([]string{email}, time.Now().Add(-onlineActivityThreshold)) {
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
