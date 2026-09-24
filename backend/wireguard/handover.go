package wireguard

import (
	"fmt"
	"log"
	"sync"

	"github.com/pasarguard/node/pkg/stats"
)

type accountingHandOver struct {
	tracker     *stats.Tracker
	interfaceRx int64
	interfaceTx int64
}

var accountingHandover = struct {
	mu      sync.Mutex
	pending map[string]*accountingHandOver
}{pending: make(map[string]*accountingHandOver)}

func depositAccounting(interfaceName string, handOver *accountingHandOver) bool {
	accountingHandover.mu.Lock()
	defer accountingHandover.mu.Unlock()

	_, occupied := accountingHandover.pending[interfaceName]
	accountingHandover.pending[interfaceName] = handOver
	return occupied
}

func takeAccounting(interfaceName string) *accountingHandOver {
	accountingHandover.mu.Lock()
	defer accountingHandover.mu.Unlock()

	handOver := accountingHandover.pending[interfaceName]
	delete(accountingHandover.pending, interfaceName)
	return handOver
}

func (wg *WireGuard) depositAccountingLocked() {
	if wg.config == nil || wg.statsTracker == nil {
		return
	}

	unreadRx, unreadTx := wg.unreadInterfaceBytesLocked()
	handOver := &accountingHandOver{
		tracker:     wg.statsTracker.Detach(),
		interfaceRx: unreadRx,
		interfaceTx: unreadTx,
	}

	interfaceName := wg.config.InterfaceName
	if depositAccounting(interfaceName, handOver) {
		message := fmt.Sprintf("wireguard accounting hand-over for %s replaced one that no backend took", interfaceName)
		log.Printf("%s", message)
		wg.emitLogLocked(logSeverityError, message)
	}
}

func (wg *WireGuard) unreadInterfaceBytesLocked() (int64, int64) {
	if wg.interfaceStats == nil {
		return 0, 0
	}

	var finalRx, finalTx int64
	if wg.manager != nil {
		currentRx, currentTx, err := wg.manager.GetInterfaceStats()
		if err != nil {
			message := fmt.Sprintf("wireguard final interface counter read failed: %v", err)
			log.Printf("%s", message)
			wg.emitLogLocked(logSeverityWarning, message)
		} else {
			finalRx, finalTx = currentRx, currentTx
		}
	}
	return wg.interfaceStats.Delta(finalRx, finalTx, true)
}

func (wg *WireGuard) adoptAccounting(handOver *accountingHandOver) {
	if handOver == nil {
		wg.interfaceStats.CountFromZero(0, 0)
		return
	}
	wg.interfaceStats.CountFromZero(handOver.interfaceRx, handOver.interfaceTx)

	tracker := handOver.tracker
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
