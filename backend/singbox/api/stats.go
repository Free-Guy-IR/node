package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"github.com/pasarguard/node/common"
)

func (c *Client) GetSysStats(ctx context.Context) (*common.BackendStatsResponse, error) {
	resp, err := c.StatsServiceClient.GetSysStats(ctx, &statscmd.SysStatsRequest{})
	if err != nil {
		code := codes.Unknown
		if st, ok := status.FromError(err); ok {
			code = st.Code()
		}
		return nil, status.Errorf(code, "failed to get sys stats: %v", err)
	}

	return &common.BackendStatsResponse{
		NumGoroutine: resp.GetNumGoroutine(),
		NumGc:        resp.GetNumGC(),
		Alloc:        resp.GetAlloc(),
		TotalAlloc:   resp.GetTotalAlloc(),
		Sys:          resp.GetSys(),
		Mallocs:      resp.GetMallocs(),
		Frees:        resp.GetFrees(),
		LiveObjects:  resp.GetLiveObjects(),
		PauseTotalNs: resp.GetPauseTotalNs(),
		Uptime:       resp.GetUptime(),
	}, nil
}

// QueryStats queries sing-box's v2ray_api StatsService. It always populates the
// modern "patterns" (repeated substring match) field rather than the legacy
// singular "pattern" field - see proto/stats.proto's doc comment: sing-box's
// server-side handler only ever reads request.Patterns, so sending just
// Pattern would silently disable filtering and return every counter.
func (c *Client) QueryStats(ctx context.Context, pattern string, reset bool) (*statscmd.QueryStatsResponse, error) {
	req := &statscmd.QueryStatsRequest{Reset_: reset}
	if pattern != "" {
		req.Patterns = []string{pattern}
	}

	resp, err := c.StatsServiceClient.QueryStats(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *Client) GetUsersStats(ctx context.Context, reset bool) (*common.StatResponse, error) {
	resp, err := c.QueryStats(ctx, "user>>>", reset)
	if err != nil {
		return nil, err
	}
	return buildStatResponse(resp.GetStat()), nil
}

func (c *Client) GetInboundsStats(ctx context.Context, reset bool) (*common.StatResponse, error) {
	resp, err := c.QueryStats(ctx, "inbound>>>", reset)
	if err != nil {
		return nil, err
	}
	return buildStatResponse(resp.GetStat()), nil
}

func (c *Client) GetOutboundsStats(ctx context.Context, reset bool) (*common.StatResponse, error) {
	resp, err := c.QueryStats(ctx, "outbound>>>", reset)
	if err != nil {
		return nil, err
	}
	return buildStatResponse(resp.GetStat()), nil
}

func (c *Client) GetUserStats(ctx context.Context, email string, reset bool) (*common.StatResponse, error) {
	if email == "" {
		return nil, errors.New("email required")
	}
	resp, err := c.QueryStats(ctx, fmt.Sprintf("user>>>%s>>>", email), reset)
	if err != nil {
		return nil, err
	}
	return buildStatResponse(resp.GetStat()), nil
}

func (c *Client) GetInboundStats(ctx context.Context, tag string, reset bool) (*common.StatResponse, error) {
	if tag == "" {
		return nil, errors.New("tag required")
	}
	resp, err := c.QueryStats(ctx, fmt.Sprintf("inbound>>>%s>>>", tag), reset)
	if err != nil {
		return nil, err
	}
	return buildStatResponse(resp.GetStat()), nil
}

func (c *Client) GetOutboundStats(ctx context.Context, tag string, reset bool) (*common.StatResponse, error) {
	if tag == "" {
		return nil, errors.New("tag required")
	}
	resp, err := c.QueryStats(ctx, fmt.Sprintf("outbound>>>%s>>>", tag), reset)
	if err != nil {
		return nil, err
	}
	return buildStatResponse(resp.GetStat()), nil
}

func buildStatResponse(stats []*statscmd.Stat) *common.StatResponse {
	resp := &common.StatResponse{}
	for _, stat := range stats {
		name, link, statType, ok := parseStatName(stat.GetName())
		if !ok {
			continue
		}

		resp.Stats = append(resp.Stats, &common.Stat{
			Name:  name,
			Type:  statType,
			Link:  link,
			Value: stat.GetValue(),
		})
	}
	return resp
}

// parseStatName mirrors backend/xray/api/stats.go's parseStatName: sing-box's
// v2ray_api registers counters using the exact same "kind>>>name>>>type>>>link"
// naming convention as xray-core.
func parseStatName(raw string) (name, link, statType string, ok bool) {
	parts := strings.Split(raw, ">>>")
	if len(parts) < 4 {
		return "", "", "", false
	}
	if parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}
