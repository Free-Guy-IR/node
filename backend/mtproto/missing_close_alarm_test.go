package mtproto

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/9seconds/mtg/v2/mtglib"
)

const missingCloseAlarmText = "stream context has no Close, revoked sessions cannot be cut"

type lockedLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *lockedLines) record(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *lockedLines) Write(p []byte) (int, error) {
	l.record(string(p))
	return len(p), nil
}

func (l *lockedLines) count(substrs ...string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		matched := true
		for _, substr := range substrs {
			matched = matched && strings.Contains(line, substr)
		}
		if matched {
			n++
		}
	}
	return n
}

type closableStreamContext struct {
	context.Context
	closed atomic.Int32
}

func (c *closableStreamContext) Close() {
	c.closed.Add(1)
}

func driveAuthenticatedStreams(stream *mtprotoEventStream, prefix string, ctxFor func() context.Context, n int) {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%d", prefix, i)
		ctx := ctxFor()
		stream.Send(ctx, mtglib.NewEventStart(id, net.IPv4(192, 0, 2, 9)))
		stream.Send(ctx, mtglib.NewEventAuthenticated(id, "alice"))
		stream.Send(ctx, mtglib.NewEventFinish(id))
	}
}

func newAlarmTestStream(tag string, recorded *lockedLines) *mtprotoEventStream {
	var outRx, outTx, inRx, inTx atomic.Int64
	stream := newMtprotoEventStream(newEventAccumulator(&outRx, &outTx, &inRx, &inTx), func(string) string { return "alice@example.com" })
	stream.panics = newPanicGuard(fmt.Sprintf("the event stream of instance %q", tag), recorded.record)
	return stream
}

func TestAStreamContextWithoutCloseRaisesOneAlarmPerInstance(t *testing.T) {
	logged := &lockedLines{}
	previous := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	firstRecorded := &lockedLines{}
	first := newAlarmTestStream("mtproto-noclose-a", firstRecorded)
	driveAuthenticatedStreams(first, "noclose-a", context.Background, 5)

	secondRecorded := &lockedLines{}
	second := newAlarmTestStream("mtproto-noclose-b", secondRecorded)
	driveAuthenticatedStreams(second, "noclose-b", context.Background, 3)

	closableRecorded := &lockedLines{}
	closable := newAlarmTestStream("mtproto-closable", closableRecorded)
	driveAuthenticatedStreams(closable, "closable", func() context.Context {
		return &closableStreamContext{Context: context.Background()}
	}, 4)

	if got := firstRecorded.count(missingCloseAlarmText); got != 1 {
		t.Fatalf("instance a recorded the missing-Close alarm %d times over 5 streams, want exactly 1; lines: %q", got, firstRecorded.lines)
	}
	if got := secondRecorded.count(missingCloseAlarmText); got != 1 {
		t.Fatalf("instance b recorded the missing-Close alarm %d times over 3 streams, want exactly 1; lines: %q", got, secondRecorded.lines)
	}
	if got := closableRecorded.count(missingCloseAlarmText); got != 0 {
		t.Fatalf("an instance whose stream contexts expose Close recorded the alarm %d times, want 0", got)
	}
	for _, tag := range []string{`"mtproto-noclose-a"`, `"mtproto-noclose-b"`} {
		if got := logged.count(tag, missingCloseAlarmText); got != 1 {
			t.Fatalf("the process log carries the missing-Close alarm for instance %s %d times, want 1", tag, got)
		}
	}
	if got := logged.count(`"mtproto-closable"`, missingCloseAlarmText); got != 0 {
		t.Fatalf("the process log carries the missing-Close alarm for the closable instance %d times, want 0", got)
	}
	if got := firstRecorded.count(`"mtproto-noclose-a"`, missingCloseAlarmText); got != 1 {
		t.Fatalf("instance a's recorded alarm does not name its instance; lines: %q", firstRecorded.lines)
	}
}
