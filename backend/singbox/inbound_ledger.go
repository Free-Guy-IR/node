package singbox

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/pasarguard/node/backend/singbox/api"
	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"github.com/pasarguard/node/common"
)

type ledgerConsumer int

const (
	consumerOutbounds ledgerConsumer = iota
	consumerInbounds
)

const inboundCounterPrefix = "inbound>>>"

var (
	errLedgerNoClient    = errors.New("sing-box stats client is not available")
	errLedgerTagRequired = errors.New("tag required")
)

type inboundLedger struct {
	mu      sync.Mutex
	pending [2]map[string]int64
}

func (l *inboundLedger) collectTag(ctx context.Context, client *api.Client, consumer ledgerConsumer, tag string, reset bool) (*common.StatResponse, error) {
	if tag == "" {
		return nil, errLedgerTagRequired
	}
	return l.collect(ctx, client, consumer, tag, reset)
}

func (l *inboundLedger) collect(ctx context.Context, client *api.Client, consumer ledgerConsumer, tag string, reset bool) (*common.StatResponse, error) {
	if client == nil || client.StatsServiceClient == nil {
		return nil, errLedgerNoClient
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	drained, err := client.DrainInboundCounters(ctx)
	if err != nil {
		return nil, err
	}
	l.credit(drained)

	prefix := inboundCounterPrefix
	if tag != "" {
		prefix += tag + ">>>"
	}

	balance := l.pending[consumer]
	names := make([]string, 0, len(balance))
	for name, value := range balance {
		if value != 0 && strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	collected := make([]*statscmd.Stat, 0, len(names))
	for _, name := range names {
		collected = append(collected, &statscmd.Stat{Name: name, Value: balance[name]})
	}

	if reset {
		for _, name := range names {
			delete(balance, name)
		}
	}

	return api.BuildStatResponse(collected), nil
}

func (l *inboundLedger) credit(drained []*statscmd.Stat) {
	for i := range l.pending {
		if l.pending[i] == nil {
			l.pending[i] = make(map[string]int64)
		}
	}

	for _, stat := range drained {
		name, value := stat.GetName(), stat.GetValue()
		if value == 0 || !strings.HasPrefix(name, inboundCounterPrefix) {
			continue
		}
		for _, balance := range l.pending {
			balance[name] += value
		}
	}
}
