package mtproto

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/antireplay"
	"github.com/9seconds/mtg/v2/ipblocklist"
	"github.com/9seconds/mtg/v2/mtglib"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

const (
	backstopTag  = "mtproto-backstop"
	callbackWait = 5 * time.Second
)

func backstopBackend(pi *proxyInstance) *Backend {
	b := &Backend{
		instances:       map[string]*proxyInstance{pi.tag: pi},
		order:           []string{pi.tag},
		inboundCounters: map[string]*inboundCounter{pi.tag: {}},
		statsTracker:    stats.New(),
		logsChan:        make(chan string, 256),
		secretsByID:     map[string]secretEntry{aliceEmail: {email: aliceEmail}},
	}
	counter := b.inboundCounters[pi.tag]
	pi.accumulator = newEventAccumulator(&b.outboundRx, &b.outboundTx, &counter.rx, &counter.tx)
	return b
}

func shutdownWithin(t *testing.T, b *Backend, limit time.Duration, onTimeout func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Shutdown()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		if onTimeout != nil {
			onTimeout()
		}
		t.Fatalf("Backend.Shutdown did not return within %s", limit)
	}
	if !b.lifecycle.TryLock() {
		t.Fatal("Backend.Shutdown returned but kept the lifecycle lock")
	}
	b.lifecycle.Unlock()
}

func drainedLogs(b *Backend) []string {
	var lines []string
	for {
		select {
		case line := <-b.logsChan:
			lines = append(lines, line)
		default:
			return lines
		}
	}
}

func expectLogLine(t *testing.T, lines []string, what string, parts ...string) {
	t.Helper()
	for _, line := range lines {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("%s: no log line holds all of %q, got %q", what, parts, lines)
}

func survives(t *testing.T, what string, call func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		call()
	}()
	select {
	case <-done:
	case <-time.After(callbackWait):
		t.Fatalf("%s did not return", what)
	}
}

func dialAndDrop(t *testing.T, address string) error {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.SetLinger(0)
	}
	return conn.Close()
}

func hiddenlyServingProxy(t *testing.T) (*mtglib.Proxy, func()) {
	t.Helper()
	ntw, err := buildNetwork()
	if err != nil {
		t.Fatalf("buildNetwork: %v", err)
	}
	started := make(chan struct{}, 1)
	logger := newMtgLogger("hidden", func(line string) {
		if strings.Contains(line, "Stream has been started") {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})
	accumulator := newEventAccumulator(new(atomic.Int64), new(atomic.Int64), new(atomic.Int64), new(atomic.Int64))
	proxy, err := mtglib.NewProxy(mtglib.ProxyOpts{
		Logger:          logger,
		Network:         ntw,
		AntiReplayCache: antireplay.NewNoop(),
		IPBlocklist:     ipblocklist.NewNoop(),
		IPAllowlist:     allowEveryone{},
		EventStream:     newMtprotoEventStream(accumulator, func(string) string { return "" }),
		Secrets:         map[string]mtglib.Secret{},
		PlainMode:       true,
	})
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = proxy.Serve(listener)
	}()

	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial the hidden listener: %v", err)
	}
	client.(*net.TCPConn).SetLinger(0)
	select {
	case <-started:
	case <-time.After(callbackWait):
		t.Fatal("the hidden proxy never started a stream")
	}

	release := sync.OnceFunc(func() {
		client.Close()
		listener.Close()
		<-served
	})
	t.Cleanup(release)
	return proxy, release
}

func TestAProxyShutdownThatPanicsStillHandsTheAccountingOver(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)

	pi := &proxyInstance{tag: backstopTag, proxy: &mtglib.Proxy{}}
	first := backstopBackend(pi)
	pi.accumulator.authenticated("s1", aliceEmail, aliceEmail)
	pi.accumulator.traffic("s1", 40, false)
	pi.accumulator.traffic("s1", 700, true)
	pi.accumulator.finished("s1")

	shutdownWithin(t, first, 10*time.Second, nil)

	lines := drainedLogs(first)
	expectLogLine(t, lines, "a recovered proxy shutdown panic must be logged with its stack", "PANIC", backstopTag, "(*Proxy).Shutdown")
	expectLogLine(t, lines, "the shutdown must complete after the panic", "mtproto shutdown complete")
	if _, err := first.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat}); err == nil {
		t.Fatal("a backend whose proxy shutdown panicked must still hand its accounting over")
	}

	second := startHandover(t, []*common.User{credentialedUser(aliceEmail, initialSecretA, backstopTag)}, backstopTag)
	expectAllRead(t, "the next backend after a panicked proxy shutdown", second,
		userTraffic(aliceEmail, backstopTag, 40, 700),
		userTraffic(backstopTag, backstopTag, 40, 700),
		userTraffic(handoverNode, handoverNode, 40, 700))
	expectNothingLeft(t, "a second read after the panicked shutdown", second)
}

func TestLateBytesOfAProxyWhoseShutdownPanickedAreBilledByTheNextBackend(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)

	pi := &proxyInstance{tag: backstopTag, proxy: &mtglib.Proxy{}}
	first := backstopBackend(pi)
	accumulator := pi.accumulator
	accumulator.authenticated("still-open", aliceEmail, aliceEmail)
	accumulator.traffic("still-open", 5, false)

	shutdownWithin(t, first, 10*time.Second, nil)

	accumulator.traffic("still-open", 11, false)
	accumulator.traffic("still-open", 900, true)
	accumulator.finished("still-open")

	second := startHandover(t, []*common.User{credentialedUser(aliceEmail, initialSecretA, backstopTag)}, backstopTag)
	expectAllRead(t, "bytes a stream relayed after its proxy shutdown panicked", second,
		userTraffic(aliceEmail, backstopTag, 16, 900),
		userTraffic(backstopTag, backstopTag, 16, 900),
		userTraffic(handoverNode, handoverNode, 16, 900))
	expectNothingLeft(t, "a second read after the late bytes", second)
}

func TestAProxyShutdownThatHangsIsAbandonedAfterTheExitTimeout(t *testing.T) {
	clearAccountingHandOver(t)
	shortDrainTimeout(t)

	proxy, release := hiddenlyServingProxy(t)
	pi := &proxyInstance{tag: backstopTag, proxy: proxy}
	first := backstopBackend(pi)
	pi.accumulator.authenticated("s1", aliceEmail, aliceEmail)
	pi.accumulator.traffic("s1", 3, false)
	pi.accumulator.traffic("s1", 30, true)
	pi.accumulator.finished("s1")

	shutdownWithin(t, first, callbackWait, release)

	expectLogLine(t, drainedLogs(first), "an abandoned proxy shutdown must be logged", backstopTag, "did not return")

	second := startHandover(t, []*common.User{credentialedUser(aliceEmail, initialSecretA, backstopTag)}, backstopTag)
	expectAllRead(t, "the next backend after an abandoned proxy shutdown", second,
		userTraffic(aliceEmail, backstopTag, 3, 30),
		userTraffic(backstopTag, backstopTag, 3, 30),
		userTraffic(handoverNode, handoverNode, 3, 30))

	release()
}

func TestAServeLoopThatPanicsCountsAsExitedAndStopsListening(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	address := listener.Addr().String()

	pi := &proxyInstance{tag: backstopTag, proxy: &mtglib.Proxy{}}
	pi.serve(listener)

	if err := dialAndDrop(t, address); err != nil {
		t.Fatalf("dial the serving listener: %v", err)
	}
	if !pi.awaitServeExit() {
		t.Fatal("a serve loop that panicked must count as exited")
	}
	if err := dialAndDrop(t, address); err == nil {
		t.Fatal("a serve loop that panicked must close its listener, not leave clients in a dead backlog")
	}
}

func TestEventStreamSurvivesAPanickingEmailLookup(t *testing.T) {
	var outRx, outTx, inRx, inTx atomic.Int64
	accumulator := newEventAccumulator(&outRx, &outTx, &inRx, &inTx)
	stream := newMtprotoEventStream(accumulator, func(string) string { panic("email lookup exploded") })

	survives(t, "Send of an authentication whose email lookup panics", func() {
		stream.Send(context.Background(), mtglib.NewEventAuthenticated("s1", aliceEmail))
	})
	if !accumulator.idle() {
		t.Fatal("an authentication whose email lookup panicked must be dropped whole")
	}

	survives(t, "Send of traffic after a recovered panic", func() {
		stream.Send(context.Background(), mtglib.NewEventTraffic("s1", 64, false))
	})
	if outTx.Load() != 64 || inTx.Load() != 64 {
		t.Fatalf("the stream must keep counting after a recovered panic, got outbound=%d inbound=%d", outTx.Load(), inTx.Load())
	}
}

func TestAPanicUnderTheAccumulatorLockDoesNotWedgeIt(t *testing.T) {
	var outRx, outTx, inRx, inTx atomic.Int64
	accumulator := newEventAccumulator(&outRx, &outTx, &inRx, &inTx)
	accumulator.streamUser = nil
	stream := newMtprotoEventStream(accumulator, func(string) string { return aliceEmail })

	survives(t, "Send of an authentication that panics under the accumulator lock", func() {
		stream.Send(context.Background(), mtglib.NewEventAuthenticated("s1", aliceEmail))
	})
	survives(t, "Send of traffic after a panic under the accumulator lock", func() {
		stream.Send(context.Background(), mtglib.NewEventTraffic("s1", 9, true))
	})
	survives(t, "Send of a finish after a panic under the accumulator lock", func() {
		stream.Send(context.Background(), mtglib.NewEventFinish("s1"))
	})
	if outRx.Load() != 9 || inRx.Load() != 9 {
		t.Fatalf("the accumulator must keep counting after a panic under its lock, got outbound=%d inbound=%d", outRx.Load(), inRx.Load())
	}
}

type nilMessageError struct{ message string }

func (e *nilMessageError) Error() string { return e.message }

func TestMtgLoggerSurvivesAPanickingSink(t *testing.T) {
	logger := newMtgLogger("mtproto.backstop", func(string) { panic("sink exploded") })
	boom := &nilMessageError{message: "boom"}

	for name, call := range map[string]func(){
		"Printf":       func() { logger.Printf("%s has been finished", "relay") },
		"Info":         func() { logger.Info("info") },
		"InfoError":    func() { logger.InfoError("info", boom) },
		"Warning":      func() { logger.Warning("warning") },
		"WarningError": func() { logger.WarningError("warning", boom) },
		"Debug":        func() { logger.Debug("debug") },
		"DebugError":   func() { logger.DebugError("debug", boom) },
	} {
		survives(t, "mtgLogger."+name+" with a panicking sink", call)
	}
}

func TestMtgLoggerSurvivesAnErrorWhoseMessagePanics(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logger := newMtgLogger("mtproto.backstop", func(line string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, line)
	})
	var typedNil *nilMessageError

	survives(t, "mtgLogger.InfoError with a typed-nil error", func() { logger.InfoError("info", typedNil) })
	survives(t, "mtgLogger.WarningError with a typed-nil error", func() { logger.WarningError("warning", typedNil) })
	survives(t, "mtgLogger.DebugError with a typed-nil error", func() { logger.DebugError("debug", typedNil) })
	survives(t, "mtgLogger.Info after the recovered panics", func() { logger.Info("still logging") })

	mu.Lock()
	defer mu.Unlock()
	expectLogLine(t, lines, "the logger must keep logging after a recovered panic", "[mtproto.backstop] still logging")
}

func TestMtgLoggerDerivationsSurviveANilLogger(t *testing.T) {
	var logger *mtgLogger

	for name, derive := range map[string]func() mtglib.Logger{
		"Named":    func() mtglib.Logger { return logger.Named("child") },
		"BindInt":  func() mtglib.Logger { return logger.BindInt("port", 443) },
		"BindStr":  func() mtglib.Logger { return logger.BindStr("ip", "127.0.0.1") },
		"BindJSON": func() mtglib.Logger { return logger.BindJSON("json", "{}") },
	} {
		var child mtglib.Logger
		survives(t, "mtgLogger."+name+" on a nil logger", func() { child = derive() })
		if child == nil {
			t.Fatalf("mtgLogger.%s on a nil logger must still return a usable logger", name)
		}
		survives(t, "a logger derived by "+name+" from a nil logger", func() { child.Info("derived") })
	}
	survives(t, "mtgLogger.Info on a nil logger", func() { logger.Info("nil") })
}
