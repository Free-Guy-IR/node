package mtproto

import (
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/backend/mtproto/middleproxy"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

type relayStep struct {
	up, down uint
	finish   bool
}

type lateRelay struct {
	steps   chan relayStep
	written chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (r *lateRelay) write(t *testing.T, up, down uint) {
	t.Helper()
	r.steps <- relayStep{up: up, down: down}
	select {
	case <-r.written:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never wrote its late bytes")
	}
}

func (r *lateRelay) finish(t *testing.T) {
	t.Helper()
	r.once.Do(func() { r.steps <- relayStep{finish: true} })
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never finished")
	}
}

func shortDrainTimeout(t *testing.T) {
	t.Helper()
	previous := serveExitTimeout
	serveExitTimeout = 100 * time.Millisecond
	t.Cleanup(func() { serveExitTimeout = previous })
}

func startStuckMiddleProxy(t *testing.T, tag string, earlyUp uint) (*Backend, *lateRelay) {
	t.Helper()

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	b := &Backend{
		instances:       map[string]*proxyInstance{},
		order:           []string{tag},
		inboundCounters: map[string]*inboundCounter{tag: {}},
		statsTracker:    stats.New(),
		logsChan:        make(chan string, 64),
		secretsByID:     map[string]secretEntry{aliceEmail: {email: aliceEmail}},
	}
	counter := b.inboundCounters[tag]
	accumulator := newEventAccumulator(&b.outboundRx, &b.outboundTx, &counter.rx, &counter.tx)
	pi := &proxyInstance{
		tag:         tag,
		mp:          middleproxy.New(middleproxy.Options{}),
		accumulator: accumulator,
		streams:     newStreamSet(),
	}
	b.instances[tag] = pi

	listener := pi.streams.track(raw)
	t.Cleanup(func() { listener.Close() })
	serveDone := make(chan struct{})
	pi.listener = listener
	pi.serveDone = serveDone

	relay := &lateRelay{
		steps:   make(chan relayStep),
		written: make(chan struct{}),
		done:    make(chan struct{}),
	}
	accepted := make(chan struct{})
	go func() {
		defer close(serveDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accumulator.authenticated("late-stream", aliceEmail, aliceEmail)
		accumulator.traffic("late-stream", earlyUp, false)
		close(accepted)
		go func() {
			defer close(relay.done)
			defer conn.Close()
			for step := range relay.steps {
				if step.finish {
					accumulator.finished("late-stream")
					return
				}
				if step.up > 0 {
					accumulator.traffic("late-stream", step.up, false)
				}
				if step.down > 0 {
					accumulator.traffic("late-stream", step.down, true)
				}
				relay.written <- struct{}{}
			}
		}()
		for {
			if _, err := listener.Accept(); err != nil {
				return
			}
		}
	}()

	client, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was never accepted")
	}
	t.Cleanup(func() {
		relay.once.Do(func() { close(relay.steps) })
		<-relay.done
	})
	return b, relay
}

func TestLateMiddleProxyBytesAreBilledOnceByTheNextBackend(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)

	first, relay := startStuckMiddleProxy(t, handoverTagA, 25)
	first.Shutdown()

	second := startHandover(t, []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}, handoverTagA)
	expectAllRead(t, "read before the late bytes", second,
		userTraffic(aliceEmail, handoverTagA, 25, 0),
		userTraffic(handoverTagA, handoverTagA, 25, 0),
		userTraffic(handoverNode, handoverNode, 25, 0))

	relay.write(t, 60, 4000)
	relay.finish(t)

	expectAllRead(t, "read after the late bytes", second,
		userTraffic(aliceEmail, handoverTagA, 60, 4000),
		userTraffic(handoverTagA, handoverTagA, 60, 4000),
		userTraffic(handoverNode, handoverNode, 60, 4000))
	expectNothingLeft(t, "read after the late bytes were billed", second)

	sendTraffic(t, second, handoverTagA, "fresh", aliceEmail, 3, 4)
	expectAllRead(t, "read after new growth", second,
		userTraffic(aliceEmail, handoverTagA, 3, 4),
		userTraffic(handoverTagA, handoverTagA, 3, 4),
		userTraffic(handoverNode, handoverNode, 3, 4))
}

func TestLateMiddleProxyBytesWrittenBeforeTheFirstReadAreBilledOnce(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)

	first, relay := startStuckMiddleProxy(t, handoverTagA, 25)
	first.Shutdown()
	relay.write(t, 5, 500)

	second := startHandover(t, []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}, handoverTagA)
	relay.write(t, 1, 100)

	expectAllRead(t, "first read", second,
		userTraffic(aliceEmail, handoverTagA, 31, 600),
		userTraffic(handoverTagA, handoverTagA, 31, 600),
		userTraffic(handoverNode, handoverNode, 31, 600))
	expectNothingLeft(t, "second read while the relay still runs", second)

	relay.finish(t)
	expectNothingLeft(t, "read after the relay finished without new bytes", second)
}

func TestLateMiddleProxyBytesSurviveTwoStarts(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)
	users := []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}

	first, relay := startStuckMiddleProxy(t, handoverTagA, 25)
	first.Shutdown()

	second := startHandover(t, users, handoverTagA)
	relay.write(t, 10, 100)
	second.Shutdown()

	third := startHandover(t, users, handoverTagA)
	relay.write(t, 5, 50)
	relay.finish(t)

	expectAllRead(t, "first read on the third backend", third,
		userTraffic(aliceEmail, handoverTagA, 40, 150),
		userTraffic(handoverTagA, handoverTagA, 40, 150),
		userTraffic(handoverNode, handoverNode, 40, 150))
	expectNothingLeft(t, "second read on the third backend", third)
}

func TestLateMiddleProxyBytesSurviveTwoStartsWithAReadBetween(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)
	users := []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}

	first, relay := startStuckMiddleProxy(t, handoverTagA, 25)
	first.Shutdown()

	second := startHandover(t, users, handoverTagA)
	relay.write(t, 10, 100)
	expectAllRead(t, "read on the second backend", second,
		userTraffic(aliceEmail, handoverTagA, 35, 100),
		userTraffic(handoverTagA, handoverTagA, 35, 100),
		userTraffic(handoverNode, handoverNode, 35, 100))
	relay.write(t, 2, 20)
	second.Shutdown()

	third := startHandover(t, users, handoverTagA)
	relay.write(t, 5, 50)
	relay.finish(t)

	expectAllRead(t, "first read on the third backend", third,
		userTraffic(aliceEmail, handoverTagA, 7, 70),
		userTraffic(handoverTagA, handoverTagA, 7, 70),
		userTraffic(handoverNode, handoverNode, 7, 70))
	expectNothingLeft(t, "second read on the third backend", third)
}

func TestLateMiddleProxyBytesOfAGoneTagAreReportedOnceThenForgotten(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)

	first, relay := startStuckMiddleProxy(t, handoverTagA, 25)
	first.Shutdown()

	second := startHandover(t, []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagB)}, handoverTagB)
	expectAllRead(t, "read before the late bytes", second,
		userTraffic(aliceEmail, handoverTagA, 25, 0),
		userTraffic(handoverTagA, handoverTagA, 25, 0),
		userTraffic(handoverNode, handoverNode, 25, 0))

	relay.write(t, 60, 4000)
	relay.finish(t)

	expectAllRead(t, "read after the late bytes", second,
		userTraffic(aliceEmail, handoverTagA, 60, 4000),
		userTraffic(handoverTagA, handoverTagA, 60, 4000),
		userTraffic(handoverNode, handoverNode, 60, 4000))
	expectNothingLeft(t, "read after the late bytes were billed", second)

	keys := second.statsTracker.Keys()
	sort.Strings(keys)
	for _, key := range keys {
		if strings.HasPrefix(key, handoverTagA+"|") {
			t.Fatalf("the gone tag must be forgotten once its late bytes were reported, tracker keys: %v", keys)
		}
	}
}
