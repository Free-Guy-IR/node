package wireguard

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

const (
	addressReadyPollInterval = 50 * time.Millisecond
	addressReadyDeadline     = 5 * time.Second
)

func listenWhenAddressReady(listen func() (net.Listener, error), deadline time.Duration) (net.Listener, error) {
	giveUp := time.Now().Add(deadline)
	for {
		listener, err := listen()
		if err == nil || !errors.Is(err, syscall.EADDRNOTAVAIL) {
			return listener, err
		}
		remaining := time.Until(giveUp)
		if remaining <= 0 {
			return nil, fmt.Errorf("address still unavailable after %s: %w", deadline, err)
		}
		time.Sleep(min(addressReadyPollInterval, remaining))
	}
}

func listenFailure(errno syscall.Errno) error {
	return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)}
}

func TestListenWhenAddressReadyRetriesWhileTheAddressIsTentative(t *testing.T) {
	const tentativeAttempts = 3
	calls := 0
	listener, err := listenWhenAddressReady(func() (net.Listener, error) {
		calls++
		if calls <= tentativeAttempts {
			return nil, listenFailure(syscall.EADDRNOTAVAIL)
		}
		return net.Listen("tcp", "127.0.0.1:0")
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("expected a listener once the address became ready, got %v", err)
	}
	defer listener.Close()
	if listener == nil {
		t.Fatal("expected a listener, got nil")
	}
	if calls != tentativeAttempts+1 {
		t.Fatalf("expected %d listen attempts, got %d", tentativeAttempts+1, calls)
	}
}

func TestListenWhenAddressReadyFailsAtOnceOnOtherErrors(t *testing.T) {
	calls := 0
	started := time.Now()
	listener, err := listenWhenAddressReady(func() (net.Listener, error) {
		calls++
		return nil, listenFailure(syscall.EADDRINUSE)
	}, 5*time.Second)
	elapsed := time.Since(started)
	if listener != nil {
		_ = listener.Close()
		t.Fatal("expected no listener on a non-retryable error")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("expected EADDRINUSE to be returned, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one listen attempt on a non-retryable error, got %d", calls)
	}
	if elapsed >= addressReadyPollInterval {
		t.Fatalf("expected an immediate return on a non-retryable error, took %s", elapsed)
	}
}

func TestListenWhenAddressReadyGivesUpAfterTheDeadline(t *testing.T) {
	const deadline = 200 * time.Millisecond
	calls := 0
	started := time.Now()
	listener, err := listenWhenAddressReady(func() (net.Listener, error) {
		calls++
		return nil, listenFailure(syscall.EADDRNOTAVAIL)
	}, deadline)
	elapsed := time.Since(started)
	if listener != nil {
		_ = listener.Close()
		t.Fatal("expected no listener while the address never became ready")
	}
	if !errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Fatalf("expected the last EADDRNOTAVAIL error after the deadline, got %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected the listen to be retried before giving up, got %d attempts", calls)
	}
	if elapsed < deadline {
		t.Fatalf("gave up after %s, before the %s deadline", elapsed, deadline)
	}
	if elapsed > deadline+2*time.Second {
		t.Fatalf("kept retrying for %s, far past the %s deadline", elapsed, deadline)
	}
}
