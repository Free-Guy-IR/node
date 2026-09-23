package api

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const stockCoreUnknownOperation = "app/proxyman/command: unknown operation > proto: not found"

type recordingHandlerService struct {
	command.UnimplementedHandlerServiceServer
	mu        sync.Mutex
	types     []string
	revokeErr error
}

func (s *recordingHandlerService) AlterInbound(_ context.Context, request *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.types = append(s.types, request.GetOperation().GetType())
	if request.GetOperation().GetType() == revokeUserOperationType && s.revokeErr != nil {
		return nil, s.revokeErr
	}
	return &command.AlterInboundResponse{}, nil
}

func (s *recordingHandlerService) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.types...)
}

func startRecordingHandler(t *testing.T, revokeErr error) (*XrayHandler, *recordingHandlerService) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	service := &recordingHandlerService{revokeErr: revokeErr}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	handler, err := NewXrayAPI(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)
	return handler, service
}

func TestRevokeUserOperationWireFormat(t *testing.T) {
	message := revokeUserOperation("42.alice")
	if message.GetType() != "xray.app.proxyman.sessionrevoke.RevokeUserOperation" {
		t.Fatalf("unexpected type: %s", message.GetType())
	}

	number, wireType, n := protowire.ConsumeTag(message.GetValue())
	if n < 0 || number != 1 || wireType != protowire.BytesType {
		t.Fatalf("unexpected tag: number=%d type=%d n=%d", number, wireType, n)
	}
	email, m := protowire.ConsumeString(message.GetValue()[n:])
	if m < 0 || email != "42.alice" || n+m != len(message.GetValue()) {
		t.Fatalf("unexpected payload: email=%q consumed=%d of %d", email, n+m, len(message.GetValue()))
	}
}

func TestRevokeInboundUserSucceedsOnPatchedCore(t *testing.T) {
	handler, service := startRecordingHandler(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := handler.RevokeInboundUser(ctx, "vless", "42.alice"); err != nil {
		t.Fatal(err)
	}
	if calls := service.calls(); len(calls) != 1 || calls[0] != revokeUserOperationType {
		t.Fatalf("unexpected calls: %v", calls)
	}
}

func TestRevokeInboundUserDetectsStockCoreOnce(t *testing.T) {
	handler, service := startRecordingHandler(t, status.Error(codes.Unknown, stockCoreUnknownOperation))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := handler.RevokeInboundUser(ctx, "vless", "42.alice"); !errors.Is(err, ErrRevokeUnsupported) {
		t.Fatalf("first revoke on a stock core: got %v, want ErrRevokeUnsupported", err)
	}
	if err := handler.RevokeInboundUser(ctx, "vless", "43.bob"); !errors.Is(err, ErrRevokeUnsupported) {
		t.Fatalf("second revoke on a stock core: got %v, want ErrRevokeUnsupported", err)
	}
	if calls := service.calls(); len(calls) != 1 {
		t.Fatalf("a stock core was asked to revoke %d times, want 1", len(calls))
	}
}

func TestRevokeInboundUserReportsOtherFailuresAsIs(t *testing.T) {
	handler, service := startRecordingHandler(t, status.Error(codes.Unknown, "app/proxyman/command: proxy is not a UserManager"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for range 2 {
		err := handler.RevokeInboundUser(ctx, "socks", "42.alice")
		if err == nil || errors.Is(err, ErrRevokeUnsupported) {
			t.Fatalf("got %v, want the core's own error", err)
		}
	}
	if calls := service.calls(); len(calls) != 2 {
		t.Fatalf("got %d revoke calls, want 2", len(calls))
	}
}

func TestIsAbsentUser(t *testing.T) {
	absent := []struct {
		email string
		err   error
	}{
		{"42.alice", status.Error(codes.Unknown, "proxy/vless: User 42.alice not found.")},
		{"42.alice", status.Error(codes.Unknown, "proxy/vmess/inbound: User 42.alice not found.")},
		{"42.alice", status.Error(codes.Unknown, "app/proxyman/command: proxy is not a UserManager")},
		{"42.Alice", status.Error(codes.Unknown, "proxy/vless: User 42.Alice not found.")},
		{"42.Alice", status.Error(codes.Unknown, "proxy/shadowsocks: User 42.alice not found.")},
	}
	for _, c := range absent {
		if !IsAbsentUser(c.err, c.email) {
			t.Fatalf("not treated as an absent user for %q: %v", c.email, c.err)
		}
	}

	present := []struct {
		email string
		err   error
	}{
		{"42.alice", nil},
		{"42.alice", status.Error(codes.Unknown, "proxy/vless: User 142.alice not found.")},
		{"42.Alice", status.Error(codes.Unknown, "proxy/shadowsocks: User 142.alice not found.")},
		{"42.alice", status.Error(codes.Unknown, "proxy/vless: User 42.alice2 not found.")},
		{"42.alice", status.Error(codes.Unavailable, "connection reset by peer")},
		{"42.alice", status.Error(codes.Unknown, "app/proxyman/command: failed to get handler: vless")},
	}
	for _, c := range present {
		if IsAbsentUser(c.err, c.email) {
			t.Fatalf("treated as an absent user for %q: %v", c.email, c.err)
		}
	}
}
