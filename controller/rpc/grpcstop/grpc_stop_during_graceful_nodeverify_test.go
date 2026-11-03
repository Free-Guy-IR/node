package grpcstop

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestNodeVerifyStopEndsAGracefulStopWaitingOnAHandlerWhoseConnectionIsGone(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(any, grpc.ServerStream) error {
		entered <- struct{}{}
		<-release
		return nil
	}))
	t.Cleanup(func() { close(release) })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = conn.Invoke(context.Background(), "/node.Service/Stuck", &emptypb.Empty{}, &emptypb.Empty{})
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never started")
	}
	_ = conn.Close()

	graceful := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(graceful)
	}()
	time.Sleep(500 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		server.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return while GracefulStop waited on a handler whose connection had closed, so the node's forced shutdown after its deadline hangs")
	}
}
