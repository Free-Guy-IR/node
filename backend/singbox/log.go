package singbox

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
)

// logRing is a small fixed-size circular buffer of recent log lines, used to
// give context (e.g. in an error returned to the caller) when startup fails or
// health checks trip a restart. This replaces xray's separate
// startup-phase/runtime-phase log ring pair (startup_diagnostics.go) with a
// single always-on ring - sing-box's v1 scope doesn't need the phase-switching
// complexity xray uses to avoid overhead during steady-state runtime.
type logRing struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

func newLogRing(size int) *logRing {
	if size <= 0 {
		size = 1
	}
	return &logRing{lines: make([]string, size)}
}

func (r *logRing) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next = 0
	r.full = false
	for i := range r.lines {
		r.lines[i] = ""
	}
}

func (r *logRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 {
		return
	}
	r.lines[r.next] = line
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
}

func (r *logRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := r.next
	if r.full {
		count = len(r.lines)
	}
	if count == 0 {
		return nil
	}
	if n <= 0 || n > count {
		n = count
	}

	oldest := 0
	if r.full {
		oldest = r.next
	}

	start := count - n
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		idx := (oldest + start + i) % len(r.lines)
		out = append(out, r.lines[idx])
	}
	return out
}

func (c *Core) setStartupFailure(msg string) {
	c.startupFailureM.Lock()
	defer c.startupFailureM.Unlock()
	c.startupFailure = msg
}

func (c *Core) LatestStartupFailure() string {
	c.startupFailureM.RLock()
	defer c.startupFailureM.RUnlock()
	return c.startupFailure
}

func (c *Core) StartupLogTail(n int) []string {
	return c.logs.tail(n)
}

func (c *Core) captureProcessLogs(ctx context.Context, pipe io.Reader) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			c.recordLog(scanner.Text())
		}
	}
}

func (c *Core) recordLog(line string) {
	c.logs.add(line)

	// Non-blocking send: skip if channel is full to prevent deadlock/blocking the
	// sing-box process's stdout/stderr pipe.
	select {
	case c.logsChan <- line:
	default:
	}

	if isStartupFailureLog(line) {
		c.setStartupFailure(line)
	}
}

// isStartupFailureLog is a best-effort heuristic for detecting fatal sing-box
// startup errors from its log output, analogous to xray's
// startup_diagnostics.go isStartupFailureLog. Calibrated against real output
// captured on the dev box: sing-box prefixes fatal lines with "FATAL[" (caught
// by the "fatal" substring check below), e.g.
//
//	FATAL[0000] decode config at ...: inbounds[0].listen_port: json: cannot unmarshal number 99999 into Go struct field ...
//	FATAL[0000] start service: start inbound/hysteria2[hy2-in]: listen udp 0.0.0.0:18443: bind: address already in use
func isStartupFailureLog(line string) bool {
	lower := strings.ToLower(line)

	keywords := [...]string{
		"fatal",
		"panic",
		"failed to start",
		"start service",
		"permission denied",
		"no such file or directory",
		"cannot find",
		"failed to open",
		"failed to load",
		"failed to listen",
		"address already in use",
	}

	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}

	return false
}
