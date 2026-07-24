package api

import (
	"context"
	"net"
	"strings"
	"testing"

	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"google.golang.org/grpc"
)

// fakeStatsServer mimics the real sing-box behavior found in
// experimental/v2rayapi/stats.go: QueryStats only ever inspects
// request.Patterns, never the deprecated singular request.Pattern field. This
// is what makes the test below meaningful - a client that populated only
// Pattern (as the original spike proto only allowed) would get every counter
// back unfiltered here, not just the matching one.
type fakeStatsServer struct {
	statscmd.UnimplementedStatsServiceServer
	lastRequest *statscmd.QueryStatsRequest
}

var allFakeStats = []*statscmd.Stat{
	{Name: "user>>>alice@example.com>>>traffic>>>uplink", Value: 111},
	{Name: "user>>>bob@example.com>>>traffic>>>uplink", Value: 222},
}

func (f *fakeStatsServer) QueryStats(_ context.Context, req *statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
	f.lastRequest = req

	if len(req.GetPatterns()) == 0 {
		return &statscmd.QueryStatsResponse{Stat: allFakeStats}, nil
	}

	var out []*statscmd.Stat
	for _, stat := range allFakeStats {
		for _, pattern := range req.GetPatterns() {
			if strings.Contains(stat.GetName(), pattern) {
				out = append(out, stat)
				break
			}
		}
	}
	return &statscmd.QueryStatsResponse{Stat: out}, nil
}

func startFakeStatsServer(t *testing.T) (*fakeStatsServer, int) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer()
	fake := &fakeStatsServer{}
	statscmd.RegisterStatsServiceServer(srv, fake)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	return fake, lis.Addr().(*net.TCPAddr).Port
}

func TestQueryStats_PopulatesPatternsFieldNotDeprecatedPattern(t *testing.T) {
	fake, port := startFakeStatsServer(t)

	client, err := NewClient(port)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	resp, err := client.QueryStats(context.Background(), "alice@example.com", false)
	if err != nil {
		t.Fatalf("QueryStats() error = %v", err)
	}

	if fake.lastRequest == nil {
		t.Fatal("fake server never received a request")
	}
	if got := fake.lastRequest.GetPatterns(); len(got) != 1 || got[0] != "alice@example.com" {
		t.Fatalf("expected server to receive Patterns=[alice@example.com], got %+v", got)
	}

	stats := resp.GetStat()
	if len(stats) != 1 || stats[0].GetName() != "user>>>alice@example.com>>>traffic>>>uplink" {
		t.Fatalf("expected exactly alice's stat back (proving server-side filtering via Patterns worked), got %+v", stats)
	}
}

func TestQueryStats_EmptyPatternReturnsEverything(t *testing.T) {
	_, port := startFakeStatsServer(t)

	client, err := NewClient(port)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	resp, err := client.QueryStats(context.Background(), "", false)
	if err != nil {
		t.Fatalf("QueryStats() error = %v", err)
	}
	if len(resp.GetStat()) != len(allFakeStats) {
		t.Fatalf("expected all %d stats back for an empty pattern, got %d", len(allFakeStats), len(resp.GetStat()))
	}
}

func TestGetSysStats_MapsAllFields(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := grpc.NewServer()
	statscmd.RegisterStatsServiceServer(srv, &sysStatsFakeServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := NewClient(lis.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	stats, err := client.GetSysStats(context.Background())
	if err != nil {
		t.Fatalf("GetSysStats() error = %v", err)
	}

	if stats.GetNumGoroutine() != 7 || stats.GetUptime() != 42 || stats.GetAlloc() != 1024 {
		t.Fatalf("unexpected mapped sys stats: %+v", stats)
	}
}

type sysStatsFakeServer struct {
	statscmd.UnimplementedStatsServiceServer
}

func (sysStatsFakeServer) GetSysStats(context.Context, *statscmd.SysStatsRequest) (*statscmd.SysStatsResponse, error) {
	return &statscmd.SysStatsResponse{
		NumGoroutine: 7,
		Uptime:       42,
		Alloc:        1024,
	}, nil
}
