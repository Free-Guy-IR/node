package mtproto

import (
	"strings"
	"testing"
	"time"
)

func TestShutdownReportsAServeThatDidNotStop(t *testing.T) {
	previous := serveExitTimeout
	serveExitTimeout = 50 * time.Millisecond
	defer func() { serveExitTimeout = previous }()

	b := &Backend{
		instances: map[string]*proxyInstance{
			"mtproto-stuck": {tag: "mtproto-stuck", serveDone: make(chan struct{})},
		},
		logsChan: make(chan string, 16),
	}

	done := make(chan struct{})
	go func() {
		b.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown must not wait for a stuck Serve beyond the exit timeout")
	}

	var lines []string
	for {
		select {
		case line := <-b.logsChan:
			lines = append(lines, line)
			continue
		default:
		}
		break
	}
	for _, line := range lines {
		if strings.Contains(line, "mtproto-stuck") && strings.Contains(line, "did not stop") {
			return
		}
	}
	t.Fatalf("a Serve that outlives the exit timeout must be logged, got %q", lines)
}
