package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

type slowStartBackend struct {
	backend.Backend
	shut atomic.Bool
}

func (b *slowStartBackend) Started() bool   { return !b.shut.Load() }
func (b *slowStartBackend) Version() string { return "slow-start" }
func (b *slowStartBackend) Shutdown()       { b.shut.Store(true) }

func slowStartController(t *testing.T, startTakes time.Duration) (*Controller, <-chan struct{}, chan struct{}) {
	t.Helper()
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	c.newBackend = func(ctx context.Context, _ *common.Backend, _, _ int) (backend.Backend, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-time.After(startTakes):
			return &slowStartBackend{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, errors.New("released by the test")
		}
	}
	return c, entered, release
}

func xrayStart() *common.Backend {
	return &common.Backend{Type: common.BackendType_XRAY, KeepAlive: 60}
}

func TestAStartOutlivesTheCallerThatGaveUp(t *testing.T) {
	c, _, release := slowStartController(t, 400*time.Millisecond)
	defer close(release)
	t.Cleanup(c.Close)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	c.LockControl()
	err := c.ReplaceBackend(ctx, xrayStart())
	c.UnlockControl()
	if err != nil {
		t.Fatalf("a Start whose caller went away failed: %v", err)
	}
	if back := c.Backend(); back == nil || !back.Started() {
		t.Fatal("no running core after a Start whose caller went away")
	}
}

func abortsAStartInProgress(t *testing.T, name string, stop func(*Controller)) {
	t.Helper()
	c, entered, release := slowStartController(t, time.Minute)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	startErr := make(chan error, 1)
	go func() {
		c.LockControl()
		defer c.UnlockControl()
		startErr <- c.ReplaceBackend(context.Background(), xrayStart())
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the start never reached the core")
	}

	stopped := make(chan struct{})
	go func() {
		stop(c)
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		close(release)
		released = true
		<-stopped
		t.Fatalf("%s waited for a start that ignores its caller instead of aborting it", name)
	}
	select {
	case err := <-startErr:
		if err == nil {
			t.Fatalf("a start aborted by %s reported success", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the start still runs 5s after %s", name)
	}
	if c.Backend() != nil {
		t.Fatalf("a start aborted by %s left a backend attached", name)
	}
}

func TestDisconnectAbortsAStartInProgress(t *testing.T) {
	abortsAStartInProgress(t, "Disconnect", func(c *Controller) { c.Disconnect() })
}

func TestCloseAbortsAStartInProgress(t *testing.T) {
	abortsAStartInProgress(t, "Close", func(c *Controller) { c.Close() })
}
