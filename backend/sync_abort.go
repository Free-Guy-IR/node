package backend

import (
	"context"
	"errors"
	"time"
)

type syncAbortKey struct{}

func WithSyncAbort(ctx, abort context.Context) context.Context {
	return context.WithValue(ctx, syncAbortKey{}, abort)
}

func SyncAbort(ctx context.Context) context.Context {
	abort, _ := ctx.Value(syncAbortKey{}).(context.Context)
	return abort
}

func DetachFromCaller(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	stops := make([]func() bool, 0, 2)
	if abort := SyncAbort(ctx); abort != nil {
		stops = append(stops, context.AfterFunc(abort, cancel))
	}
	stops = append(stops, context.AfterFunc(ctx, func() {
		cause := context.Cause(ctx)
		if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			cancel()
		}
	}))
	return detached, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}
