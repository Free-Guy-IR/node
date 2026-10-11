package xray

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
)

func controlPortsGuarded(cfg *Config, ports ...int) error {
	if cfg == nil {
		return fmt.Errorf("no active config")
	}
	for _, port := range ports {
		for _, q := range []destQuery{
			{inbound: sessionTestInbound, isIP: true, value: "127.0.0.1", port: port},
			{inbound: sessionTestInbound, isIP: false, value: "localhost", port: port},
		} {
			tag, protocol, err := routeFor(cfg, q)
			if err != nil {
				return err
			}
			if !destinationIsBlocked(protocol) {
				return fmt.Errorf("%q:%d routes to %q (%s)", q.value, port, tag, protocol)
			}
		}
	}
	return nil
}

func brokenCandidate(x *Xray) (*Config, error) {
	candidate, err := x.activeConfig().Clone()
	if err != nil {
		return nil, err
	}
	candidate.InboundConfigs = append(candidate.InboundConfigs, &Inbound{
		Tag:      "BROKEN PROBE",
		Listen:   "127.0.0.1",
		Port:     1,
		Protocol: "no-such-protocol",
		Settings: map[string]any{},
	})
	return candidate, nil
}

func TestLiveControlPortGuardHoldsAcrossConcurrentSyncsAndRestarts(t *testing.T) {
	forEachXrayBinary(t, controlPortGuardHoldsUnderInterleaving)
}

func controlPortGuardHoldsUnderInterleaving(t *testing.T, binary string) {
	source := startByteSource(t)
	aliceID := uuid.New()
	bobID := uuid.New()
	alice := sessionUser("1.alice", aliceID, sessionTestInbound)
	bob := sessionUser("2.bob", bobID, sessionTestInbound)
	back, inboundPort := startSessionBackend(t, binary, alice, bob)

	apiPort := controlPort(t, back, "API_INBOUND")
	metricPort := back.metricPort

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reached, tunnelled, attempts, unguarded, restores, restoreFailures, controlCarried atomic.Int64
	var firstUnguarded atomic.Value

	loop := func(body func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				body(i)
			}
		}()
	}

	loop(func(i int) {
		user := alice
		if i%2 == 1 {
			user = bob
		}
		_ = back.SyncUser(ctx, user)
	})
	loop(func(int) { _ = back.UpdateUsers(ctx, []*common.User{alice, bob}) })
	loop(func(int) {
		_ = back.SyncUsers(ctx, []*common.User{alice, bob})
		time.Sleep(1500 * time.Millisecond)
	})
	loop(func(int) {
		_ = back.Restart()
		time.Sleep(2 * time.Second)
	})
	loop(func(int) {
		candidate, err := brokenCandidate(back)
		if err != nil {
			restoreFailures.Add(1)
			return
		}
		unlock, lockErr := back.lockSync(ctx)
		if lockErr != nil {
			restoreFailures.Add(1)
			return
		}
		err = back.applyConfigWithRestart(ctx, candidate)
		unlock()
		if err == nil {
			restoreFailures.Add(1)
		} else {
			restores.Add(1)
		}
		time.Sleep(3 * time.Second)
	})
	loop(func(int) {
		_, _ = back.GetStats(ctx, &common.StatRequest{Type: common.StatType_UsersStat})
		time.Sleep(20 * time.Millisecond)
	})
	loop(func(int) {
		if err := controlPortsGuarded(back.activeConfig(), apiPort, metricPort); err != nil {
			unguarded.Add(1)
			firstUnguarded.CompareAndSwap(nil, err.Error())
		}
		time.Sleep(10 * time.Millisecond)
	})
	loop(func(int) {
		conn, err := dialVlessStream(inboundPort, aliceID, source)
		if err == nil {
			if n, _ := firstBytes(conn, time.Second); n > 0 {
				controlCarried.Add(1)
			}
			_ = conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	})
	loop(func(i int) {
		dial := func() (net.Conn, error) {
			return dialVlessStream(inboundPort, aliceID, &net.TCPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: apiPort})
		}
		if i%2 == 1 {
			dial = func() (net.Conn, error) {
				return dialVlessStreamDomain(inboundPort, aliceID, "localhost", apiPort)
			}
		}
		counted := func() (net.Conn, error) {
			conn, err := dial()
			if err == nil {
				tunnelled.Add(1)
			}
			return conn, err
		}
		attempts.Add(1)
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 3*time.Second)
		if attemptQueryStatsReset(attemptCtx, counted) == nil {
			reached.Add(1)
		}
		attemptCancel()
		time.Sleep(50 * time.Millisecond)
	})

	time.Sleep(30 * time.Second)
	close(stop)
	wg.Wait()

	t.Logf("attacker attempts %d, tunnels opened %d, reached the API %d; control tunnels that carried data %d; failed restarts restored %d (unexpected successes %d); unguarded samples %d",
		attempts.Load(), tunnelled.Load(), reached.Load(), controlCarried.Load(), restores.Load(), restoreFailures.Load(), unguarded.Load())

	if tunnelled.Load() == 0 || controlCarried.Load() == 0 {
		t.Fatal("no tunnel carried traffic during the run, so it proves nothing")
	}
	if restores.Load() == 0 {
		t.Fatal("no failed restart was restored, so the restore path was not exercised")
	}
	if reached.Load() != 0 {
		t.Errorf("a proxy user reached the xray API %d times while syncs and restarts interleaved", reached.Load())
	}
	if unguarded.Load() != 0 {
		t.Errorf("the active config lost the control-port guard %d times (first: %v)", unguarded.Load(), firstUnguarded.Load())
	}
	if err := controlPortsGuarded(back.activeConfig(), apiPort, metricPort); err != nil {
		t.Errorf("the final config is unguarded: %v", err)
	}
}
