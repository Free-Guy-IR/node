package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/pasarguard/node/common"
)

func mtprotoStartUsers(tag string, n int) []*common.User {
	users := make([]*common.User, n)
	for i := range users {
		name := fmt.Sprintf("snapshot-%d", i)
		users[i] = &common.User{
			Email:    name,
			Proxies:  &common.Proxy{Mtproto: &common.MtprotoUser{Username: name, Secret: randomSecret()}},
			Inbounds: []string{tag},
		}
	}
	return users
}

func randomSecret() string {
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return hex.EncodeToString(secret)
}

func TestUserSyncDuringTheStartBuildMustNotBeAcknowledgedWithNoCoreToReceiveIt(t *testing.T) {
	c := newWatchdogController(t)

	const tag = "build"
	data := &common.Backend{
		Type:      common.BackendType_MTPROTO,
		Config:    fmt.Sprintf(`{"instances":[{"tag":%q,"port":%d,"mode":"plain"}]}`, tag, unusedPort(t)),
		Users:     mtprotoStartUsers(tag, 2000),
		KeepAlive: 60,
	}
	if err := panelStart(c, panelIP, data); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	late := &common.User{
		Email:    "changed-after-the-start-snapshot",
		Proxies:  &common.Proxy{Mtproto: &common.MtprotoUser{Username: "changed-after-the-start-snapshot", Secret: randomSecret()}},
		Inbounds: []string{tag},
	}

	var acknowledgedIntoNothing, calls atomic.Int64
	for round := 0; round < 5; round++ {
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				before := len(c.AllBackends())
				err := c.QueueUserAll(context.Background(), late)
				after := len(c.AllBackends())
				calls.Add(1)
				if before == 0 && after == 0 && err == nil {
					acknowledgedIntoNothing.Add(1)
				}
				runtime.Gosched()
			}
		}()

		err := panelStart(c, panelIP, data)
		close(stop)
		<-done
		if err != nil {
			t.Fatalf("Start %d: %v", round, err)
		}
	}

	if n := acknowledgedIntoNothing.Load(); n > 0 {
		t.Fatalf("%d of %d user syncs sent on an already-open SyncUser stream while Start was rebuilding the core returned success although the node held no core at all before and after the call; the stream acknowledges them, the panel drops them from its queue, and the core Start builds from its older snapshot never receives them",
			n, calls.Load())
	}
}
