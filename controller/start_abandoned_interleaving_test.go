package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

type startTagKey struct{}

type taggedCore struct {
	backend.Backend
	tag  string
	shut atomic.Bool
}

func (b *taggedCore) Started() bool   { return !b.shut.Load() }
func (b *taggedCore) Version() string { return "tagged" }
func (b *taggedCore) Shutdown()       { b.shut.Store(true) }

type coreLedger struct {
	mu      sync.Mutex
	cores   []*taggedCore
	entered chan string
}

func (l *coreLedger) build(startTakes time.Duration) func(context.Context, *common.Backend, int, int) (backend.Backend, error) {
	return func(ctx context.Context, _ *common.Backend, _, _ int) (backend.Backend, error) {
		tag, _ := ctx.Value(startTagKey{}).(string)
		select {
		case l.entered <- tag:
		default:
		}
		select {
		case <-time.After(startTakes):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		core := &taggedCore{tag: tag}
		l.mu.Lock()
		l.cores = append(l.cores, core)
		l.mu.Unlock()
		return core, nil
	}
}

func (l *coreLedger) snapshot() []*taggedCore {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*taggedCore(nil), l.cores...)
}

func TestAnAbandonedStartRacingASecondStartAndADisconnectEndsConsistent(t *testing.T) {
	for round := 0; round < 16; round++ {
		lockedStop := round%2 == 0
		stopDelay := time.Duration(round/2%4) * 25 * time.Millisecond
		abandonedStartRound(t, round, lockedStop, stopDelay)
	}
}

func abandonedStartRound(t *testing.T, round int, lockedStop bool, stopDelay time.Duration) {
	t.Helper()
	ledger := &coreLedger{entered: make(chan string, 8)}
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	c.newBackend = ledger.build(60 * time.Millisecond)
	t.Cleanup(c.Close)

	var orderMu sync.Mutex
	var order []string
	record := func(name string) {
		orderMu.Lock()
		order = append(order, name)
		orderMu.Unlock()
	}

	abandoned, giveUp := context.WithCancel(context.WithValue(context.Background(), startTagKey{}, "first"))
	firstErr := make(chan error, 1)
	go func() {
		c.LockControl()
		defer c.UnlockControl()
		err := c.ReplaceBackend(abandoned, xrayStart())
		record("first")
		firstErr <- err
	}()
	select {
	case <-ledger.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("round %d: the first start never reached the core", round)
	}
	giveUp()

	secondErr := make(chan error, 1)
	go func() {
		c.LockControl()
		defer c.UnlockControl()
		err := c.ReplaceBackend(context.WithValue(context.Background(), startTagKey{}, "second"), xrayStart())
		record("second")
		secondErr <- err
	}()
	stopped := make(chan struct{})
	go func() {
		time.Sleep(stopDelay)
		if lockedStop {
			c.LockControl()
			defer c.UnlockControl()
		}
		c.Disconnect()
		record("disconnect")
		close(stopped)
	}()

	deadline := time.After(10 * time.Second)
	var errFirst, errSecond error
	for pending := 3; pending > 0; pending-- {
		select {
		case errFirst = <-firstErr:
		case errSecond = <-secondErr:
		case <-stopped:
		case <-deadline:
			t.Fatalf("round %d: deadlock, an abandoned start, a second start and a Disconnect did not all finish", round)
		}
	}

	for name, err := range map[string]error{"first": errFirst, "second": errSecond} {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("round %d: the %s start failed with something other than an abort: %v", round, name, err)
		}
	}
	if lockedStop {
		if errFirst != nil {
			t.Fatalf("round %d: the start whose caller gave up failed although nothing aborted it: %v", round, errFirst)
		}
		if errSecond != nil {
			t.Fatalf("round %d: the second start failed although nothing aborted it: %v", round, errSecond)
		}
	}

	current := c.Backend()
	running := 0
	for _, core := range ledger.snapshot() {
		if core.Started() {
			running++
			if backend.Backend(core) != current {
				t.Fatalf("round %d: a stray %s core is still running beside the attached backend", round, core.tag)
			}
		}
	}
	if running > 1 {
		t.Fatalf("round %d: %d cores running", round, running)
	}

	orderMu.Lock()
	last := order[len(order)-1]
	orderMu.Unlock()
	switch {
	case current != nil:
		core, ok := current.(*taggedCore)
		if !ok || core.tag != "second" || !core.Started() {
			t.Fatalf("round %d: the attached backend is not the second start's running core", round)
		}
		if errSecond != nil {
			t.Fatalf("round %d: a core is attached although the second start failed: %v", round, errSecond)
		}
		if lockedStop && last != "second" {
			t.Fatalf("round %d: a core is attached although Disconnect ran after the second start", round)
		}
	case errSecond == nil && lockedStop && last == "second":
		t.Fatalf("round %d: the second start succeeded last but no core is attached", round)
	}
}
