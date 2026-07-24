package singbox

import (
	"context"
	"errors"

	"github.com/pasarguard/node/common"
)

func (s *SingBox) GetSysStats(ctx context.Context) (*common.BackendStatsResponse, error) {
	return s.client.GetSysStats(ctx)
}

func (s *SingBox) GetStats(ctx context.Context, request *common.StatRequest) (*common.StatResponse, error) {
	switch request.GetType() {

	case common.StatType_Outbounds:
		return s.client.GetOutboundsStats(ctx, request.GetReset_())
	case common.StatType_Outbound:
		return s.client.GetOutboundStats(ctx, request.GetName(), request.GetReset_())

	case common.StatType_Inbounds:
		return s.client.GetInboundsStats(ctx, request.GetReset_())
	case common.StatType_Inbound:
		return s.client.GetInboundStats(ctx, request.GetName(), request.GetReset_())

	case common.StatType_UsersStat:
		return s.client.GetUsersStats(ctx, request.GetReset_())
	case common.StatType_UserStat:
		return s.client.GetUserStats(ctx, request.GetName(), request.GetReset_())

	default:
		return nil, errors.New("not implemented stat type")
	}
}

// GetOutboundsLatency is a known v1 limitation: xray's implementation
// (backend/xray/latency.go) scrapes xray's own HTTP "/debug/vars" observatory
// endpoint, which has no sing-box equivalent wired into this minimal Hysteria2
// integration. A single hysteria2 inbound also has no outbounds to probe in the
// first place (this backend's generated config only ever has a "direct"-style
// egress). Returns an empty, non-error response so callers relying on the
// Backend interface degrade gracefully instead of failing hard; a real
// implementation is out of scope for this pass.
func (s *SingBox) GetOutboundsLatency(ctx context.Context, request *common.LatencyRequest) (*common.LatencyResponse, error) {
	return &common.LatencyResponse{Latencies: []*common.Latency{}}, nil
}

// GetUserOnlineStats is a stub: sing-box's v2ray_api StatsService does not
// track an "online" pseudo-counter the way xray-core does. Confirmed by
// reading experimental/v2rayapi/stats.go on the dev box - RoutedConnection/
// RoutedPacketConnection only ever create
// "user>>>email>>>traffic>>>{uplink,downlink}" counters, there is no
// "user>>>email>>>online" counter and no GetStatsOnline RPC on the service at
// all (see api/proto/stats.proto - only GetStats/QueryStats/GetSysStats exist).
// There is genuinely no data source to answer this from, so rather than
// inventing a number this always reports 0.
func (s *SingBox) GetUserOnlineStats(ctx context.Context, email string) (*common.OnlineStatResponse, error) {
	return &common.OnlineStatResponse{Name: email, Value: 0}, nil
}

// GetUserOnlineIpListStats is a stub for the same reason as
// GetUserOnlineStats: sing-box's v2ray_api has no GetStatsOnlineIpList RPC or
// any per-IP tracking to query, so there is nothing to report.
func (s *SingBox) GetUserOnlineIpListStats(ctx context.Context, email string) (*common.StatsOnlineIpListResponse, error) {
	return &common.StatsOnlineIpListResponse{Name: email, Ips: map[string]int64{}}, nil
}
