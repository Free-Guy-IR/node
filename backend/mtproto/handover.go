package mtproto

import (
	"net"

	"github.com/pasarguard/node/pkg/stats"
)

type accountingHandOver struct {
	statsTracker *stats.Tracker
}

func takeHandOver() *accountingHandOver {
	return nil
}

type streamSet struct{}

func newStreamSet() *streamSet {
	return &streamSet{}
}

func (s *streamSet) track(listener net.Listener) net.Listener {
	return listener
}
