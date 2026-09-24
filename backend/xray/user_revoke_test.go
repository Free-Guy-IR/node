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
	revokeErr  error
	removeErr  error
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
	switch {
	case name == revokeOperation && s.stockCore:
		return nil, status.Error(codes.Unknown, "app/proxyman/command: unknown operation > proto: not found")
	case name == revokeOperation && s.revokeErr != nil:
		return nil, s.revokeErr
	case name == removeOperation && s.removeErr != nil:
		return nil, s.removeErr
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
	return newRecordingXrayWith(t, &fakeInboundService{stockCore: stockCore})
}

func newRecordingXrayWith(t *testing.T, service *fakeInboundService) (*Xray, *fakeInboundService) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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

func clientEmails(x *Xray, tag string) []string {
	for _, inbound := range x.config.InboundConfigs {
		if inbound.Tag != tag {
			continue
		}
		inbound.mu.RLock()
		defer inbound.mu.RUnlock()
		emails := make([]string, 0, len(inbound.clients))
		for email := range inbound.clients {
			emails = append(emails, email)
		}
		sort.Strings(emails)
		return emails
	}
	return nil
}

func TestSyncUserReportsAFailedRevoke(t *testing.T) {
	x, service := newRecordingXray(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := x.SyncUser(ctx, userOnInbounds("1.alice", "a", "b")); err != nil {
		t.Fatal(err)
	}
	service.take()
	service.mu.Lock()
	service.revokeErr = status.Error(codes.Unavailable, "connection reset by peer")
	service.mu.Unlock()

	err := x.SyncUser(ctx, userOnInbounds("1.alice", "a"))
	if err == nil || !strings.Contains(err.Error(), "revoke 1.alice on b") || !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("a failed revoke was not reported: %v", err)
	}
	assertOperations(t, service.take(), "b:revoke", "b:remove", "c:revoke", "c:remove")
	if emails := clientEmails(x, "b"); len(emails) != 0 {
		t.Fatalf("the removed user stayed in the node's desired state for b: %v", emails)
	}
}

func TestUpdateUsersReportsAFailedRevoke(t *testing.T) {
	x, service := newRecordingXrayWith(t, &fakeInboundService{revokeErr: status.Error(codes.Unavailable, "connection reset by peer")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := x.UpdateUsers(ctx, []*common.User{userOnInbounds("2.bob")})
	if err == nil || !strings.Contains(err.Error(), "revoke 2.bob on") {
		t.Fatalf("a failed revoke was not reported: %v", err)
	}
	assertOperations(t, service.take(), "a:revoke", "a:remove", "b:revoke", "b:remove", "c:revoke", "c:remove")
}

func TestRemovingAUserAnInboundCannotHoldIsSilent(t *testing.T) {
	x, service := newRecordingXrayWith(t, &fakeInboundService{revokeErr: status.Error(codes.Unknown, "app/proxyman/sessionrevoke: proxy is not a UserManager")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := x.SyncUser(ctx, userOnInbounds("1.alice")); err != nil {
		t.Fatalf("revoking from an inbound without users was reported: %v", err)
	}
	assertOperations(t, service.take(), "a:revoke", "b:revoke", "c:revoke")
}

func TestStockCoreFallbackIgnoresOnlyAbsentUsers(t *testing.T) {
	x, service := newRecordingXrayWith(t, &fakeInboundService{
		stockCore: true,
		removeErr: status.Error(codes.Unknown, "proxy/vless: User 1.alice not found."),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := x.SyncUser(ctx, userOnInbounds("1.alice")); err != nil {
		t.Fatalf("removing an absent user on a stock core was reported: %v", err)
	}
	assertOperations(t, service.take(), "a:revoke", "a:remove", "b:remove", "c:remove")

	service.mu.Lock()
	service.removeErr = status.Error(codes.Unavailable, "connection reset by peer")
	service.mu.Unlock()

	err := x.SyncUser(ctx, userOnInbounds("1.alice"))
	if err == nil || !strings.Contains(err.Error(), "remove 1.alice from") {
		t.Fatalf("a failed remove on a stock core was not reported: %v", err)
	}
	assertOperations(t, service.take(), "a:remove", "b:remove", "c:remove")
}
