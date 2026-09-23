package access_test

import (
	"context"
	"net"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/controller/rpc"
)

const (
	currentClient = "127.0.0.1"
	foreignClient = "::1"
)

func newService(t *testing.T, key uuid.UUID) *rpc.Service {
	t.Helper()
	s := rpc.New(config.NewTestConfig(t.TempDir(), key))
	t.Cleanup(s.Disconnect)
	return s
}

func incomingCtx(apiKeyHeader, clientIP string) context.Context {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", apiKeyHeader))
	if clientIP != "" {
		ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(clientIP), Port: 4321}})
	}
	return ctx
}

func okHandler(context.Context, any) (any, error) {
	return "ok", nil
}

func call(s *rpc.Service, method, key, clientIP string) (any, error) {
	info := &grpc.UnaryServerInfo{FullMethod: "/service.NodeService/" + method}
	return rpc.ConditionalMiddleware(s)(incomingCtx(key, clientIP), nil, info, okHandler)
}

func TestApiKeyCheckRejectsEveryCallerWhileTheConfiguredKeyIsNil(t *testing.T) {
	s := newService(t, uuid.Nil)

	if _, err := call(s, "GetBaseInfo", uuid.Nil.String(), currentClient); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a nil configured key must reject even a matching nil client key, got %v", err)
	}
}

func TestApiKeyCheckAcceptsOnlyTheConfiguredKey(t *testing.T) {
	key := uuid.New()
	s := newService(t, key)

	if resp, err := call(s, "GetBaseInfo", key.String(), currentClient); err != nil || resp != "ok" {
		t.Fatalf("the configured key must reach the handler, got resp=%v err=%v", resp, err)
	}
	if _, err := call(s, "GetBaseInfo", uuid.New().String(), currentClient); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a different key must be denied, got %v", err)
	}
}

func TestBackendManagementMethodsRequireTheCurrentClient(t *testing.T) {
	key := uuid.New()
	s := newService(t, key)
	s.Connect(currentClient, 0)

	for _, method := range []string{"AddBackend", "RemoveBackend", "ListBackends"} {
		if _, err := call(s, method, key.String(), foreignClient); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: a client other than the one that started the node must be denied, got %v", method, err)
		}
		if resp, err := call(s, method, key.String(), currentClient); err != nil || resp != "ok" {
			t.Fatalf("%s: the current client must reach the handler, got resp=%v err=%v", method, resp, err)
		}
	}
}

func TestBackendManagementMethodsAllowAnyClientBeforeFirstConnect(t *testing.T) {
	key := uuid.New()
	s := newService(t, key)

	if resp, err := call(s, "ListBackends", key.String(), foreignClient); err != nil || resp != "ok" {
		t.Fatalf("with no client connected any key-holder must reach the handler, got resp=%v err=%v", resp, err)
	}
}

func TestBaseInfoSkipsTheCurrentClientCheck(t *testing.T) {
	key := uuid.New()
	s := newService(t, key)
	s.Connect(currentClient, 0)

	if resp, err := call(s, "GetBaseInfo", key.String(), foreignClient); err != nil || resp != "ok" {
		t.Fatalf("GetBaseInfo must stay open to any key-holder, got resp=%v err=%v", resp, err)
	}
}
