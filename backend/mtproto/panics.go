package mtproto

import (
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

var panicReportInterval = 30 * time.Second

var strayPanics = newPanicGuard("an mtproto callback", nil)

type panicGuard struct {
	site   string
	record func(string)
	now    func() time.Time

	mu         sync.Mutex
	next       time.Time
	suppressed int
}

func newPanicGuard(site string, record func(string)) *panicGuard {
	return &panicGuard{site: site, record: record, now: time.Now}
}

func (g *panicGuard) absorb() {
	value := recover()
	if value == nil {
		return
	}
	g.report(value)
}

func (g *panicGuard) report(value any) {
	if g == nil {
		g = strayPanics
	}

	g.mu.Lock()
	now := g.now()
	if now.Before(g.next) {
		g.suppressed++
		g.mu.Unlock()
		return
	}
	suppressed := g.suppressed
	g.suppressed = 0
	g.next = now.Add(panicReportInterval)
	g.mu.Unlock()

	site := g.site
	if suppressed > 0 {
		site = fmt.Sprintf("%s (%d more since the last report)", site, suppressed)
	}
	reportPanic(site, value, g.record)
}

func reportPanic(site string, value any, record func(string)) {
	alarm(fmt.Sprintf("mtproto: PANIC recovered in %s: %v\n%s", site, value, debug.Stack()), record)
}

func alarm(line string, record func(string)) {
	log.Print(line)
	if record == nil {
		return
	}
	defer func() {
		if value := recover(); value != nil {
			log.Printf("mtproto: the report above could not reach the backend log: %v", value)
		}
	}()
	record(line)
}
