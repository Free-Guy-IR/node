package xray

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const sessionTestConfig = `{
	"log": {"loglevel": "warning"},
	"inbounds": [{
		"tag": "VLESS TCP SESSION",
		"listen": "127.0.0.1",
		"port": %d,
		"protocol": "vless",
		"settings": {"clients": [], "decryption": "none"}
	}],
	"outbounds": [{"tag": "direct", "protocol": "freedom"}]
}`

const sessionTestInbound = "VLESS TCP SESSION"

func startByteSource(t *testing.T) *net.TCPAddr {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		chunk := make([]byte, 32*1024)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				for {
					if _, err := conn.Write(chunk); err != nil {
						return
					}
				}
			}()
		}
	}()

	return listener.Addr().(*net.TCPAddr)
}

func dialVlessStream(inboundPort int, id uuid.UUID, target *net.TCPAddr) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(inboundPort)), 3*time.Second)
	if err != nil {
		return nil, err
	}

	request := []byte{0}
	request = append(request, id[:]...)
	request = append(request, 0, 1)
	request = binary.BigEndian.AppendUint16(request, uint16(target.Port))
	request = append(request, 1)
	request = append(request, target.IP.To4()...)
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func openVlessStream(t *testing.T, inboundPort int, id uuid.UUID, target *net.TCPAddr) net.Conn {
	t.Helper()

	conn, err := dialVlessStream(inboundPort, id, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func receive(conn net.Conn, window time.Duration) (int64, error) {
	_ = conn.SetReadDeadline(time.Now().Add(window))
	received, err := io.Copy(io.Discard, conn)
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return received, nil
	case err == nil:
		return received, io.EOF
	}
	return received, err
}

func requireFlowing(t *testing.T, conn net.Conn, moment string) {
	t.Helper()

	received, err := receive(conn, 500*time.Millisecond)
	if err != nil || received == 0 {
		t.Fatalf("open connection stopped %s: received %d bytes, err %v", moment, received, err)
	}
}

func sessionUser(email string, id uuid.UUID, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies:  &common.Proxy{Vless: &common.Vless{Id: id.String()}},
	}
}

func startSessionBackend(t *testing.T, binary string, users ...*common.User) (*Xray, int) {
	t.Helper()

	inboundPort := netutil.FindFreePort()
	xrayConfig, err := NewConfig(fmt.Sprintf(sessionTestConfig, inboundPort), nil)
	if err != nil {
		t.Fatal(err)
	}

	back, err := New(
		context.Background(),
		xrayConfig,
		users,
		netutil.FindFreePort(),
		netutil.FindFreePort(),
		&config.Config{
			XrayExecutablePath:  binary,
			XrayAssetsPath:      assetsPath,
			GeneratedConfigPath: t.TempDir(),
			LogBufferSize:       1000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(back.Shutdown)
	t.Logf("core under test: %s", back.Version())
	return back, inboundPort
}

func forEachXrayBinary(t *testing.T, run func(t *testing.T, binary string)) {
	t.Helper()

	binaries := xrayBinariesUnderTest()
	if len(binaries) == 0 {
		t.Skip("no xray binary found")
	}
	for name, binary := range binaries {
		t.Run(name, func(t *testing.T) { run(t, binary) })
	}
}

func TestUserSyncKeepsAnUnchangedUsersOpenConnection(t *testing.T) {
	forEachXrayBinary(t, keepUnchangedUsersOpenConnection)
}

func keepUnchangedUsersOpenConnection(t *testing.T, binary string) {
	source := startByteSource(t)
	aliceID := uuid.New()
	alice := sessionUser("1.alice", aliceID, sessionTestInbound)
	back, inboundPort := startSessionBackend(t, binary, alice)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream := openVlessStream(t, inboundPort, aliceID, source)
	requireFlowing(t, stream, "before any sync")

	if err := back.SyncUser(ctx, alice); err != nil {
		t.Fatal(err)
	}
	requireFlowing(t, stream, "after SyncUser sent the same user again")

	if err := back.UpdateUsers(ctx, []*common.User{alice}); err != nil {
		t.Fatal(err)
	}
	requireFlowing(t, stream, "after UpdateUsers sent the same user again")

	if err := back.SyncUser(ctx, sessionUser("1.alice", aliceID)); err != nil {
		t.Fatal(err)
	}

	if received, _ := receive(openVlessStream(t, inboundPort, aliceID, source), time.Second); received != 0 {
		t.Fatalf("a removed user opened a new connection and received %d bytes", received)
	}

	received, err := receive(stream, time.Second)
	t.Logf("the connection the removed user had already opened received %d bytes in the next second (err %v)", received, err)
}

func TestUserSyncAcceptsAnUnchangedUsersNewConnections(t *testing.T) {
	forEachXrayBinary(t, acceptUnchangedUsersNewConnections)
}

func firstBytes(conn net.Conn, window time.Duration) (int, error) {
	_ = conn.SetReadDeadline(time.Now().Add(window))
	buffer := make([]byte, 1024)
	return conn.Read(buffer)
}

func acceptUnchangedUsersNewConnections(t *testing.T, binary string) {
	const wantAttempts = 400

	source := startByteSource(t)
	aliceID := uuid.New()
	alice := sessionUser("1.alice", aliceID, sessionTestInbound)
	back, inboundPort := startSessionBackend(t, binary, alice)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	probe := openVlessStream(t, inboundPort, aliceID, source)
	requireFlowing(t, probe, "before any sync")
	_ = probe.Close()

	var attempts, refused atomic.Int64
	var firstRefusal atomic.Value
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempts.Add(1) <= wantAttempts {
				conn, err := dialVlessStream(inboundPort, aliceID, source)
				if err != nil {
					refused.Add(1)
					firstRefusal.CompareAndSwap(nil, err.Error())
					continue
				}
				n, err := firstBytes(conn, 2*time.Second)
				_ = conn.Close()
				if n == 0 {
					refused.Add(1)
					firstRefusal.CompareAndSwap(nil, fmt.Sprintf("no bytes, err %v", err))
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	rounds := 0
	var syncErr error
syncing:
	for syncErr == nil {
		select {
		case <-done:
			break syncing
		default:
		}
		if rounds%2 == 0 {
			syncErr = back.SyncUser(ctx, alice)
		} else {
			syncErr = back.UpdateUsers(ctx, []*common.User{alice})
		}
		rounds++
	}
	<-done

	if syncErr != nil {
		t.Fatal(syncErr)
	}
	if refused.Load() != 0 {
		t.Fatalf("%d of %d new connections of an unchanged user were refused while it was resynced %d times (first: %v)",
			refused.Load(), wantAttempts, rounds, firstRefusal.Load())
	}
	t.Logf("%d new connections of the unchanged user all carried data across %d syncs", wantAttempts, rounds)
}
