package mtproto

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/9seconds/mtg/v2/mtglib"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

// statsKey mirrors backend/openvpn/user.go's statsKey: a user's traffic is
// scoped per instance (tag), since the same secret can be authorized on more
// than one instance, and the shared pkg/stats.Tracker needs one key per
// (instance, user) pair for per-instance ("Inbound") aggregation.
func statsKey(tag, username string) string {
	return tag + "|" + username
}

func splitStatsKey(key string) (tag string) {
	tag, _, _ = strings.Cut(key, "|")
	return tag
}

// eventAccumulator is the per-instance cumulative-byte-counter side of an
// mtglib.EventStream implementation: EventAuthenticated tells it which
// secret/user a streamID belongs to, and every subsequent EventTraffic for
// that streamID adds onto that user's running Rx/Tx totals until
// EventFinish garbage-collects the streamID->user mapping (the cumulative
// totals themselves are never reset here - only pkg/stats.Tracker's
// delta/reset logic, driven by refreshUserStats below, does that).
type eventAccumulator struct {
	mu           sync.Mutex
	streamUser   map[string]string // streamID -> username
	cumulativeRx map[string]int64  // username -> cumulative bytes received (Telegram/front -> client)
	cumulativeTx map[string]int64  // username -> cumulative bytes sent (client -> Telegram/front)
	email        map[string]string // username -> email, refreshed on every auth

	// outboundRx/outboundTx point at the owning Backend's node-wide totals
	// (see Backend.outboundRx/outboundTx in mtproto.go). Every instance's
	// accumulator is constructed with pointers to the SAME two counters, so
	// traffic from every instance (mtglib and middleproxy relay paths alike
	// - both funnel through this one traffic() method) folds into one
	// backend-wide total. This is deliberately independent from
	// cumulativeRx/cumulativeTx and b.statsTracker above: those exist to
	// answer UserStat/UsersStat (per-user billing, reset on
	// record_user_usages' own schedule); these two atomics exist solely to
	// answer GetStats' Outbound/Outbounds case (node-level dashboard
	// display, reset on record_node_usages' independent schedule) - see
	// that case in GetStats below for why a genuinely separate counter was
	// needed here (unlike OpenVPN/sing-box, MTProto has no pre-existing
	// per-instance aggregate to repurpose).
	outboundRx *atomic.Int64
	outboundTx *atomic.Int64
}

func newEventAccumulator(outboundRx, outboundTx *atomic.Int64) *eventAccumulator {
	return &eventAccumulator{
		streamUser:   make(map[string]string),
		cumulativeRx: make(map[string]int64),
		cumulativeTx: make(map[string]int64),
		email:        make(map[string]string),
		outboundRx:   outboundRx,
		outboundTx:   outboundTx,
	}
}

func (a *eventAccumulator) authenticated(streamID, username, email string) {
	a.mu.Lock()
	a.streamUser[streamID] = username
	a.email[username] = email
	a.mu.Unlock()
}

func (a *eventAccumulator) traffic(streamID string, n uint, isRead bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if isRead {
		a.outboundRx.Add(int64(n))
	} else {
		a.outboundTx.Add(int64(n))
	}

	username, ok := a.streamUser[streamID]
	if !ok {
		return
	}

	if isRead {
		a.cumulativeRx[username] += int64(n)
	} else {
		a.cumulativeTx[username] += int64(n)
	}
}

func (a *eventAccumulator) finished(streamID string) {
	a.mu.Lock()
	delete(a.streamUser, streamID)
	a.mu.Unlock()
}

// snapshot returns the current cumulative counters as pkg/stats.Sample
// entries keyed for the given instance tag.
func (a *eventAccumulator) snapshot(tag string) []stats.Sample {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]stats.Sample, 0, len(a.cumulativeRx)+len(a.cumulativeTx))
	seen := make(map[string]struct{}, len(a.cumulativeRx))

	for username := range a.cumulativeRx {
		seen[username] = struct{}{}
	}
	for username := range a.cumulativeTx {
		seen[username] = struct{}{}
	}

	for username := range seen {
		out = append(out, stats.Sample{
			PublicKey: statsKey(tag, username),
			Email:     a.email[username],
			Rx:        a.cumulativeRx[username],
			Tx:        a.cumulativeTx[username],
		})
	}

	return out
}

// mtprotoEventStream implements mtglib.EventStream for one instance,
// feeding EventAuthenticated/EventTraffic/EventFinish into the instance's
// own eventAccumulator (each instance's Proxy has one, since a given
// streamID only ever belongs to one instance's listener).
type mtprotoEventStream struct {
	accumulator *eventAccumulator
	emailOf     func(username string) string
}

func newMtprotoEventStream(accumulator *eventAccumulator, emailOf func(string) string) *mtprotoEventStream {
	return &mtprotoEventStream{accumulator: accumulator, emailOf: emailOf}
}

func (s *mtprotoEventStream) Send(_ context.Context, event mtglib.Event) {
	switch e := event.(type) {
	case mtglib.EventAuthenticated:
		s.accumulator.authenticated(e.StreamID(), e.SecretID, s.emailOf(e.SecretID))
	case mtglib.EventTraffic:
		s.accumulator.traffic(e.StreamID(), e.Traffic, e.IsRead)
	case mtglib.EventFinish:
		s.accumulator.finished(e.StreamID())
	}
}

func (b *Backend) emailForUsername(username string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.secretsByID[username].email
}

// refreshUserStats pulls every instance's accumulated cumulative counters
// into the shared pkg/stats.Tracker, which turns them into the delta/reset
// series GetStats/GetUsersStats report - mirrors
// backend/openvpn/stats.go's refreshUserStats.
func (b *Backend) refreshUserStats() {
	b.mu.RLock()
	instances := make([]*proxyInstance, 0, len(b.instances))
	for _, inst := range b.instances {
		instances = append(instances, inst)
	}
	b.mu.RUnlock()

	var samples []stats.Sample
	for _, inst := range instances {
		samples = append(samples, inst.accumulator.snapshot(inst.tag)...)
	}

	b.statsTracker.UpdateStatsBatch(samples)
}

func (b *Backend) keysForEmail(username string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	keys := make([]string, 0, len(b.instances))
	for tag := range b.instances {
		keys = append(keys, statsKey(tag, username))
	}
	return keys
}

func rewriteLinkToTag(resp *common.StatResponse) *common.StatResponse {
	for _, s := range resp.GetStats() {
		s.Link = splitStatsKey(s.Link)
	}
	return resp
}

// GetStats maps this backend's data onto the shared StatType vocabulary:
// instances play the role of "inbounds" (they are the listeners accepting
// connections), and there is no "outbound" concept (see GetOutboundsLatency).
//
// Unlike OpenVPN, GetStats keys by username (the MtprotoUser proxy field
// value = str(user_id), matching every other password-based protocol's
// convention), not email - request.GetName() carries whichever key the
// caller expects the stats to be scoped to; the panel calls this with the
// user's email exactly like every other backend, so username lookups here
// key off email, requiring a reverse lookup - see userStat.
func (b *Backend) GetStats(ctx context.Context, request *common.StatRequest) (*common.StatResponse, error) {
	b.refreshUserStats()

	switch request.GetType() {
	case common.StatType_UserStat:
		return b.userStat(ctx, request.GetName(), request.GetReset_()), nil
	case common.StatType_UsersStat:
		return rewriteLinkToTag(b.statsTracker.GetUsersStats(ctx, request.GetReset_())), nil
	case common.StatType_Inbound, common.StatType_Inbounds:
		// Every instance shares the same user set (see user.go) - there is no
		// distinct per-instance aggregate beyond what UsersStat already reports,
		// so instance-level totals are read the same way sing-box's single
		// v2ray-less inbound would be: not applicable as a separate figure.
		return &common.StatResponse{Stats: []*common.Stat{}}, nil
	case common.StatType_Outbound, common.StatType_Outbounds:
		return b.outboundStat(request.GetReset_()), nil
	default:
		return nil, errors.New("unsupported stat type")
	}
}

// outboundStat answers Outbound/Outbounds from the dedicated
// backend-wide atomics every instance's eventAccumulator.traffic() feeds
// (see the eventAccumulator struct's outboundRx/outboundTx comment) -
// entirely independent of b.statsTracker (per-user, reset by
// record_user_usages) and of the per-instance cumulative maps used for
// Inbound/Inbounds above. With reset=true (the only mode the panel's
// record_node_usages job actually uses - see get_outbounds_stats) this
// atomically reads-and-zeros via Swap so no traffic is double-counted or
// dropped between polls; with reset=false it peeks via Load without
// disturbing the running total. Reports one node-wide {tag: "mtproto"}
// pair rather than a per-instance breakdown, because unlike OpenVPN's
// per-listener sockets, every mtproto instance on this backend shares the
// same authenticated-user set (see GetStats' Inbound/Inbounds case above),
// so there is no meaningful sub-instance split to report here either.
func (b *Backend) outboundStat(reset bool) *common.StatResponse {
	var rx, tx int64
	if reset {
		rx = b.outboundRx.Swap(0)
		tx = b.outboundTx.Swap(0)
	} else {
		rx = b.outboundRx.Load()
		tx = b.outboundTx.Load()
	}
	return &common.StatResponse{Stats: stats.BuildInterfaceStats("mtproto", "mtproto", rx, tx)}
}

func (b *Backend) usernameForEmail(email string) (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for username, entry := range b.secretsByID {
		if entry.email == email {
			return username, true
		}
	}
	return "", false
}

func (b *Backend) userStat(ctx context.Context, email string, reset bool) *common.StatResponse {
	username, ok := b.usernameForEmail(email)
	if !ok {
		return &common.StatResponse{Stats: []*common.Stat{}}
	}
	return rewriteLinkToTag(b.statsTracker.GetStats(ctx, b.keysForEmail(username), reset))
}

// GetSysStats reports the controlling node process's own Go runtime stats -
// mtg has no in-process "report your own memory/GC stats" API to query, the
// same situation every non-Xray/sing-box backend in this repo is in (see
// backend/openvpn/stats.go's identical rationale).
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

// GetUserOnlineStats reports whether email has an active stream right now.
// Unlike OpenVPN's management-socket-backed live count, mtg's Proxy exposes
// no public "list active streams for this secret" API, so this reports a
// coarse recently-active signal instead: 1 if any traffic was attributed to
// this user within the last stats refresh, 0 otherwise. Good enough for
// online/offline display; not a precise concurrent-connection count.
func (b *Backend) GetUserOnlineStats(ctx context.Context, email string) (*common.OnlineStatResponse, error) {
	username, ok := b.usernameForEmail(email)
	if !ok {
		return &common.OnlineStatResponse{Name: email, Value: 0}, nil
	}

	b.mu.RLock()
	instances := make([]*proxyInstance, 0, len(b.instances))
	for _, inst := range b.instances {
		instances = append(instances, inst)
	}
	b.mu.RUnlock()

	for _, inst := range instances {
		inst.accumulator.mu.Lock()
		for _, u := range inst.accumulator.streamUser {
			if u == username {
				inst.accumulator.mu.Unlock()
				return &common.OnlineStatResponse{Name: email, Value: 1}, nil
			}
		}
		inst.accumulator.mu.Unlock()
	}

	return &common.OnlineStatResponse{Name: email, Value: 0}, nil
}

// GetUserOnlineIpListStats has no data source here - mtg's Proxy does not
// expose per-connection source IPs to an external observer the way
// OpenVPN's management socket does. Returns an empty, non-error response so
// callers degrade gracefully (mirrors GetOutboundsLatency below).
func (b *Backend) GetUserOnlineIpListStats(ctx context.Context, email string) (*common.StatsOnlineIpListResponse, error) {
	return &common.StatsOnlineIpListResponse{Name: email, Ips: map[string]int64{}}, nil
}

// GetOutboundsLatency: MTProto, like OpenVPN, has no "outbound" concept to
// probe - see backend/openvpn/stats.go's identical rationale.
func (b *Backend) GetOutboundsLatency(ctx context.Context, request *common.LatencyRequest) (*common.LatencyResponse, error) {
	return &common.LatencyResponse{Latencies: []*common.Latency{}}, nil
}
