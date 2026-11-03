package mtproto

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib"
)

func TestNodeVerifyAServeLoopThatPanickedIsReportedDeadNotHealthy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	address := listener.Addr().String()

	var linesMu sync.Mutex
	var lines []string
	record := func(line string) {
		linesMu.Lock()
		lines = append(lines, line)
		linesMu.Unlock()
	}
	pi := &proxyInstance{tag: backstopTag, proxy: &mtglib.Proxy{}, record: record}
	b := &Backend{instances: map[string]*proxyInstance{backstopTag: pi}, logsChan: make(chan string, 64)}
	pi.serve(listener)
	t.Cleanup(func() {
		pi.listener.Close()
		pi.awaitServeExit()
	})

	_ = dialAndDrop(t, address)

	state, reported := awaitInstanceState(b, backstopTag, 5*time.Second, func(s InstanceState) bool {
		return strings.Contains(s.LastError, "panic") && (s.Dead || s.RelistenAttempts >= 1)
	})
	if !reported {
		t.Fatalf("a serve loop that panicked is not reported through InstanceStates: %+v", state)
	}

	linesMu.Lock()
	snapshot := append([]string(nil), lines...)
	linesMu.Unlock()
	if countLinesContaining(snapshot, `listener dead: `) == 0 {
		t.Fatalf("a serve loop that panicked left no listener dead line: %q", snapshot)
	}

	for deadline := time.Now().Add(5 * time.Second); ; {
		current, _ := instanceStateOf(b, backstopTag)
		if current.Dead {
			break
		}
		if dialAndDrop(t, address) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the listener of a panicked serve loop is closed while InstanceStates reports it healthy: %+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
