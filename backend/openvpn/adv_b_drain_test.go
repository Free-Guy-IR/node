package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdvBDrainTimeoutNeverBillsAReauthenticatedSessionTwice(t *testing.T) {
	dir, err := os.MkdirTemp("", "ab")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	const email = "alice@example.com"
	var authCalls atomic.Int32
	reauthEntered := make(chan struct{})
	release := make(chan struct{})
	mgmt := newManagementClient("advb", sockPath, func(username, password string) (string, uint32, bool) {
		if authCalls.Add(1) == 2 {
			close(reauthEntered)
			<-release
		}
		return email, 0, true
	})

	serverConn := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		fmt.Fprint(conn, ">INFO:OpenVPN Management Interface Version 5\r\n")
		r := bufio.NewReader(conn)
		for range 2 {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			fmt.Fprintf(conn, "SUCCESS: %s\r\n", strings.TrimSpace(line))
		}
		serverConn <- conn
		_, _ = io.Copy(io.Discard, r)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgmt.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	conn := <-serverConn

	fmt.Fprint(conn, ">CLIENT:CONNECT,1,0\r\n>CLIENT:ENV,username=alice\r\n>CLIENT:ENV,password=pw\r\n>CLIENT:ENV,END\r\n"+
		">CLIENT:ESTABLISHED,1\r\n>CLIENT:ENV,END\r\n>BYTECOUNT_CLI:1,100,200\r\n")
	waitForCondition(t, 5*time.Second, func() bool {
		return mgmt.UserStats(email) == ClientStats{Uplink: 200, Downlink: 100}
	})

	mgmt.beginDrain()
	fmt.Fprint(conn, ">CLIENT:REAUTH,1,1\r\n>CLIENT:ENV,username=alice\r\n>CLIENT:ENV,password=pw\r\n>CLIENT:ENV,END\r\n"+
		">BYTECOUNT_CLI:1,150,260\r\n")
	select {
	case <-reauthEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the read loop never processed the renegotiation")
	}

	drained := make(chan struct{})
	go func() {
		mgmt.finishDrain(50 * time.Millisecond)
		close(drained)
	}()
	waitForCondition(t, 5*time.Second, mgmt.stopped.Load)
	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never finished")
	}

	got := mgmt.AllUserStats()[email]
	want := ClientStats{Uplink: 260, Downlink: 150}
	if got != want {
		t.Fatalf("session billed as %+v after the drain, its cumulative counters were %+v", got, want)
	}
}
