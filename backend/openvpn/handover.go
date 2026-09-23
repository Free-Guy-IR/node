package openvpn

import (
	"errors"
	"log"
	"sync"

	"github.com/pasarguard/node/pkg/stats"
)

var errAccountingHandedOver = errors.New("openvpn: the backend has stopped and handed its accounting over")

type nodeBytes struct {
	rx int64
	tx int64
}

type tagAccounting struct {
	outbound nodeBytes
	inbound  nodeBytes
}

func (a tagAccounting) empty() bool {
	return a == tagAccounting{}
}

func retiredOutbound(a *tagAccounting) *nodeBytes {
	return &a.outbound
}

func retiredInbound(a *tagAccounting) *nodeBytes {
	return &a.inbound
}

type accountingHandOver struct {
	statsTracker *stats.Tracker
	nodeLevel    map[string]tagAccounting
	onlineIPs    map[string]map[string]map[string]int64
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

func (h *accountingHandOver) addNodeLevel(tag string, carried tagAccounting) {
	if carried.empty() {
		return
	}
	sum := h.nodeLevel[tag]
	sum.outbound.rx += carried.outbound.rx
	sum.outbound.tx += carried.outbound.tx
	sum.inbound.rx += carried.inbound.rx
	sum.inbound.tx += carried.inbound.tx
	h.nodeLevel[tag] = sum
}

func (h *accountingHandOver) addOnlineIPs(tag string, byEmail map[string]map[string]int64) {
	for email, ips := range byEmail {
		mergeOnlineIPs(h.onlineIPs, tag, email, ips)
	}
}

func mergeOnlineIPs(into map[string]map[string]map[string]int64, tag, email string, ips map[string]int64) {
	if len(ips) == 0 {
		return
	}
	if into[tag] == nil {
		into[tag] = make(map[string]map[string]int64)
	}
	if into[tag][email] == nil {
		into[tag][email] = make(map[string]int64, len(ips))
	}
	for ip, seen := range ips {
		if prev, ok := into[tag][email][ip]; !ok || seen > prev {
			into[tag][email][ip] = seen
		}
	}
}

func (b *Backend) recordLog(line string) {
	log.Println(line)
	select {
	case b.logsChan <- line:
	default:
	}
}

func (b *Backend) handOverAccountingLocked(drained map[string]*ManagementClient) {
	if b.handedOver || b.statsTracker == nil {
		return
	}
	b.handedOver = true

	h := &accountingHandOver{
		nodeLevel: make(map[string]tagAccounting, len(b.instances)+len(b.retired)),
		onlineIPs: make(map[string]map[string]map[string]int64, len(b.instances)),
	}

	var samples []stats.Sample
	for tag, instance := range b.instances {
		var rx, tx int64
		if mgmt := drained[tag]; mgmt != nil {
			totals := mgmt.AllUserStats()
			samples = append(samples, userSamples(tag, totals)...)
			rx, tx = sumUserStats(totals)
			h.addOnlineIPs(tag, mgmt.allOnlineIPs())
		}
		var carried tagAccounting
		carried.outbound.rx, carried.outbound.tx = instance.statsTracker.DeltaClamped(rx, tx, true)
		carried.inbound.rx, carried.inbound.tx = instance.inboundStatsTracker.DeltaClamped(rx, tx, true)
		h.addNodeLevel(tag, carried)
		h.addOnlineIPs(tag, b.carriedIPs[tag])
	}
	for tag, carried := range b.retired {
		h.addNodeLevel(tag, *carried)
	}
	b.retired = nil
	b.carriedIPs = nil

	b.statsTracker.UpdateStatsBatch(samples)
	h.statsTracker = b.statsTracker.Detach()

	if storeHandOver(h) {
		b.recordLog("openvpn: an accounting hand-over that no backend took was replaced")
	}
}

func (b *Backend) adoptAccounting(h *accountingHandOver) {
	for tag, instance := range b.instances {
		var carried tagAccounting
		if h != nil {
			carried = h.nodeLevel[tag]
		}
		instance.statsTracker.CountFromZero(carried.outbound.rx, carried.outbound.tx)
		instance.inboundStatsTracker.CountFromZero(carried.inbound.rx, carried.inbound.tx)
	}
	if h == nil {
		return
	}

	for tag, carried := range h.nodeLevel {
		if _, ok := b.instances[tag]; !ok {
			retired := carried
			b.retired[tag] = &retired
		}
	}

	live := make(map[string]struct{})
	for tag, instance := range b.instances {
		authorised := instance.auth.emails()
		for email := range authorised {
			live[statsKey(tag, email)] = struct{}{}
		}
		for email, ips := range h.onlineIPs[tag] {
			if _, ok := authorised[email]; ok {
				mergeOnlineIPs(b.carriedIPs, tag, email, ips)
			}
		}
	}

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

	b.recordLog("openvpn: carried the unread accounting over from the previous backend")
}

func (b *Backend) readRetired(tag string, reset bool, part func(*tagAccounting) *nodeBytes) (nodeBytes, bool) {
	carried, ok := b.retired[tag]
	if !ok {
		return nodeBytes{}, false
	}
	bytes := *part(carried)
	if reset {
		*part(carried) = nodeBytes{}
		if carried.empty() {
			delete(b.retired, tag)
		}
	}
	return bytes, true
}
