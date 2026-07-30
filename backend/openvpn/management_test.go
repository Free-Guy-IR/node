package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met within timeout")
	}
}

// listenUnix opens a real Unix domain socket listener in a temp directory.
// A real OS socket (unlike net.Pipe, which is fully synchronous/unbuffered)
// is used deliberately: this test drives a bidirectional protocol where
// either side can write without the other actively reading at that exact
// instant (e.g. the fake server pushes an unprompted banner immediately on
// accept), which would deadlock over net.Pipe. This also exercises the same
// net.Dial("unix", ...) code path process.go actually uses in production.
func listenUnix(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "mgmt.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.(*net.UnixListener), sockPath
}

// TestManagementClient_Protocol drives a fake OpenVPN management-interface
// server (hand-written against real OpenVPN management-notes.txt semantics)
// against a real ManagementClient over a real Unix socket, exercising the
// full flow this package depends on:
//
//   - the "state on"/"bytecount 5" subscription handshake, including the
//     unprompted ">INFO:..." banner every real openvpn management socket
//     sends immediately on connect (readReplyLine must skip it);
//   - CLIENT:CONNECT with valid credentials -> exact "client-auth CID
//     KID\nEND" multi-line reply;
//   - CLIENT:CONNECT with invalid credentials -> exact 'client-deny CID KID
//     "reason"' reply;
//   - CLIENT:ESTABLISHED -> online tracking;
//   - BYTECOUNT_CLI -> live per-user Uplink/Downlink accounting with the
//     codebase's uplink=server-transmitted / downlink=server-received
//     convention (see ClientStats' doc comment);
//   - CLIENT:DISCONNECT -> folding the session's final bytes_sent/
//     bytes_received into closed-session totals and clearing online state.
//
// The fake server is explicitly paced with "proceed"/"finish" handshake
// channels rather than just firing all events back to back: without that,
// the goroutine would race ahead to DISCONNECT before the test ever gets a
// chance to observe the ESTABLISHED/BYTECOUNT state, and the final
// mgmt.Close() must run before the server drops the connection, or the
// client's (correct, by-design - see management.go's reconnect logic) EOF
// handling would kick off a pointless reconnect attempt against a peer that
// will never accept again.
func TestManagementClient_Protocol(t *testing.T) {
	ln, sockPath := listenUnix(t)

	mgmt := newManagementClient("test", sockPath, func(username, password string) (string, bool) {
		if username == "alice" && password == "secret" {
			return "alice@example.com", true
		}
		return "", false
	})

	serverErrCh := make(chan error, 1)
	replyCh := make(chan string, 4)
	proceed := make(chan struct{})
	finish := make(chan struct{})

	go func() {
		serverConn, err := ln.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer serverConn.Close()
		r := bufio.NewReader(serverConn)

		// Real openvpn sends this immediately on connect, before any reply -
		// readReplyLine must skip it rather than mistaking it for a command
		// reply.
		fmt.Fprint(serverConn, ">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\r\n")

		for _, want := range []string{"state on", "bytecount 5"} {
			line, err := r.ReadString('\n')
			if err != nil {
				serverErrCh <- err
				return
			}
			if strings.TrimSpace(line) != want {
				serverErrCh <- fmt.Errorf("expected command %q, got %q", want, line)
				return
			}
			fmt.Fprintf(serverConn, "SUCCESS: %s\r\n", want)
		}

		// A valid CONNECT: expect a client-auth reply.
		fmt.Fprint(serverConn, ">CLIENT:CONNECT,1,0\r\n>CLIENT:ENV,username=alice\r\n>CLIENT:ENV,password=secret\r\n>CLIENT:ENV,END\r\n")
		line1, err := r.ReadString('\n')
		if err != nil {
			serverErrCh <- err
			return
		}
		line2, err := r.ReadString('\n')
		if err != nil {
			serverErrCh <- err
			return
		}
		replyCh <- strings.TrimSpace(line1) + "|" + strings.TrimSpace(line2)

		// ESTABLISHED with a real client IP, then a periodic bytecount
		// sample.
		fmt.Fprint(serverConn, ">CLIENT:ESTABLISHED,1\r\n>CLIENT:ENV,trusted_ip=1.2.3.4\r\n>CLIENT:ENV,END\r\n")
		fmt.Fprint(serverConn, ">BYTECOUNT_CLI:1,1000,2000\r\n")

		<-proceed // wait for the test to verify the established/bytecount state

		// An invalid CONNECT: expect a client-deny reply.
		fmt.Fprint(serverConn, ">CLIENT:CONNECT,2,0\r\n>CLIENT:ENV,username=mallory\r\n>CLIENT:ENV,password=wrong\r\n>CLIENT:ENV,END\r\n")
		line3, err := r.ReadString('\n')
		if err != nil {
			serverErrCh <- err
			return
		}
		replyCh <- strings.TrimSpace(line3)

		// Disconnect cid 1 with final byte counters.
		fmt.Fprint(serverConn, ">CLIENT:DISCONNECT,1\r\n>CLIENT:ENV,bytes_sent=2500\r\n>CLIENT:ENV,bytes_received=1200\r\n>CLIENT:ENV,END\r\n")

		<-finish // wait for the test to close mgmt before this end hangs up
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgmt.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	select {
	case err := <-serverErrCh:
		t.Fatalf("fake server error: %v", err)
	case got := <-replyCh:
		want := "client-auth 1 0|END"
		if got != want {
			t.Fatalf("expected client-auth reply %q, got %q", want, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client-auth reply")
	}

	waitForCondition(t, time.Second, func() bool { return mgmt.OnlineCount() == 1 })
	waitForCondition(t, time.Second, func() bool {
		s := mgmt.UserStats("alice@example.com")
		return s.Uplink == 2000 && s.Downlink == 1000
	})

	got := mgmt.UserStats("alice@example.com")
	if got.Uplink != 2000 || got.Downlink != 1000 {
		t.Fatalf("expected Uplink=2000 Downlink=1000 (BYTES_OUT=uplink, BYTES_IN=downlink), got %+v", got)
	}

	ips := mgmt.OnlineIPs("alice@example.com")
	if ips["1.2.3.4"] == 0 {
		t.Fatalf("expected trusted_ip 1.2.3.4 to be recorded, got %+v", ips)
	}

	close(proceed)

	select {
	case err := <-serverErrCh:
		t.Fatalf("fake server error: %v", err)
	case got := <-replyCh:
		want := `client-deny 2 0 "invalid credentials"`
		if got != want {
			t.Fatalf("expected client-deny reply %q, got %q", want, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client-deny reply")
	}

	waitForCondition(t, time.Second, func() bool { return mgmt.OnlineCount() == 0 })

	finalStats := mgmt.UserStats("alice@example.com")
	if finalStats.Uplink != 2500 || finalStats.Downlink != 1200 {
		t.Fatalf("expected final Uplink=2500 Downlink=1200 from DISCONNECT env (bytes_sent/bytes_received), got %+v", finalStats)
	}

	all := mgmt.AllUserStats()
	if s, ok := all["alice@example.com"]; !ok || s.Uplink != 2500 || s.Downlink != 1200 {
		t.Fatalf("expected AllUserStats to reflect the same closed totals, got %+v", all)
	}

	mgmt.Close()
	close(finish)
}

// TestManagementClient_EnforceAuthorized checks the self-healing
// disconnect-on-revocation path: EnforceAuthorized must issue a
// "client-kill CID" for any session whose username the supplied predicate no
// longer authorizes.
func TestManagementClient_EnforceAuthorized(t *testing.T) {
	ln, sockPath := listenUnix(t)

	mgmt := newManagementClient("test", sockPath, func(username, password string) (string, bool) {
		return "bob@example.com", true
	})

	killCh := make(chan string, 1)
	serverErrCh := make(chan error, 1)
	finish := make(chan struct{})

	go func() {
		serverConn, err := ln.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer serverConn.Close()
		r := bufio.NewReader(serverConn)

		for range 2 {
			line, err := r.ReadString('\n')
			if err != nil {
				serverErrCh <- err
				return
			}
			fmt.Fprintf(serverConn, "SUCCESS: %s\r\n", strings.TrimSpace(line))
		}

		fmt.Fprint(serverConn, ">CLIENT:CONNECT,5,0\r\n>CLIENT:ENV,username=bob\r\n>CLIENT:ENV,password=x\r\n>CLIENT:ENV,END\r\n")
		// Drain the client-auth reply.
		if _, err := r.ReadString('\n'); err != nil {
			serverErrCh <- err
			return
		}
		if _, err := r.ReadString('\n'); err != nil {
			serverErrCh <- err
			return
		}

		// Now expect a client-kill command triggered by EnforceAuthorized.
		line, err := r.ReadString('\n')
		if err != nil {
			serverErrCh <- err
			return
		}
		killCh <- strings.TrimSpace(line)
		fmt.Fprint(serverConn, "SUCCESS: kill\r\n")

		<-finish // wait for the test to close mgmt before this end hangs up
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgmt.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	waitForCondition(t, time.Second, func() bool {
		mgmt.stateMu.Lock()
		defer mgmt.stateMu.Unlock()
		_, ok := mgmt.cidIdentity["5"]
		return ok
	})

	mgmt.EnforceAuthorized(func(username string) bool { return false })

	select {
	case err := <-serverErrCh:
		t.Fatalf("fake server error: %v", err)
	case got := <-killCh:
		if got != "client-kill 5" {
			t.Fatalf("expected \"client-kill 5\", got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client-kill command")
	}

	mgmt.Close()
	close(finish)
}
