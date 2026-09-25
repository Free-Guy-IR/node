package rpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

type scriptedUserStream struct {
	grpc.ServerStream
	users      []*common.User
	next       int
	beforeRecv func(index int)
	closed     bool
}

func (s *scriptedUserStream) Context() context.Context { return context.Background() }

func (s *scriptedUserStream) Recv() (*common.User, error) {
	if s.beforeRecv != nil {
		s.beforeRecv(s.next)
	}
	if s.next >= len(s.users) {
		return nil, io.EOF
	}
	u := s.users[s.next]
	s.next++
	return u, nil
}

func (s *scriptedUserStream) SendAndClose(*common.Empty) error {
	s.closed = true
	return nil
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestSyncUserStreamRefusesAMessageThatArrivesAfterTheCoreIsGone(t *testing.T) {
	s := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	t.Cleanup(s.Disconnect)

	data := &common.Backend{
		Type:   common.BackendType_MTPROTO,
		Config: fmt.Sprintf(`{"instances":[{"tag":"stream","port":%d,"mode":"plain"}]}`, freeLoopbackPort(t)),
	}
	if err := s.StartBackend(context.Background(), data); err != nil {
		t.Fatalf("StartBackend: %v", err)
	}

	stream := &scriptedUserStream{
		users: []*common.User{{Email: "before-stop"}, {Email: "after-stop"}},
		beforeRecv: func(index int) {
			if index == 1 {
				s.LockControl()
				s.Disconnect()
				s.UnlockControl()
			}
		},
	}

	err := s.SyncUser(stream)
	if status.Code(err) != codes.Unavailable || stream.closed {
		t.Fatalf("a SyncUser stream message that arrived after the node lost its core ended the stream with (err=%v, acknowledged=%v); it must fail with codes.Unavailable and never be acknowledged", err, stream.closed)
	}
}
