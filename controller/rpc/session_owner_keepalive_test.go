package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
)

const (
	ownerPanel  = "10.20.30.1"
	secondPanel = "10.20.30.2"
)

func callFrom(s *Service, ip, key, method string, handler grpc.UnaryHandler) error {
	ctx := fromClient(metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", key)), ip)
	_, err := ConditionalMiddleware(s)(ctx, &common.Empty{}, &grpc.UnaryServerInfo{FullMethod: method}, handler)
	return err
}

func pollUntil(stop <-chan struct{}, every time.Duration, poll func()) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		poll()
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func TestFailingCallsFromTheOwnerPanelKeepItsSession(t *testing.T) {
	s, key := newKeepAliveTestService(t)
	s.Connect(ownerPanel, 1)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollUntil(stop, 300*time.Millisecond, func() {
			err := callFrom(s, ownerPanel, key.String(), "/service.NodeService/GetBackendStats", func(context.Context, any) (any, error) {
				return nil, status.Error(codes.DeadlineExceeded, "core did not answer")
			})
			if err == nil {
				t.Error("GetBackendStats without a backend succeeded")
			}
		})
	}()

	time.Sleep(12 * time.Second)
	owner := s.Ip()
	close(stop)
	wg.Wait()

	if owner != ownerPanel {
		t.Fatalf("the owner panel's failing calls did not keep its session: client is %q after 12s, want %s", owner, ownerPanel)
	}
}

func TestASecondPanelsCallsNeverKeepTheOwnerPanelsSessionAlive(t *testing.T) {
	s, key := newKeepAliveTestService(t)
	s.Connect(ownerPanel, 1)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollUntil(stop, 300*time.Millisecond, func() {
			_ = callFrom(s, secondPanel, key.String(), "/service.NodeService/GetBaseInfo", func(context.Context, any) (any, error) {
				return s.BaseInfoResponse(), nil
			})
			_ = callFrom(s, secondPanel, key.String(), "/service.NodeService/GetBackendStats", func(context.Context, any) (any, error) {
				return &common.BackendStatsResponse{}, nil
			})
		})
	}()

	deadline := time.Now().Add(15 * time.Second)
	for s.Ip() == ownerPanel && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	owner := s.Ip()
	close(stop)
	wg.Wait()

	if owner == ownerPanel {
		t.Fatalf("a second panel's calls kept the silent owner panel's session alive for 15s; the second panel could never take over")
	}
}
