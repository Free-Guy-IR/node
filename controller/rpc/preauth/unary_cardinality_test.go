package preauth_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func openRawGetBaseInfo(t *testing.T, conn *grpc.ClientConn, key string) grpc.ClientStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(withKey(key), 15*time.Second)
	t.Cleanup(cancel)
	desc := &grpc.StreamDesc{StreamName: "GetBaseInfo", ClientStreams: true}
	stream, err := conn.NewStream(ctx, desc, common.NodeService_GetBaseInfo_FullMethodName)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func rawGetBaseInfoStream(t *testing.T) grpc.ClientStream {
	t.Helper()
	key := uuid.New()
	addr, certPath := startNode(t, config.NewTestConfig(t.TempDir(), key))
	return openRawGetBaseInfo(t, dialNode(t, addr, certPath), key.String())
}

func TestUnaryCallCarryingASecondRequestMessageIsRefused(t *testing.T) {
	stream := rawGetBaseInfoStream(t)

	for i := 1; i <= 2; i++ {
		if err := stream.SendMsg(&common.Empty{}); err != nil {
			t.Fatalf("send request message %d: %v", i, err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}

	var info common.BaseInfoResponse
	err := stream.RecvMsg(&info)
	if status.Code(err) != codes.Internal {
		t.Fatalf("a unary GetBaseInfo carrying two request messages must be refused with Internal, got err=%v node_version=%q", err, info.GetNodeVersion())
	}
}

func TestUnaryCallWithoutARequestMessageIsRefused(t *testing.T) {
	stream := rawGetBaseInfoStream(t)

	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var info common.BaseInfoResponse
	err := stream.RecvMsg(&info)
	t.Logf("GetBaseInfo without a request message: code=%s err=%v", status.Code(err), err)
	if err == nil {
		t.Fatalf("a unary GetBaseInfo that carried no request message must be refused, got node_version=%q", info.GetNodeVersion())
	}
}

func TestUnaryCallIsAnsweredOnlyAfterTheCallerHalfCloses(t *testing.T) {
	stream := rawGetBaseInfoStream(t)

	if err := stream.SendMsg(&common.Empty{}); err != nil {
		t.Fatal(err)
	}
	var info common.BaseInfoResponse
	answered := make(chan error, 1)
	go func() { answered <- stream.RecvMsg(&info) }()

	select {
	case err := <-answered:
		t.Fatalf("the node answered a unary GetBaseInfo before the caller half-closed its request stream: err=%v node_version=%q", err, info.GetNodeVersion())
	case <-time.After(time.Second):
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("GetBaseInfo after the caller half-closed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the node did not answer a unary GetBaseInfo within 5 s of the caller half-closing")
	}
	if info.GetNodeVersion() == "" {
		t.Fatal("GetBaseInfo answered without a node version")
	}
}

func TestHalfOpenUnaryCallWithAWrongKeyIsRefusedWithoutWaitingForHalfClose(t *testing.T) {
	addr, certPath := startNode(t, config.NewTestConfig(t.TempDir(), uuid.New()))
	stream := openRawGetBaseInfo(t, dialNode(t, addr, certPath), uuid.New().String())

	_ = stream.SendMsg(&common.Empty{})
	answered := make(chan error, 1)
	go func() { answered <- stream.RecvMsg(&common.BaseInfoResponse{}) }()

	select {
	case err := <-answered:
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("a half-open GetBaseInfo with a wrong api key must be refused with PermissionDenied, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a half-open GetBaseInfo with a wrong api key was not refused within 3 s; the pre-auth tap must refuse it before any handler waits for the caller to half-close")
	}
}

func TestHalfOpenUnaryCallsDoNotStallConcurrentCallsOnTheSameConnection(t *testing.T) {
	key := uuid.New()
	addr, certPath := startNode(t, config.NewTestConfig(t.TempDir(), key))
	conn := dialNode(t, addr, certPath)
	client := common.NewNodeServiceClient(conn)

	const halfOpen, callers, callsEach = 8, 32, 5
	pending := make([]grpc.ClientStream, halfOpen)
	for i := range pending {
		pending[i] = openRawGetBaseInfo(t, conn, key.String())
		if err := pending[i].SendMsg(&common.Empty{}); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(withKey(key.String()), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var failures atomic.Int64
	var firstErr atomic.Value
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range callsEach {
				if _, err := client.GetBaseInfo(ctx, &common.Empty{}); err != nil {
					failures.Add(1)
					firstErr.CompareAndSwap(nil, err.Error())
				}
			}
		}()
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("%d of %d GetBaseInfo calls failed while %d unary calls sat half-open on the same connection, first error: %v", n, callers*callsEach, halfOpen, firstErr.Load())
	}

	results := make(chan error, halfOpen)
	for _, stream := range pending {
		go func() {
			if err := stream.CloseSend(); err != nil {
				results <- err
				return
			}
			results <- stream.RecvMsg(&common.BaseInfoResponse{})
		}()
	}
	for range halfOpen {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("a half-open GetBaseInfo failed after its caller half-closed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a half-open GetBaseInfo was not answered within 5 s of its caller half-closing")
		}
	}
}
