package mtproto

import (
	"net"
	"time"
)

type allowEveryone struct{}

func (allowEveryone) Contains(net.IP) bool { return true }

func (allowEveryone) Run(time.Duration) {}

func (allowEveryone) Shutdown() {}
