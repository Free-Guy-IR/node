package mtproto

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"syscall"
	"time"
)

var errServeReturned = errors.New("its server stopped on its own")

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
	mu        sync.Mutex
	current   net.Listener
	addr      net.Addr
	closed    bool
	stopped   chan struct{}
	dead      bool
	lastError string
	deadSince time.Time
	attempts  int
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

func (s *supervisedListener) markDead(reason error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = reason.Error()
	if s.dead {
		return false
	}
	s.dead = true
	s.deadSince = time.Now()
	s.attempts = 0
	return true
}

func (s *supervisedListener) markRecovered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dead {
		return false
	}
	s.dead = false
	s.deadSince = time.Time{}
	return true
}

func (s *supervisedListener) countRelistenAttempt() {
	s.mu.Lock()
	s.attempts++
	s.mu.Unlock()
}

func (s *supervisedListener) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

func (s *supervisedListener) state(tag string) InstanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return InstanceState{Tag: tag, Dead: s.dead, LastError: s.lastError, DeadSince: s.deadSince, RelistenAttempts: s.attempts}
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

func (pi *proxyInstance) serveOnce(listener net.Listener) (err error) {
	defer func() {
		if value := recover(); value != nil {
			reportPanic(fmt.Sprintf("the serve loop of instance %q, which is listening and serving again", pi.tag), value, pi.record)
			err = fmt.Errorf("serve panicked: %v", value)
		}
	}()
	if pi.mp != nil {
		return pi.mp.Serve(listener) //nolint: wrapcheck
	}
	return pi.proxy.Serve(listener) //nolint: wrapcheck
}

func (pi *proxyInstance) reportDead(handle *supervisedListener, reason error) {
	if handle.markDead(reason) {
		pi.note(fmt.Sprintf("mtproto instance %q listener dead: %v", pi.tag, reason))
	}
}

func (pi *proxyInstance) supervise(handle *supervisedListener, served net.Listener, done chan struct{}) {
	defer close(done)

	var delay time.Duration
	for {
		began := time.Now()
		err := pi.serveOnce(served)
		if handle.stopping() {
			return
		}
		if err == nil {
			pi.reportDead(handle, errServeReturned)
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
			pi.reportDead(handle, err)
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
			handle.countRelistenAttempt()
			relistenErr := handle.relisten()
			if relistenErr == nil {
				if handle.markRecovered() {
					pi.note(fmt.Sprintf("mtproto instance %q listener recovered", pi.tag))
				}
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
	return ok && handle.isDead()
}

type InstanceState struct {
	Tag              string
	Dead             bool
	LastError        string
	DeadSince        time.Time
	RelistenAttempts int
}

func (b *Backend) InstanceStates() []InstanceState {
	b.mu.RLock()
	defer b.mu.RUnlock()

	states := make([]InstanceState, 0, len(b.instances))
	for tag, inst := range b.instances {
		if handle, ok := inst.listener.(*supervisedListener); ok {
			states = append(states, handle.state(tag))
			continue
		}
		states = append(states, InstanceState{Tag: tag, Dead: inst.listener == nil})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Tag < states[j].Tag })
	return states
}
