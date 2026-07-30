package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// logRing is a small fixed-size circular buffer of recent log lines, used to
// give context (e.g. in an error returned to the caller) when startup fails.
// Mirrors backend/singbox/log.go's logRing exactly (kept as an independent
// copy rather than a shared import - this repo does not share internal
// helper packages between backend implementations; xray and singbox each
// carry their own near-identical process/log helpers too).
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

func (p *instanceProcess) setStartupFailure(msg string) {
	p.startupFailureMu.Lock()
	defer p.startupFailureMu.Unlock()
	p.startupFailure = msg
}

func (p *instanceProcess) LatestStartupFailure() string {
	p.startupFailureMu.RLock()
	defer p.startupFailureMu.RUnlock()
	return p.startupFailure
}

func (p *instanceProcess) StartupLogTail(n int) []string {
	return p.logs.tail(n)
}

func (p *instanceProcess) captureProcessLogs(ctx context.Context, pipe io.Reader) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			p.recordLog(scanner.Text())
		}
	}
}

func (p *instanceProcess) recordLog(line string) {
	p.logs.add(line)

	// Non-blocking send: skip if the shared backend log channel is full,
	// rather than blocking (and stalling) this instance's stdout/stderr pipe.
	select {
	case p.logsChan <- fmt.Sprintf("[%s] %s", p.tag, line):
	default:
	}

	if isStartupFailureLog(line) {
		p.setStartupFailure(line)
	}
}

// isStartupFailureLog is a best-effort heuristic for detecting fatal openvpn
// startup errors from its log output, analogous to backend/singbox/log.go's
// isStartupFailureLog and backend/xray/startup_diagnostics.go's.
func isStartupFailureLog(line string) bool {
	lower := strings.ToLower(line)

	keywords := [...]string{
		"options error",
		"exiting due to fatal error",
		"cannot resolve host address",
		"failed to start",
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
