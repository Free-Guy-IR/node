package wireguard

import (
	"fmt"
	"log"
	"sync"

	"github.com/pasarguard/node/pkg/stats"
)

var accountingHandover = struct {
	mu       sync.Mutex
	trackers map[string]*stats.Tracker
}{trackers: make(map[string]*stats.Tracker)}

func depositAccounting(interfaceName string, tracker *stats.Tracker) bool {
	accountingHandover.mu.Lock()
	defer accountingHandover.mu.Unlock()

	_, occupied := accountingHandover.trackers[interfaceName]
	accountingHandover.trackers[interfaceName] = tracker
	return occupied
}

func takeAccounting(interfaceName string) *stats.Tracker {
	accountingHandover.mu.Lock()
	defer accountingHandover.mu.Unlock()

	tracker := accountingHandover.trackers[interfaceName]
	delete(accountingHandover.trackers, interfaceName)
	return tracker
}

func (wg *WireGuard) depositAccountingLocked() {
	if wg.config == nil || wg.statsTracker == nil {
		return
	}

	interfaceName := wg.config.InterfaceName
	if depositAccounting(interfaceName, wg.statsTracker) {
		message := fmt.Sprintf("wireguard accounting hand-over for %s replaced one that no backend took", interfaceName)
		log.Printf("%s", message)
		wg.emitLogLocked(logSeverityError, message)
	}
}

func (wg *WireGuard) adoptAccounting(tracker *stats.Tracker) {
	if tracker == nil {
		return
	}

	configured := wg.peerStore.GetEmailMap()
	restart := make([]string, 0, len(configured))
	for _, key := range tracker.Keys() {
		if _, ok := configured[key]; ok {
			restart = append(restart, key)
			continue
		}
		tracker.RemoveStats(key)
	}
	tracker.RestartCounters(restart)

	wg.statsTracker = tracker
}
