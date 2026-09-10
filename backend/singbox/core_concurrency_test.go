//go:build linux

package singbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func stubScript(body string) string {
	return fmt.Sprintf("#!/bin/sh\ncase \"$1\" in version) echo \"sing-box version 1.0.0-stub\"; exit 0;; esac\n%s\n", body)
}

func stubCoreWith(t *testing.T, body string) *Core {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "stub-sing-box")
	if err := os.WriteFile(exe, []byte(stubScript(body)), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
	core, err := NewCore(exe, dir, 16, 16)
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	return core
}

func stubConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := NewConfig(`{"log":{"level":"error"},"inbounds":[],"outbounds":[{"type":"direct"}]}`)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	return cfg
}

func TestCore_StubBinaryAnswersVersionProbe(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	if core.Version() != "1.0.0-stub" {
		t.Fatalf("stub binary must answer the version probe, got %q", core.Version())
	}
}

func TestCore_ConcurrentStartStopStartedIsRaceFree(t *testing.T) {
	core := stubCoreWith(t, "exec sleep 30")
	cfg := stubConfig(t)
	t.Cleanup(core.Stop)

	done := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = core.Started()
					_ = core.Stopping()
					_ = core.Version()
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = core.Start(cfg)
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				core.Stop()
				time.Sleep(7 * time.Millisecond)
			}
		}
	}()

	time.Sleep(800 * time.Millisecond)
	close(done)
	wg.Wait()
}

func TestCore_StartedFlipsFalseOnceTheProcessExits(t *testing.T) {
	core := stubCoreWith(t, "exit 0")
	t.Cleanup(core.Stop)

	if err := core.Start(stubConfig(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for core.Started() {
		if time.Now().After(deadline) {
			t.Fatal("Started() still reports true long after the process exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCore_VersionProbeCannotHangForever(t *testing.T) {
	original := versionProbeTimeout
	versionProbeTimeout = 250 * time.Millisecond
	t.Cleanup(func() { versionProbeTimeout = original })

	dir := t.TempDir()
	exe := filepath.Join(dir, "stub-sing-box")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := NewCore(exe, dir, 16, 16); err != nil {
			t.Errorf("NewCore: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(versionProbeTimeout + 5*time.Second):
		t.Fatalf("NewCore blocked past the %s version-probe timeout on a hanging binary", versionProbeTimeout)
	}
}
