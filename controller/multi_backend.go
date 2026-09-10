package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/netutil"
)

const perBackendStatsTimeout = 10 * time.Second

type extraBackend struct {
	backendType common.BackendType
	backend     backend.Backend
}

var multiInstanceBackends = map[common.BackendType]bool{
	common.BackendType_WIREGUARD: true,
	common.BackendType_SING_BOX:  true,
	common.BackendType_OPEN_VPN:  true,
	common.BackendType_MTPROTO:   true,
	common.BackendType_L2TP:      true,
}

var ErrBackendTypeNotShareable = errors.New("this backend type cannot run alongside another backend on the same node")

func (c *Controller) AttachBackend(ctx context.Context, b *common.Backend) error {
	backendType := b.GetType()
	if !multiInstanceBackends[backendType] {
		return fmt.Errorf("%w: %s", ErrBackendTypeNotShareable, backendType)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.backend == nil {
		return errors.New("no primary backend is running on this node")
	}
	if c.primaryType == backendType {
		return fmt.Errorf("a %s backend is already running as this node primary backend", backendType)
	}
	for _, extra := range c.extras {
		if extra.backendType == backendType {
			return fmt.Errorf("a %s backend is already running on this node", backendType)
		}
	}

	newBackend, err := c.buildBackend(ctx, b, netutil.FindFreePort(), netutil.FindFreePort())
	if err != nil {
		return err
	}

	c.extras = append(c.extras, extraBackend{backendType: backendType, backend: newBackend})
	return nil
}

func (c *Controller) DetachBackend(backendType common.BackendType) error {
	c.mu.Lock()
	var removed backend.Backend
	remaining := make([]extraBackend, 0, len(c.extras))
	for _, extra := range c.extras {
		if extra.backendType == backendType && removed == nil {
			removed = extra.backend
			continue
		}
		remaining = append(remaining, extra)
	}
	c.extras = remaining
	c.mu.Unlock()

	if removed == nil {
		return fmt.Errorf("no %s backend is running on this node", backendType)
	}

	removed.Shutdown()
	return nil
}

func (c *Controller) ExtraBackendTypes() []common.BackendType {
	c.mu.RLock()
	defer c.mu.RUnlock()

	types := make([]common.BackendType, 0, len(c.extras))
	for _, extra := range c.extras {
		types = append(types, extra.backendType)
	}
	return types
}

func (c *Controller) BackendTypes() []common.BackendType {
	c.mu.RLock()
	defer c.mu.RUnlock()

	types := make([]common.BackendType, 0, len(c.extras)+1)
	if c.backend != nil {
		types = append(types, c.primaryType)
	}
	for _, extra := range c.extras {
		types = append(types, extra.backendType)
	}
	return types
}

func (c *Controller) AllBackends() []backend.Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	backends := make([]backend.Backend, 0, len(c.extras)+1)
	if c.backend != nil {
		backends = append(backends, c.backend)
	}
	for _, extra := range c.extras {
		backends = append(backends, extra.backend)
	}
	return backends
}

func (c *Controller) shutdownExtras() {
	c.mu.Lock()
	extras := c.extras
	c.extras = nil
	c.mu.Unlock()

	for _, extra := range extras {
		if extra.backend != nil {
			extra.backend.Shutdown()
		}
	}
}

func (c *Controller) SyncUserAll(ctx context.Context, user *common.User) error {
	var errs []error
	for _, b := range c.AllBackends() {
		if err := b.SyncUser(ctx, user); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) SyncUsersAll(ctx context.Context, users []*common.User) error {
	var errs []error
	for _, b := range c.AllBackends() {
		if err := b.SyncUsers(ctx, users); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) UpdateUsersAll(ctx context.Context, users []*common.User) error {
	var errs []error
	for _, b := range c.AllBackends() {
		if err := b.UpdateUsers(ctx, users); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type backendResult[T any] struct {
	backendType common.BackendType
	value       T
	err         error
}

func (c *Controller) fanOut(ctx context.Context, call func(context.Context, backend.Backend) (any, error)) []backendResult[any] {
	c.mu.RLock()
	targets := make([]extraBackend, 0, len(c.extras)+1)
	if c.backend != nil {
		targets = append(targets, extraBackend{backendType: c.primaryType, backend: c.backend})
	}
	targets = append(targets, c.extras...)
	c.mu.RUnlock()

	results := make([]backendResult[any], len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target extraBackend) {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(ctx, perBackendStatsTimeout)
			defer cancel()
			value, err := call(callCtx, target.backend)
			results[i] = backendResult[any]{backendType: target.backendType, value: value, err: err}
		}(i, target)
	}
	wg.Wait()
	return results
}

func joinBackendErrors(results []backendResult[any]) []error {
	var errs []error
	for _, result := range results {
		if result.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", result.backendType, result.err))
		}
	}
	return errs
}

func logPartialFailure(operation string, delivered int, errs []error) {
	unexpected := make([]error, 0, len(errs))
	for _, err := range errs {
		if !errors.Is(err, backend.ErrStatTypeNotSupported) {
			unexpected = append(unexpected, err)
		}
	}
	if len(unexpected) == 0 || delivered == 0 {
		return
	}
	log.Printf("%s: %d backend(s) failed while %d result(s) were delivered: %v", operation, len(unexpected), delivered, errors.Join(unexpected...))
}

func (c *Controller) StatsAll(ctx context.Context, request *common.StatRequest) (*common.StatResponse, error) {
	results := c.fanOut(ctx, func(callCtx context.Context, b backend.Backend) (any, error) {
		return b.GetStats(callCtx, request)
	})
	if len(results) == 0 {
		return nil, errors.New("backend not initialized")
	}

	merged := &common.StatResponse{}
	for _, result := range results {
		if result.err != nil {
			continue
		}
		if resp, ok := result.value.(*common.StatResponse); ok && resp != nil {
			merged.Stats = append(merged.Stats, resp.GetStats()...)
		}
	}

	errs := joinBackendErrors(results)
	if len(merged.GetStats()) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	logPartialFailure("stats", len(merged.GetStats()), errs)
	return merged, nil
}

func (c *Controller) UserOnlineStatsAll(ctx context.Context, email string) (*common.OnlineStatResponse, error) {
	results := c.fanOut(ctx, func(callCtx context.Context, b backend.Backend) (any, error) {
		return b.GetUserOnlineStats(callCtx, email)
	})
	if len(results) == 0 {
		return nil, errors.New("backend not initialized")
	}

	merged := &common.OnlineStatResponse{Name: email}
	delivered := 0
	for _, result := range results {
		if result.err != nil {
			continue
		}
		resp, ok := result.value.(*common.OnlineStatResponse)
		if !ok || resp == nil {
			continue
		}
		delivered++
		merged.Value += resp.GetValue()
	}

	errs := joinBackendErrors(results)
	if delivered == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	logPartialFailure("online stats", delivered, errs)
	return merged, nil
}

func (c *Controller) UserOnlineIpListStatsAll(ctx context.Context, email string) (*common.StatsOnlineIpListResponse, error) {
	results := c.fanOut(ctx, func(callCtx context.Context, b backend.Backend) (any, error) {
		return b.GetUserOnlineIpListStats(callCtx, email)
	})
	if len(results) == 0 {
		return nil, errors.New("backend not initialized")
	}

	merged := &common.StatsOnlineIpListResponse{Ips: map[string]int64{}}
	delivered := 0
	for _, result := range results {
		if result.err != nil {
			continue
		}
		resp, ok := result.value.(*common.StatsOnlineIpListResponse)
		if !ok || resp == nil {
			continue
		}
		delivered++
		for ip, seenAt := range resp.GetIps() {
			if existing, ok := merged.Ips[ip]; !ok || seenAt > existing {
				merged.Ips[ip] = seenAt
			}
		}
	}

	errs := joinBackendErrors(results)
	if delivered == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	logPartialFailure("online ip list", delivered, errs)
	return merged, nil
}
