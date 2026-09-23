package mtproto

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pasarguard/node/pkg/stats"
)

var errAccountingHandedOver = errors.New("mtproto: the backend has stopped and handed its accounting over")

type trafficBytes struct {
	rx int64
	tx int64
}

type accountingHandOver struct {
	statsTracker *stats.Tracker
	outbound     trafficBytes
	inbound      map[string]trafficBytes
	retired      []*retiredSource
}

type retiredSource struct {
	tag         string
	accumulator *eventAccumulator
	streams     *streamSet
	marks       map[string]trafficBytes
}

func (s *retiredSource) exited() bool {
	return (s.streams == nil || s.streams.empty()) && s.accumulator.idle()
}

var handOver struct {
	sync.Mutex
	pending *accountingHandOver
}

func storeHandOver(h *accountingHandOver) (replaced bool) {
	handOver.Lock()
	defer handOver.Unlock()
	replaced = handOver.pending != nil
	handOver.pending = h
	return replaced
}

func takeHandOver() *accountingHandOver {
	handOver.Lock()
	defer handOver.Unlock()
	h := handOver.pending
	handOver.pending = nil
	return h
}

func (h *accountingHandOver) addInbound(tag string, bytes trafficBytes) {
	if bytes.rx == 0 && bytes.tx == 0 {
		return
	}
	sum := h.inbound[tag]
	sum.rx += bytes.rx
	sum.tx += bytes.tx
	h.inbound[tag] = sum
}

func (b *Backend) handOverAccounting(running []*proxyInstance) {
	b.accountingMu.Lock()
	defer b.accountingMu.Unlock()

	if b.handedOver || b.statsTracker == nil {
		return
	}
	b.handedOver = true

	b.drainRetiredSources()
	b.refreshUserStats()

	retired := b.retiredSources
	b.retiredSources = nil
	b.retiredSinks = nil
	for _, inst := range running {
		samples, marks := inst.accumulator.snapshotAndMark(inst.tag)
		b.statsTracker.UpdateStatsBatch(samples)
		retired = append(retired, &retiredSource{
			tag:         inst.tag,
			accumulator: inst.accumulator,
			streams:     inst.streams,
			marks:       marks,
		})
	}

	h := &accountingHandOver{
		statsTracker: b.statsTracker.Detach(),
		outbound:     trafficBytes{rx: b.outboundRx.Swap(0), tx: b.outboundTx.Swap(0)},
		inbound:      make(map[string]trafficBytes, len(b.inboundCounters)+len(b.retiredInbound)),
		retired:      retired,
	}
	for tag, counter := range b.inboundCounters {
		h.addInbound(tag, trafficBytes{rx: counter.rx.Swap(0), tx: counter.tx.Swap(0)})
	}
	for tag, carried := range b.retiredInbound {
		h.addInbound(tag, *carried)
	}
	b.retiredInbound = nil

	if storeHandOver(h) {
		b.recordLog("mtproto: an accounting hand-over that no backend took was replaced")
	}
}

func (b *Backend) adoptAccounting(h *accountingHandOver) {
	if h == nil {
		return
	}

	b.outboundRx.Add(h.outbound.rx)
	b.outboundTx.Add(h.outbound.tx)
	for tag, bytes := range h.inbound {
		if counter, ok := b.inboundCounters[tag]; ok {
			counter.rx.Add(bytes.rx)
			counter.tx.Add(bytes.tx)
			continue
		}
		b.addRetiredInbound(tag, bytes)
	}
	b.retiredSources = append(b.retiredSources, h.retired...)

	live := b.liveStatsKeys()
	restart := make([]string, 0, len(live))
	for _, key := range h.statsTracker.Keys() {
		if _, ok := live[key]; ok {
			restart = append(restart, key)
			continue
		}
		h.statsTracker.RemoveStats(key)
	}
	h.statsTracker.RestartCounters(restart)
	b.statsTracker = h.statsTracker

	b.recordLog("mtproto: carried the unread accounting over from the previous backend")
}

func (b *Backend) addRetiredInbound(tag string, bytes trafficBytes) {
	if bytes.rx == 0 && bytes.tx == 0 {
		return
	}
	if b.retiredInbound == nil {
		b.retiredInbound = make(map[string]*trafficBytes)
	}
	carried, ok := b.retiredInbound[tag]
	if !ok {
		carried = &trafficBytes{}
		b.retiredInbound[tag] = carried
	}
	carried.rx += bytes.rx
	carried.tx += bytes.tx
}

func (b *Backend) drainRetiredSources() {
	if len(b.retiredSources) == 0 {
		return
	}

	kept := make([]*retiredSource, 0, len(b.retiredSources))
	for _, source := range b.retiredSources {
		exited := source.exited()
		b.drainRetiredSource(source)
		if !exited {
			kept = append(kept, source)
		}
	}
	b.retiredSources = kept
	b.releaseIdleSinks()
}

func (b *Backend) drainRetiredSource(source *retiredSource) {
	sink := b.sinkFor(source.tag)
	for username, grown := range source.accumulator.growthSince(source.marks) {
		sink.fold(username, grown.email, grown.bytes)
	}

	from := source.accumulator
	b.outboundRx.Add(from.outboundRx.Swap(0))
	b.outboundTx.Add(from.outboundTx.Swap(0))
	inbound := trafficBytes{rx: from.inboundRx.Swap(0), tx: from.inboundTx.Swap(0)}
	if counter, ok := b.inboundCounters[source.tag]; ok {
		counter.rx.Add(inbound.rx)
		counter.tx.Add(inbound.tx)
		return
	}
	b.addRetiredInbound(source.tag, inbound)
}

func (b *Backend) sinkFor(tag string) *eventAccumulator {
	b.mu.RLock()
	inst, ok := b.instances[tag]
	b.mu.RUnlock()
	if ok && inst.accumulator != nil {
		return inst.accumulator
	}

	if b.retiredSinks == nil {
		b.retiredSinks = make(map[string]*eventAccumulator)
	}
	sink, ok := b.retiredSinks[tag]
	if !ok {
		sink = newEventAccumulator(new(atomic.Int64), new(atomic.Int64), new(atomic.Int64), new(atomic.Int64))
		b.retiredSinks[tag] = sink
	}
	return sink
}

func (b *Backend) releaseIdleSinks() {
	for tag, sink := range b.retiredSinks {
		if b.feedsSink(tag) {
			continue
		}
		samples := sink.snapshot(tag)
		b.statsTracker.UpdateStatsBatch(samples)
		for _, sample := range samples {
			b.statsTracker.RemoveStats(sample.PublicKey)
		}
		delete(b.retiredSinks, tag)
	}
}

func (b *Backend) feedsSink(tag string) bool {
	for _, source := range b.retiredSources {
		if source.tag == tag {
			return true
		}
	}
	return false
}

func (b *Backend) liveStatsKeys() map[string]struct{} {
	b.mu.RLock()
	defer b.mu.RUnlock()

	live := make(map[string]struct{}, len(b.instances)*len(b.secretsByID))
	for tag := range b.instances {
		for username := range b.secretsByID {
			live[statsKey(tag, username)] = struct{}{}
		}
	}
	return live
}

type streamSet struct {
	mu     sync.Mutex
	conns  map[*trackedConn]struct{}
	closed bool
}

func newStreamSet() *streamSet {
	return &streamSet{conns: make(map[*trackedConn]struct{})}
}

func (s *streamSet) track(listener net.Listener) net.Listener {
	return &trackedListener{Listener: listener, streams: s}
}

func (s *streamSet) add(conn net.Conn) net.Conn {
	tracked := &trackedConn{Conn: conn, streams: s}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[tracked] = struct{}{}
	if s.closed {
		_ = conn.Close()
	}
	return tracked
}

func (s *streamSet) remove(conn *trackedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *streamSet) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for conn := range s.conns {
		_ = conn.Conn.Close()
	}
}

func (s *streamSet) empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns) == 0
}

type trackedListener struct {
	net.Listener
	streams *streamSet
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.streams.add(conn), nil
}

type trackedConn struct {
	net.Conn
	streams *streamSet
	once    sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.streams.remove(c) })
	return c.Conn.Close()
}

func (pi *proxyInstance) drainStreams() bool {
	if pi.streams == nil {
		return true
	}
	pi.streams.closeAll()

	deadline := time.Now().Add(serveExitTimeout)
	for !pi.streams.empty() || !pi.accumulator.idle() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}
