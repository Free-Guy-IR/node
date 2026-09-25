package mtproto

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib"
)

type capturedLines struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLines) record(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
}

func (c *capturedLines) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppedClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestAllowlistCheckThatPanicsStillAdmitsEveryone(t *testing.T) {
	previous := beforeAllowlistCheck
	beforeAllowlistCheck = func(net.IP) { panic("allowlist check exploded") }
	t.Cleanup(func() { beforeAllowlistCheck = previous })

	captured := &capturedLines{}
	allowlist := allowEveryone{panics: newPanicGuard("the allowlist", captured.record)}

	var admitted bool
	survives(t, "allowEveryone.Contains with a panicking check", func() {
		admitted = allowlist.Contains(net.IPv4(127, 0, 0, 1))
	})
	if !admitted {
		t.Fatal("allowEveryone admits everyone, so a recovered panic must still admit")
	}
	expectLogLine(t, captured.snapshot(), "a recovered allowlist panic must be logged", "PANIC", "the allowlist", "allowlist check exploded")
}

func TestAllowlistStillAdmitsWithoutAPanic(t *testing.T) {
	if !(allowEveryone{}).Contains(net.IPv4(127, 0, 0, 1)) {
		t.Fatal("allowEveryone must admit everyone")
	}
}

func TestCallbackPanicsAreReportedLoudlyButRateLimited(t *testing.T) {
	captured := &capturedLines{}
	clock := &steppedClock{now: time.Unix(1_800_000_000, 0)}
	var outRx, outTx, inRx, inTx atomic.Int64
	stream := newMtprotoEventStream(newEventAccumulator(&outRx, &outTx, &inRx, &inTx), func(string) string { panic("email lookup exploded") })
	stream.panics = newPanicGuard("the event stream of instance \"mtproto-flood\"", captured.record)
	stream.panics.now = clock.read

	const workers, perWorker = 8, 200
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range perWorker {
				stream.Send(context.Background(), mtglib.NewEventAuthenticated(fmt.Sprintf("s-%d-%d", w, i), aliceEmail))
			}
		})
	}
	survives(t, "a flood of panicking Sends", wg.Wait)

	lines := captured.snapshot()
	if len(lines) != 1 {
		t.Fatalf("a flood of %d panics inside one report interval must be reported once, got %d reports", workers*perWorker, len(lines))
	}
	expectLogLine(t, lines, "the first report must be loud and carry the stack", "PANIC", "mtproto-flood", "email lookup exploded", "goroutine ")

	clock.advance(panicReportInterval + time.Second)
	survives(t, "a panicking Send after the report interval", func() {
		stream.Send(context.Background(), mtglib.NewEventAuthenticated("after", aliceEmail))
	})
	lines = captured.snapshot()
	if len(lines) != 2 {
		t.Fatalf("the next panic after the interval must be reported, got %d reports", len(lines))
	}
	expectLogLine(t, lines[1:], "the next report must count what was suppressed", "PANIC", fmt.Sprintf("%d more", workers*perWorker-1))
}

func TestAServeLoopPanicIsReportedLoudly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	captured := &capturedLines{}
	pi := &proxyInstance{tag: backstopTag, proxy: &mtglib.Proxy{}, record: captured.record}
	pi.serve(listener)
	if err := dialAndDrop(t, listener.Addr().String()); err != nil {
		t.Fatalf("dial the serving listener: %v", err)
	}
	if !pi.awaitServeExit() {
		t.Fatal("a serve loop that panicked must count as exited")
	}
	expectLogLine(t, captured.snapshot(), "a recovered serve panic must be logged with its stack", "PANIC", backstopTag, "(*Proxy).Serve")
}

func TestMtgLoggerPanicsAreReportedThroughTheBackendLog(t *testing.T) {
	captured := &capturedLines{}
	logger := newMtgLogger("mtproto.backstop", captured.record)
	var typedNil *nilMessageError

	survives(t, "mtgLogger.WarningError with a typed-nil error", func() { logger.WarningError("warning", typedNil) })

	lines := captured.snapshot()
	expectLogLine(t, lines, "a recovered logger panic must be logged", "PANIC", "mtproto.backstop")
	for _, line := range lines {
		if strings.HasPrefix(line, "[mtproto.backstop] warning") {
			t.Fatalf("a log call that panicked must not emit a partial line, got %q", line)
		}
	}
}
