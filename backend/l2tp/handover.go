package l2tp

import (
	"sort"
	"sync"

	"github.com/pasarguard/node/pkg/stats"
)

type accountingHandOver struct {
	statsTracker   *stats.Tracker
	interfaceStats *stats.InterfaceCountersTracker
	inboundStats   *stats.InterfaceCountersTracker
	cumRx          map[string]int64
	cumTx          map[string]int64
	totalRx        int64
	totalTx        int64
	counted        *countedState
}

var handOvers = struct {
	sync.Mutex
	byTag map[string]*accountingHandOver
}{byTag: make(map[string]*accountingHandOver)}

func storeHandOver(tag string, h *accountingHandOver) (replaced bool) {
	handOvers.Lock()
	defer handOvers.Unlock()
	_, replaced = handOvers.byTag[tag]
	handOvers.byTag[tag] = h
	return replaced
}

func takeHandOver(tag string) *accountingHandOver {
	handOvers.Lock()
	defer handOvers.Unlock()
	h, ok := handOvers.byTag[tag]
	if !ok {
		return nil
	}
	delete(handOvers.byTag, tag)
	return h
}

func (o *L2TP) depositHandOver(h *accountingHandOver) {
	if storeHandOver(o.config.InboundTag, h) {
		o.emitLogf("Error", "l2tp: an unclaimed accounting hand-over of %s was replaced", o.config.InboundTag)
	}
}

func (o *L2TP) startWithAccounting(start func() error) error {
	taken := takeHandOver(o.config.InboundTag)
	o.mu.Lock()
	if taken != nil {
		o.adoptLocked(taken)
	} else {
		o.loadCountedLocked()
	}
	o.mu.Unlock()

	if err := start(); err != nil {
		if taken != nil {
			o.depositHandOver(taken)
		}
		return err
	}
	o.forgetUsers(o.absentUsers())
	return nil
}

func (o *L2TP) adoptLocked(h *accountingHandOver) {
	o.statsTracker = h.statsTracker
	o.interfaceStats = h.interfaceStats
	o.inboundStats = h.inboundStats
	o.cumRx = h.cumRx
	o.cumTx = h.cumTx
	o.totalRx = h.totalRx
	o.totalTx = h.totalTx
	o.counted = h.counted
	o.emitLogf("Info", "l2tp: carried the accounting of %s over from the previous instance", o.config.InboundTag)
}

func (o *L2TP) handOverLocked() {
	o.pollLocked()
	h := &accountingHandOver{
		statsTracker:   o.statsTracker,
		interfaceStats: o.interfaceStats,
		inboundStats:   o.inboundStats,
		cumRx:          o.cumRx,
		cumTx:          o.cumTx,
		totalRx:        o.totalRx,
		totalTx:        o.totalTx,
		counted:        o.counted,
	}
	o.cumRx = make(map[string]int64)
	o.cumTx = make(map[string]int64)
	o.counted = newCountedState()
	o.depositHandOver(h)
}

func (o *L2TP) absentUsers() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	absent := make(map[string]struct{})
	for _, cum := range []map[string]int64{o.cumRx, o.cumTx} {
		for u := range cum {
			if !o.users.has(u) {
				absent[u] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(absent))
	for u := range absent {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}
