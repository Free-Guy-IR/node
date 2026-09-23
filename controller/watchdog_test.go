package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const panelIP = "127.0.0.1"

type gatedBackend struct {
	backend.Backend
	started   atomic.Bool
	gate      chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
	shutdowns atomic.Int32
}

func newGatedBackend(gate chan struct{}) *gatedBackend {
	g := &gatedBackend{gate: gate, entered: make(chan struct{})}
	g.started.Store(true)
	return g
}

func (g *gatedBackend) Started() bool   { return g.started.Load() }
func (g *gatedBackend) Version() string { return "gated" }

func (g *gatedBackend) GetStats(context.Context, *common.StatRequest) (*common.StatResponse, error) {
	return &common.StatResponse{}, nil
}

func (g *gatedBackend) GetSysStats(context.Context) (*common.BackendStatsResponse, error) {
	return &common.BackendStatsResponse{}, nil
}

func (g *gatedBackend) Shutdown() {
	g.shutdowns.Add(1)
	g.enterOnce.Do(func() { close(g.entered) })
	if g.gate != nil {
		<-g.gate
	}
	g.started.Store(false)
}

func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func panelStart(c *Controller, ip string, data *common.Backend) error {
	c.LockControl()
	defer c.UnlockControl()

	if c.Backend() != nil {
		if !c.IsCurrentClient(ip) {
			return errors.New("node is controlled by another client")
		}
		c.Disconnect()
	}

	if err := c.StartBackend(context.Background(), data); err != nil {
		return err
	}

	c.Connect(ip, data.GetKeepAlive())
	_ = c.BaseInfoResponse()
	return nil
}

func newWatchdogController(t *testing.T) *Controller {
	t.Helper()
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	t.Cleanup(func() {
		c.LockControl()
		c.Disconnect()
		c.UnlockControl()
	})
	return c
}

func silentFor(c *Controller, d time.Duration) {
	c.mu.Lock()
	c.lastRequest = time.Now().Add(-d)
	c.mu.Unlock()
}

func TestLateKeepAliveWatchdogMustNotTearDownACoreAConcurrentStartCreated(t *testing.T) {
	t.Parallel()
	c := newWatchdogController(t)

	old := newGatedBackend(nil)
	slowExtra := newGatedBackend(make(chan struct{}))

	c.mu.Lock()
	c.backend = old
	c.primaryType = common.BackendType_XRAY
	c.extras = []extraBackend{{backendType: common.BackendType_WIREGUARD, backend: slowExtra}}
	c.mu.Unlock()

	c.Connect(panelIP, 1)
	silentFor(c, time.Minute)

	select {
	case <-slowExtra.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the keep-alive watchdog never fired")
	}

	freshPort := unusedPort(t)
	data := &common.Backend{
		Type:      common.BackendType_MTPROTO,
		Config:    fmt.Sprintf(`{"instances":[{"tag":"watchdog","port":%d,"mode":"plain"}]}`, freshPort),
		Users:     []*common.User{},
		KeepAlive: 60,
	}
	startErr := make(chan error, 1)
	go func() { startErr <- panelStart(c, panelIP, data) }()

	select {
	case err := <-startErr:
		startErr <- err
	case <-time.After(time.Second):
	}
	close(slowExtra.gate)

	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("panel Start failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("panel Start never returned")
	}

	fresh := c.Backend()
	time.Sleep(2 * time.Second)

	got := c.Backend()
	listening := false
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", freshPort), time.Second); err == nil {
		listening = true
		conn.Close()
	}
	if fresh == nil || got != fresh || !listening || c.Ip() != panelIP {
		t.Fatalf("a late keep-alive watchdog tore down the session the panel had just started: core right after Start=%v, controller still holds it=%v, its port still accepting=%v, client ip=%q (want %q)",
			fresh != nil, fresh != nil && got == fresh, listening, c.Ip(), panelIP)
	}
}

func TestWatchdogThatWaitedBehindAStartMustNotDisconnectIt(t *testing.T) {
	t.Parallel()
	c := newWatchdogController(t)

	old := newGatedBackend(nil)
	c.mu.Lock()
	c.backend = old
	c.primaryType = common.BackendType_XRAY
	c.mu.Unlock()

	c.Connect(panelIP, 1)
	silentFor(c, time.Minute)

	c.LockControl()
	time.Sleep(6 * time.Second)

	c.Disconnect()
	fresh := newGatedBackend(nil)
	c.mu.Lock()
	c.backend = fresh
	c.primaryType = common.BackendType_XRAY
	c.mu.Unlock()
	c.Connect(panelIP, 60)
	c.UnlockControl()

	time.Sleep(500 * time.Millisecond)

	if got := c.Backend(); got != fresh || fresh.shutdowns.Load() != 0 || c.Ip() != panelIP {
		t.Fatalf("a watchdog that went stale while a Start held the control lock disconnected the session that Start then built: controller still holds it=%v, fresh core shutdowns=%d, client ip=%q (want %q)",
			got == fresh, fresh.shutdowns.Load(), c.Ip(), panelIP)
	}
}

func TestKeepAliveWatchdogAllowsAGraceBeyondKeepAlive(t *testing.T) {
	t.Parallel()
	c := newWatchdogController(t)

	primary := newGatedBackend(nil)
	c.mu.Lock()
	c.backend = primary
	c.primaryType = common.BackendType_XRAY
	c.mu.Unlock()

	c.Connect(panelIP, 60)
	silentFor(c, 58*time.Second)

	time.Sleep(7 * time.Second)
	if c.Backend() != primary || primary.shutdowns.Load() != 0 {
		t.Fatal("the watchdog disconnected a panel that had been silent for about 63 s with keep_alive 60; a panel polling exactly every keep_alive seconds must not trip it on jitter")
	}

	deadline := time.Now().Add(8 * time.Second)
	for c.Backend() != nil && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if c.Backend() != nil || primary.shutdowns.Load() != 1 {
		t.Fatalf("the watchdog must still disconnect a panel silent well past keep_alive plus the grace: backend still attached=%v, shutdowns=%d", c.Backend() != nil, primary.shutdowns.Load())
	}
}

func TestDisconnectConcurrentWithConnect(t *testing.T) {
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	t.Cleanup(c.Disconnect)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Connect(panelIP, 0)
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Disconnect()
		}()
	}
	wg.Wait()
}
