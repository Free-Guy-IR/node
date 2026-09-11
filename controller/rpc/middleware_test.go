package rpc

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
)

func newMiddlewareTestService(key uuid.UUID) *Service {
	return New(&config.Config{ApiKey: key})
}

func incomingCtx(apiKeyHeader, clientIP string) context.Context {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", apiKeyHeader))
	if clientIP != "" {
		ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(clientIP), Port: 4321}})
	}
	return ctx
}

func okHandler(ctx context.Context, req any) (any, error) {
	return "ok", nil
}

func TestValidateApiKeyRejectsNilConfiguredKey(t *testing.T) {
	s := newMiddlewareTestService(uuid.Nil)

	err := validateApiKey(incomingCtx(uuid.Nil.String(), ""), s)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a nil configured key must reject even a matching nil client key, got %v", err)
	}
}

func TestValidateApiKeyAcceptsMatchingKey(t *testing.T) {
	key := uuid.New()
	s := newMiddlewareTestService(key)

	if err := validateApiKey(incomingCtx(key.String(), ""), s); err != nil {
		t.Fatalf("matching key must pass, got %v", err)
	}
}

func TestValidateApiKeyRejectsMismatchedKey(t *testing.T) {
	s := newMiddlewareTestService(uuid.New())

	err := validateApiKey(incomingCtx(uuid.New().String(), ""), s)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mismatched key must be denied, got %v", err)
	}
}

func TestBackendManagementMethodsRequireTheCurrentClient(t *testing.T) {
	key := uuid.New()
	s := newMiddlewareTestService(key)
	s.Connect("10.0.0.1", 0)
	t.Cleanup(s.Disconnect)

	interceptor := ConditionalMiddleware(s)

	for _, method := range []string{
		"/service.NodeService/AddBackend",
		"/service.NodeService/RemoveBackend",
		"/service.NodeService/ListBackends",
	} {
		info := &grpc.UnaryServerInfo{FullMethod: method}

		_, err := interceptor(incomingCtx(key.String(), "9.9.9.9"), nil, info, okHandler)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: a foreign client must be denied, got %v", method, err)
		}

		resp, err := interceptor(incomingCtx(key.String(), "10.0.0.1"), nil, info, okHandler)
		if err != nil || resp != "ok" {
			t.Fatalf("%s: the current client must reach the handler, got resp=%v err=%v", method, resp, err)
		}
	}
}

func TestBackendManagementMethodsAllowAnyClientBeforeFirstConnect(t *testing.T) {
	key := uuid.New()
	s := newMiddlewareTestService(key)

	interceptor := ConditionalMiddleware(s)
	info := &grpc.UnaryServerInfo{FullMethod: "/service.NodeService/ListBackends"}

	resp, err := interceptor(incomingCtx(key.String(), "9.9.9.9"), nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Fatalf("with no client connected any key-holder must reach the handler, got resp=%v err=%v", resp, err)
	}
}

func TestBaseInfoSkipsTheCurrentClientCheck(t *testing.T) {
	key := uuid.New()
	s := newMiddlewareTestService(key)
	s.Connect("10.0.0.1", 0)
	t.Cleanup(s.Disconnect)

	interceptor := ConditionalMiddleware(s)
	info := &grpc.UnaryServerInfo{FullMethod: "/service.NodeService/GetBaseInfo"}

	resp, err := interceptor(incomingCtx(key.String(), "9.9.9.9"), nil, info, okHandler)
	if err != nil || resp != "ok" {
		t.Fatalf("GetBaseInfo must stay open to any key-holder, got resp=%v err=%v", resp, err)
	}
}
