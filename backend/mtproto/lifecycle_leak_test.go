package mtproto

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/config"
)

const lifecycleLeakRounds = 20

func settledGoroutines(limit int) int {
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= limit || time.Now().After(deadline) {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func assertNoGoroutineGrowth(t *testing.T, before int, what string) {
	t.Helper()
	allowed := before + lifecycleLeakRounds/4
	if after := settledGoroutines(allowed); after > allowed {
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
		t.Fatalf("%s leaked goroutines: %d before, %d after %d rounds\n%s", what, before, after, lifecycleLeakRounds, stacks.String())
	}
}

func TestStartingAnAdTagInstanceDoesNotLeakGoroutines(t *testing.T) {
	warm := startBackend(t, fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, unusedListenPorts(t, 1)[0], testAdTag), nil)
	warm.Shutdown()
	before := settledGoroutines(0)

	for range lifecycleLeakRounds {
		raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg-ad","port":%d,"fake_tls_domain":"localhost","ad_tag":%q}]}`, unusedListenPorts(t, 1)[0], testAdTag)
		backend := startBackend(t, raw, nil)
		backend.Shutdown()
	}

	assertNoGoroutineGrowth(t, before, "starting and stopping an ad_tag instance")
}

func TestStartingAFakeTLSInstanceDoesNotLeakGoroutines(t *testing.T) {
	warm := startBackend(t, faketlsConfigOnFreshPort(t), nil)
	warm.Shutdown()
	before := settledGoroutines(0)

	for range lifecycleLeakRounds {
		backend := startBackend(t, faketlsConfigOnFreshPort(t), nil)
		backend.Shutdown()
	}

	assertNoGoroutineGrowth(t, before, "starting and stopping a faketls instance")
}

func TestFailingToListenDoesNotLeakTheBuiltProxy(t *testing.T) {
	port := unusedListenPorts(t, 1)[0]
	occupant, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupant.Close()

	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"}]}`, port)
	mtCfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	nodeCfg := &config.Config{LogBufferSize: 4096, StatsUpdateIntervalSeconds: 3600}

	if backend, err := New(context.Background(), mtCfg, nil, nodeCfg); err == nil {
		backend.Shutdown()
		t.Fatal("New must fail while the instance port is taken")
	}
	before := settledGoroutines(0)

	for range lifecycleLeakRounds {
		mtCfg, err := NewConfig(raw)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if backend, err := New(context.Background(), mtCfg, nil, nodeCfg); err == nil {
			backend.Shutdown()
			t.Fatal("New must fail while the instance port is taken")
		}
	}

	assertNoGoroutineGrowth(t, before, "a Start that cannot listen")
}

func TestStartedDoesNotRaceRestart(t *testing.T) {
	backend := startBackend(t, faketlsConfigOnFreshPort(t), nil)
	defer backend.Shutdown()

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
				backend.Started()
			}
		}
	}()

	for range 10 {
		if err := backend.Restart(); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Restart: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	if !backend.Started() {
		t.Fatal("the backend must report started after its restarts")
	}
}
