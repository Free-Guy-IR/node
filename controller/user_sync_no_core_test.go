package controller

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
)

func TestEveryUserSyncPathRefusesWithUnavailableWhenNoCoreIsRunning(t *testing.T) {
	c := &Controller{}
	u := &common.User{Email: "nobody-home"}
	paths := map[string]func() error{
		"QueueUserAll":   func() error { return c.QueueUserAll(context.Background(), u) },
		"SyncUserAll":    func() error { return c.SyncUserAll(context.Background(), u) },
		"SyncUsersAll":   func() error { return c.SyncUsersAll(context.Background(), []*common.User{u}) },
		"UpdateUsersAll": func() error { return c.UpdateUsersAll(context.Background(), []*common.User{u}) },
	}
	for name, call := range paths {
		err := call()
		if st, ok := status.FromError(err); err == nil || !ok || st.Code() != codes.Unavailable || st.Message() != "backend not initialized" {
			t.Errorf("%s with no backend running returned %v; it must refuse with codes.Unavailable \"backend not initialized\" so the panel keeps the update queued", name, err)
		}
	}
}
