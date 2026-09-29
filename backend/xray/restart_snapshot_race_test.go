package xray

import (
	"sync"
	"testing"
)

func TestAHealthRestartConfigSnapshotDoesNotRaceASyncUser(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", syncTestTags...)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := x.config.ToBytes(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := range 200 {
		if i%2 == 0 {
			user.Proxies.Trojan.Password = "first trojan secret"
		} else {
			user.Proxies.Trojan.Password = "second trojan secret"
		}
		_ = x.SyncUser(ctx, user)
	}
	close(stop)
	wg.Wait()
}
