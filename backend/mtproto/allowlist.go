package mtproto

import (
	"net"
	"time"
)

var beforeAllowlistCheck = func(net.IP) {}

type allowEveryone struct {
	panics *panicGuard
}

func (a allowEveryone) Contains(ip net.IP) (admitted bool) {
	defer a.panics.absorb()
	admitted = true
	beforeAllowlistCheck(ip)
	return admitted
}

func (allowEveryone) Run(time.Duration) {}

func (allowEveryone) Shutdown() {}
