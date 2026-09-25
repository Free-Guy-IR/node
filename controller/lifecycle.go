package controller

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
)

const defaultUserSyncDrainGrace = 10 * time.Second

var (
	ErrBackendNotInitialized = status.Error(codes.Unavailable, "backend not initialized")
	ErrCoreNotStarted        = status.Error(codes.Unavailable, "core is not started yet")
	ErrNodeClosed            = status.Error(codes.Unavailable, "node is shutting down")
)

type userSyncRegistry struct {
	mu      sync.Mutex
	next    uint64
	cancels map[uint64]context.CancelFunc
}

func (r *userSyncRegistry) track(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	if r.cancels == nil {
		r.cancels = make(map[uint64]context.CancelFunc)
	}
	r.next++
	id := r.next
	r.cancels[id] = cancel
	r.mu.Unlock()
	return ctx, func() {
		r.mu.Lock()
		delete(r.cancels, id)
		r.mu.Unlock()
		cancel()
	}
}

func (r *userSyncRegistry) cancelAll() int {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.cancels))
	for _, cancel := range r.cancels {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

func (c *Controller) lockLifecycle() {
	grace := c.syncDrainGrace
	if grace <= 0 {
		grace = defaultUserSyncDrainGrace
	}
	acquired := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(grace)
		defer ticker.Stop()
		for {
			select {
			case <-acquired:
				return
			case <-ticker.C:
				select {
				case <-acquired:
					return
				default:
				}
				if n := c.userSyncs.cancelAll(); n > 0 {
					log.Printf("a core lifecycle change waited %s behind %d in-flight user sync(s); cancelling them so they fail instead of blocking it", grace, n)
				}
			}
		}
	}()
	c.lifecycleMu.Lock()
	close(acquired)
	<-watcherDone
}

func (c *Controller) unlockLifecycle() {
	c.lifecycleMu.Unlock()
}

func (c *Controller) fanOutUserSync(ctx context.Context, apply func(context.Context, backend.Backend) error) error {
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()

	backends := c.AllBackends()
	if len(backends) == 0 {
		return ErrBackendNotInitialized
	}

	ctx, done := c.userSyncs.track(ctx)
	defer done()

	var errs []error
	for _, b := range backends {
		if err := apply(ctx, b); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) ReplaceBackend(ctx context.Context, b *common.Backend) error {
	c.lockLifecycle()
	defer c.unlockLifecycle()

	if c.closed {
		return ErrNodeClosed
	}
	if c.Backend() != nil {
		c.disconnect()
	}
	return c.startBackend(ctx, b)
}

func (c *Controller) Close() {
	c.lockLifecycle()
	defer c.unlockLifecycle()

	c.closed = true
	c.disconnect()
}

func (c *Controller) PrimarySysStats(ctx context.Context) (*common.BackendStatsResponse, error) {
	primary := c.Backend()
	if primary == nil {
		return nil, ErrBackendNotInitialized
	}
	if !primary.Started() {
		return nil, ErrCoreNotStarted
	}
	return primary.GetSysStats(ctx)
}
