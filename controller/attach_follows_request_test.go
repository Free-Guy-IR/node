package controller

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func TestAnAddedBackendStopsBuildingWhenItsRequestIsCancelled(t *testing.T) {
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	t.Cleanup(c.Close)

	c.mu.Lock()
	c.backend = &slowStartBackend{}
	c.primaryType = common.BackendType_XRAY
	c.mu.Unlock()

	builds := make(chan struct{}, 4)
	c.newBackend = func(ctx context.Context, _ *common.Backend, _, _ int) (backend.Backend, error) {
		builds <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
			return &slowStartBackend{}, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	began := time.Now()
	c.LockControl()
	err := c.AttachBackend(ctx, &common.Backend{Type: common.BackendType_WIREGUARD})
	c.UnlockControl()
	took := time.Since(began)

	if err == nil {
		t.Fatalf("AddBackend succeeded after its request was cancelled (took %s); it must build under the request's context", took)
	}
	if took > time.Second {
		t.Fatalf("AddBackend kept building %s after its request was cancelled at 50ms", took)
	}
	if got := c.BackendTypes(); len(got) != 1 {
		t.Fatalf("a cancelled AddBackend left backends %v, want only the primary", got)
	}
	if len(builds) == 0 {
		t.Fatal("AddBackend never reached the builder")
	}
}
