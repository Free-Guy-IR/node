package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

type userRecordingGated struct {
	*gatedBackend
	mu    sync.Mutex
	users map[string]bool
}

func newUserRecordingGated(gate chan struct{}, snapshot ...string) *userRecordingGated {
	r := &userRecordingGated{gatedBackend: newGatedBackend(gate), users: map[string]bool{}}
	for _, email := range snapshot {
		r.users[email] = true
	}
	return r
}

func (r *userRecordingGated) record(users []*common.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range users {
		r.users[u.GetEmail()] = true
	}
}

func (r *userRecordingGated) SyncUser(_ context.Context, u *common.User) error {
	r.record([]*common.User{u})
	return nil
}

func (r *userRecordingGated) SyncUsers(_ context.Context, users []*common.User) error {
	r.record(users)
	return nil
}

func (r *userRecordingGated) knows(email string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.users[email]
}

func nodeAcceptsUserSync(c *Controller) bool {
	return c.Backend() != nil && len(c.AllBackends()) > 0 && c.AnyBackendStarted()
}

func TestUserSyncAcknowledgedWhileStartReplacesTheCoreMustReachTheCoreStartLeavesRunning(t *testing.T) {
	t.Parallel()
	c := newWatchdogController(t)

	oldPrimary := newUserRecordingGated(nil, "snapshot-user")
	drainingExtra := newGatedBackend(make(chan struct{}))
	c.mu.Lock()
	c.backend = oldPrimary
	c.primaryType = common.BackendType_XRAY
	c.extras = []extraBackend{{backendType: common.BackendType_L2TP, backend: drainingExtra}}
	c.mu.Unlock()
	c.Connect(panelIP, 60)

	fresh := newUserRecordingGated(nil, "snapshot-user")
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		c.LockControl()
		defer c.UnlockControl()
		c.Disconnect()
		c.mu.Lock()
		c.backend = fresh
		c.primaryType = common.BackendType_XRAY
		c.mu.Unlock()
		c.Connect(panelIP, 60)
	}()

	select {
	case <-drainingExtra.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start never began shutting down the old cores")
	}

	late := &common.User{Email: "changed-after-the-start-snapshot"}
	accepted := nodeAcceptsUserSync(c)
	var syncErr error
	if accepted {
		syncErr = c.QueueUserAll(context.Background(), late)
	}

	close(drainingExtra.gate)
	select {
	case <-startDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Start never finished")
	}

	if accepted && syncErr == nil && !fresh.knows(late.GetEmail()) {
		t.Fatalf("a SyncUser that arrived while Start was shutting the old cores down was acknowledged (accepted=%v, err=%v) but only the core being torn down received it (old primary has it=%v); the core Start left running (built from the Start snapshot) does not have it=%v, so the panel will never resend it",
			accepted, syncErr, oldPrimary.knows(late.GetEmail()), fresh.knows(late.GetEmail()))
	}
}
