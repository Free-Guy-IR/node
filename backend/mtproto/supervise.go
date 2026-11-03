package mtproto

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	serveRetryFloor   = 5 * time.Millisecond
	serveRetryCeiling = time.Second
	serveHealthyAfter = 5 * time.Second
)

var transientAcceptErrnos = []syscall.Errno{
	syscall.EMFILE,
	syscall.ENFILE,
	syscall.ENOBUFS,
	syscall.ENOMEM,
	syscall.ECONNABORTED,
	syscall.ECONNRESET,
	syscall.EINTR,
	syscall.EPROTO,
	syscall.ENETDOWN,
	syscall.ENETUNREACH,
	syscall.EHOSTDOWN,
	syscall.EHOSTUNREACH,
}

func acceptErrorIsTransient(err error) bool {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	for _, errno := range transientAcceptErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

func nextServeRetry(previous time.Duration) time.Duration {
	if previous <= 0 {
		return serveRetryFloor
	}
	return min(previous*2, serveRetryCeiling)
}

type supervisedListener struct {
	mu      sync.Mutex
	current net.Listener
	addr    net.Addr
	closed  bool
	stopped chan struct{}
	dead    atomic.Bool
}

func newSupervisedListener(listener net.Listener) *supervisedListener {
	return &supervisedListener{current: listener, addr: listener.Addr(), stopped: make(chan struct{})}
}

func (s *supervisedListener) active() net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *supervisedListener) Accept() (net.Conn, error) {
	return s.active().Accept()
}

func (s *supervisedListener) Addr() net.Addr {
	return s.addr
}

func (s *supervisedListener) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stopped)
	current := s.current
	s.mu.Unlock()
	return current.Close()
}

func (s *supervisedListener) stopping() bool {
	select {
	case <-s.stopped:
		return true
	default:
		return false
	}
}

func (s *supervisedListener) pause(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.stopped:
		return false
	case <-timer.C:
		return true
	}
}

func (s *supervisedListener) relisten() error {
	tcpAddr, ok := s.addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("cannot listen again on a %s address", s.addr.Network())
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return net.ErrClosed
	}
	old := s.current
	s.mu.Unlock()
	_ = old.Close()

	next, err := net.ListenTCP("tcp", &net.TCPAddr{Port: tcpAddr.Port})
	if err != nil {
		return err //nolint: wrapcheck
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = next.Close()
		return net.ErrClosed
	}
	s.current = next
	return nil
}

func (pi *proxyInstance) note(line string) {
	if pi.record != nil {
		pi.record(line)
	}
}

func (pi *proxyInstance) serveOnce(listener net.Listener) error {
	if pi.mp != nil {
		return pi.mp.Serve(listener) //nolint: wrapcheck
	}
	return pi.proxy.Serve(listener) //nolint: wrapcheck
}

func (pi *proxyInstance) supervise(handle *supervisedListener, served net.Listener, done chan struct{}) {
	defer close(done)
	defer pi.absorbServePanic(handle)

	var delay time.Duration
	for {
		began := time.Now()
		err := pi.serveOnce(served)
		if handle.stopping() {
			return
		}
		if err == nil {
			handle.dead.Store(true)
			_ = handle.Close()
			pi.note(fmt.Sprintf("mtproto instance %q: its server stopped on its own, the instance is down until a restart", pi.tag))
			return
		}
		if time.Since(began) >= serveHealthyAfter {
			delay = 0
		}
		if delay == 0 {
			pi.note(fmt.Sprintf("mtproto instance %q: accepting failed (%v), serving again with a growing pause", pi.tag, err))
		}

		replace := !acceptErrorIsTransient(err)
		if replace {
			handle.dead.Store(true)
		}
		reported := false
		for {
			delay = nextServeRetry(delay)
			if !handle.pause(delay) {
				return
			}
			if !replace {
				break
			}
			relistenErr := handle.relisten()
			if relistenErr == nil {
				handle.dead.Store(false)
				pi.note(fmt.Sprintf("mtproto instance %q: listening again after its listener failed", pi.tag))
				break
			}
			if errors.Is(relistenErr, net.ErrClosed) {
				return
			}
			if !reported {
				reported = true
				pi.note(fmt.Sprintf("mtproto instance %q: is down, cannot listen again: %v", pi.tag, relistenErr))
			}
		}
	}
}

func (pi *proxyInstance) listenerDead() bool {
	handle, ok := pi.listener.(*supervisedListener)
	return ok && handle.dead.Load()
}
