package xray

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const (
	addOperation    = "add"
	removeOperation = "remove"
	revokeOperation = "revoke"
)

type fakeInboundService struct {
	command.UnimplementedHandlerServiceServer
	mu         sync.Mutex
	operations []string
	stockCore  bool
}

func operationName(typeURL string) string {
	switch {
	case strings.HasSuffix(typeURL, ".AddUserOperation"):
		return addOperation
	case strings.HasSuffix(typeURL, ".RemoveUserOperation"):
		return removeOperation
	case strings.HasSuffix(typeURL, ".RevokeUserOperation"):
		return revokeOperation
	}
	return typeURL
}

func (s *fakeInboundService) AlterInbound(_ context.Context, request *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := operationName(request.GetOperation().GetType())
	s.operations = append(s.operations, request.GetTag()+":"+name)
	if name == revokeOperation && s.stockCore {
		return nil, status.Error(codes.Unknown, "app/proxyman/command: unknown operation > proto: not found")
	}
	return &command.AlterInboundResponse{}, nil
}

func (s *fakeInboundService) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	operations := s.operations
	s.operations = nil
	return operations
}

func newRecordingXray(t *testing.T, stockCore bool) (*Xray, *fakeInboundService) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeInboundService{stockCore: stockCore}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	handler, err := api.NewXrayAPI(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)

	cfg, err := NewConfig(`{"inbounds":[
		{"tag":"a","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
		{"tag":"b","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
		{"tag":"c","protocol":"trojan","settings":{"clients":[]}}
	]}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	return &Xray{config: cfg, handler: handler}, service
}

func userOnInbounds(email string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Vless:  &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"},
			Trojan: &common.Trojan{Password: "secret"},
		},
	}
}

func assertOperations(t *testing.T, got []string, want ...string) {
	t.Helper()

	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)
	if strings.Join(sortedGot, ",") != strings.Join(sortedWant, ",") {
		t.Fatalf("operations sent to xray:\n got  %v\n want %v", got, want)
	}
}

func TestSyncUserRevokesOnlyInboundsTheUserLeaves(t *testing.T) {
	x, service := newRecordingXray(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := x.SyncUser(ctx, userOnInbounds("1.alice", "a")); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, service.take(), "a:remove", "a:add", "b:revoke", "c:revoke")

	if err := x.SyncUser(ctx, userOnInbounds("1.alice")); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, service.take(), "a:revoke", "b:revoke", "c:revoke")
}

func TestSyncUserFallsBackToRemoveOnStockCore(t *testing.T) {
	x, service := newRecordingXray(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := x.SyncUser(ctx, userOnInbounds("1.alice", "a")); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, service.take(), "a:remove", "a:add", "b:revoke", "b:remove", "c:remove")

	if err := x.SyncUser(ctx, userOnInbounds("1.alice")); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, service.take(), "a:remove", "b:remove", "c:remove")
}

func TestUpdateUsersRevokesRemovedUsersAndKeepsActiveOnes(t *testing.T) {
	x, service := newRecordingXray(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	users := []*common.User{userOnInbounds("1.alice", "a", "c"), userOnInbounds("2.bob")}
	if err := x.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, service.take(),
		"a:remove", "a:add", "c:remove", "c:add", "b:revoke",
		"a:revoke", "b:revoke", "c:revoke",
	)
}
