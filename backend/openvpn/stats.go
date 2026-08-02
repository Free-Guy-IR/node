package openvpn

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

// statsKey builds the pkg/stats.Tracker key for a (instance tag, user email)
// pair. Unlike WireGuard's one-key-per-peer model, a user's OpenVPN traffic
// is naturally scoped per instance (a user can in principle be authorized on
// more than one instance/tag at once), so the tag has to be part of the
// tracker key for per-instance ("Inbound") aggregation to be possible.
func statsKey(tag, email string) string {
	return tag + "|" + email
}

func splitStatsKey(key string) (tag string) {
	tag, _, _ = strings.Cut(key, "|")
	return tag
}

// refreshUserStats pulls the current cumulative per-user counters out of
// every instance's ManagementClient and feeds them into the shared
// pkg/stats.Tracker, which turns them into the delta/reset-aware series
// GetStats/GetUsersStats report. Safe to call frequently/concurrently -
// mirrors how WireGuard's stats plumbing (backend/wireguard/jobs.go) polls
// its own counters on a ticker.
func (b *Backend) refreshUserStats() {
	b.mu.RLock()
	instances := make([]*instanceProcess, 0, len(b.instances))
	for _, instance := range b.instances {
		instances = append(instances, instance)
	}
	b.mu.RUnlock()

	var samples []stats.Sample
	for _, instance := range instances {
		mgmt := instance.managementClient()
		if mgmt == nil {
			continue
		}
		for email, s := range mgmt.AllUserStats() {
			samples = append(samples, stats.Sample{
				PublicKey: statsKey(instance.tag, email),
				Email:     email,
				Rx:        int64(s.Downlink),
				Tx:        int64(s.Uplink),
			})
		}
	}

	b.statsTracker.UpdateStatsBatch(samples)
}

func (b *Backend) keysForEmail(email string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	keys := make([]string, 0, len(b.instances))
	for tag := range b.instances {
		keys = append(keys, statsKey(tag, email))
	}
	return keys
}

// rewriteLinkToTag replaces each Stat's Link (which pkg/stats.Tracker sets
// to the tracker key, i.e. "tag|email" - see statsKey) with just the
// instance tag, which is what Link is actually meant to identify (mirrors
// e.g. WireGuard's Link=interface-name / xray's Link=inbound-tag usage).
func rewriteLinkToTag(resp *common.StatResponse) *common.StatResponse {
	for _, s := range resp.GetStats() {
		s.Link = splitStatsKey(s.Link)
	}
	return resp
}

func (b *Backend) userStat(ctx context.Context, email string, reset bool) *common.StatResponse {
	return rewriteLinkToTag(b.statsTracker.GetStats(ctx, b.keysForEmail(email), reset))
}

func (b *Backend) instanceStat(name string, reset bool) (*common.StatResponse, error) {
	b.mu.RLock()
	instance, ok := b.instances[name]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("openvpn: unknown instance %q", name)
	}

	rx, tx := instance.aggregateStats()
	deltaRx, deltaTx := instance.statsTracker.DeltaClamped(rx, tx, reset)
	return &common.StatResponse{Stats: stats.BuildInterfaceStats(name, name, deltaRx, deltaTx)}, nil
}

func (b *Backend) instancesStat(reset bool) *common.StatResponse {
	b.mu.RLock()
	instances := make([]*instanceProcess, 0, len(b.instances))
	for _, instance := range b.instances {
		instances = append(instances, instance)
	}
	b.mu.RUnlock()

	out := &common.StatResponse{Stats: make([]*common.Stat, 0, len(instances)*2)}
	for _, instance := range instances {
		rx, tx := instance.aggregateStats()
		deltaRx, deltaTx := instance.statsTracker.DeltaClamped(rx, tx, reset)
		out.Stats = append(out.Stats, stats.BuildInterfaceStats(instance.tag, instance.tag, deltaRx, deltaTx)...)
	}
	return out
}

// GetStats maps this backend's data onto the shared StatType vocabulary:
// instances play the role of "inbounds" (they are the listeners accepting
// tunneled traffic). OpenVPN has no "outbound" concept of its own (see
// GetOutboundsLatency), but the dashboard's node-level traffic total is
// sourced from an Outbound/Outbounds query regardless of backend type (see
// the panel's app/jobs/record_usages.py get_outbounds_stats) - so
// Outbound/Outbounds here deliberately answers with the same per-instance
// aggregate Inbound/Inbounds already reports, via instance.statsTracker (a
// stats.InterfaceCountersTracker, a distinct tracker instance from
// b.statsTracker above, which is what UserStat/UsersStat reads/resets).
// Reusing it for the node-level total is safe specifically because it
// never touches b.statsTracker's own reset cycle - the two trackers only
// share their underlying data source (mgmt.AllUserStats()), which is
// polled read-only, not consumed/reset at the source.
func (b *Backend) GetStats(ctx context.Context, request *common.StatRequest) (*common.StatResponse, error) {
	b.refreshUserStats()

	switch request.GetType() {
	case common.StatType_UserStat:
		return b.userStat(ctx, request.GetName(), request.GetReset_()), nil
	case common.StatType_UsersStat:
		return rewriteLinkToTag(b.statsTracker.GetUsersStats(ctx, request.GetReset_())), nil
	case common.StatType_Inbound:
		return b.instanceStat(request.GetName(), request.GetReset_())
	case common.StatType_Inbounds:
		return b.instancesStat(request.GetReset_()), nil
	case common.StatType_Outbound:
		return b.instanceStat(request.GetName(), request.GetReset_())
	case common.StatType_Outbounds:
		return b.instancesStat(request.GetReset_()), nil
	default:
		return nil, errors.New("unsupported stat type")
	}
}

// GetSysStats reports the controlling node process's own Go runtime stats.
// OpenVPN's management interface exposes no analogue of xray/sing-box's
// in-process runtime-stats API (there is no "report your own memory/GC
// stats" command in the management protocol) - the same situation WireGuard
// is in (see backend/wireguard/stats.go's GetSysStats), so - like WireGuard -
// this reports the node process's own stats as the best available proxy for
// backend health/resource usage.
func (b *Backend) GetSysStats(ctx context.Context) (*common.BackendStatsResponse, error) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	b.mu.RLock()
	startTime := b.startTime
	b.mu.RUnlock()

	return &common.BackendStatsResponse{
		NumGoroutine: uint32(runtime.NumGoroutine()),
		NumGc:        mem.NumGC,
		Alloc:        mem.Alloc,
		TotalAlloc:   mem.TotalAlloc,
		Sys:          mem.Sys,
		Mallocs:      mem.Mallocs,
		Frees:        mem.Frees,
		LiveObjects:  mem.Mallocs - mem.Frees,
		PauseTotalNs: mem.PauseTotalNs,
		Uptime:       uint32(time.Since(startTime).Seconds()),
	}, nil
}

// GetUserOnlineStats reports how many concurrent sessions email has open
// across all instances right now, read live off each instance's
// ManagementClient. Unlike sing-box's GetUserOnlineStats
// (backend/singbox/stats.go), which always reports 0 because sing-box's
// v2ray_api has no online concept to query, OpenVPN's management interface
// gives a real answer via CLIENT:ESTABLISHED/DISCONNECT tracking (see
// management.go).
func (b *Backend) GetUserOnlineStats(ctx context.Context, email string) (*common.OnlineStatResponse, error) {
	b.mu.RLock()
	instances := make([]*instanceProcess, 0, len(b.instances))
	for _, instance := range b.instances {
		instances = append(instances, instance)
	}
	b.mu.RUnlock()

	var total int64
	for _, instance := range instances {
		if mgmt := instance.managementClient(); mgmt != nil {
			total += int64(mgmt.OnlineUserCount(email))
		}
	}

	return &common.OnlineStatResponse{Name: email, Value: total}, nil
}

// GetUserOnlineIpListStats reports the last-seen unix timestamp per real
// client IP for email, aggregated across every instance - like
// GetUserOnlineStats, this is real data from the management sockets, not a
// stub.
func (b *Backend) GetUserOnlineIpListStats(ctx context.Context, email string) (*common.StatsOnlineIpListResponse, error) {
	b.mu.RLock()
	instances := make([]*instanceProcess, 0, len(b.instances))
	for _, instance := range b.instances {
		instances = append(instances, instance)
	}
	b.mu.RUnlock()

	ips := make(map[string]int64)
	for _, instance := range instances {
		mgmt := instance.managementClient()
		if mgmt == nil {
			continue
		}
		for ip, ts := range mgmt.OnlineIPs(email) {
			if prev, ok := ips[ip]; !ok || ts > prev {
				ips[ip] = ts
			}
		}
	}

	return &common.StatsOnlineIpListResponse{Name: email, Ips: ips}, nil
}

// GetOutboundsLatency is a stub for the same reason sing-box's is
// (backend/singbox/stats.go): OpenVPN, like a single Hysteria2 inbound, has
// no "outbound" concept at all to probe - a server instance either accepts
// tunneled traffic or it doesn't, there's nothing analogous to xray's
// multi-outbound egress selection to measure latency against. Returns an
// empty, non-error response so callers relying on the Backend interface
// degrade gracefully instead of failing hard; a real implementation is out
// of scope.
func (b *Backend) GetOutboundsLatency(ctx context.Context, request *common.LatencyRequest) (*common.LatencyResponse, error) {
	return &common.LatencyResponse{Latencies: []*common.Latency{}}, nil
}
