// Package api is a thin gRPC client for sing-box's experimental.v2ray_api
// StatsService. See proto/stats.proto for why this is a hand-written minimal
// client rather than a dependency on xtls/xray-core's stats client.
package api

import (
	"context"
	"fmt"
	"net"
	"time"

	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const maxGRPCMessageSize = 64 * 1024 * 1024 // 64MB

// Client wraps a gRPC connection to a running sing-box process's
// experimental.v2ray_api port. Mirrors backend/xray/api.XrayHandler's dial
// pattern.
type Client struct {
	StatsServiceClient statscmd.StatsServiceClient
	GrpcClient         *grpc.ClientConn
}

func NewClient(apiPort int) (*Client, error) {
	c := &Client{}
	target := fmt.Sprintf("127.0.0.1:%v", apiPort)
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
	}

	var err error
	c.GrpcClient, err = grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
			grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
		),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			conn, dialErr := dialer.DialContext(ctx, "tcp4", addr)
			if dialErr == nil {
				return conn, nil
			}

			var fallback net.Dialer
			return fallback.DialContext(ctx, "tcp", addr)
		}),
	)
	if err != nil {
		return nil, err
	}

	c.StatsServiceClient = statscmd.NewStatsServiceClient(c.GrpcClient)

	return c, nil
}

func (c *Client) Close() {
	if c.GrpcClient != nil {
		_ = c.GrpcClient.Close()
	}
	c.StatsServiceClient = nil
}
