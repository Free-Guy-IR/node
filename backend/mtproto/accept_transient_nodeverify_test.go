//go:build darwin || linux

package mtproto

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/pasarguard/node/config"
)

func nvServesAConnection(port int) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	garbage := make([]byte, 64)
	for i := range garbage {
		garbage[i] = byte(i*7 + 1)
	}
	if _, err := conn.Write(garbage); err != nil {
		return nil
	}
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 256)
	for {
		_, err := conn.Read(buf)
		if err == nil {
			continue
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return fmt.Errorf("nobody served the connection within 4s")
		}
		return nil
	}
}

func nvExhaustDescriptorsDuring(t *testing.T, during func()) {
	t.Helper()
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	gauge, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	lowered := original
	lowered.Cur = uint64(gauge.Fd()) + 48
	gauge.Close()
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lowered); err != nil {
		t.Fatal(err)
	}
	var held []*os.File
	defer func() {
		for _, f := range held {
			f.Close()
		}
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
			t.Errorf("restore RLIMIT_NOFILE: %v", err)
		}
	}()
	for {
		f, err := os.Open(os.DevNull)
		if err != nil {
			if !errors.Is(err, syscall.EMFILE) {
				t.Fatalf("filling descriptors: %v", err)
			}
			break
		}
		held = append(held, f)
		if len(held) > 4096 {
			t.Fatal("RLIMIT_NOFILE did not take effect")
		}
	}
	if len(held) == 0 {
		t.Fatal("no descriptor could be held")
	}
	held[len(held)-1].Close()
	held = held[:len(held)-1]
	during()
}

func nvAcceptTransientCase(t *testing.T, instance string) {
	port := unusedListenPorts(t, 1)[0]
	cfg, err := NewConfig(fmt.Sprintf(`{"instances": [%s]}`, fmt.Sprintf(instance, port)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := New(ctx, cfg, nil, &config.Config{LogBufferSize: 1000, StatsUpdateIntervalSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	if err := nvServesAConnection(port); err != nil {
		t.Fatalf("before any accept error: %v", err)
	}

	nvExhaustDescriptorsDuring(t, func() {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
		if err != nil {
			t.Fatalf("dial while descriptors are exhausted: %v", err)
		}
		defer conn.Close()
		time.Sleep(300 * time.Millisecond)
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		err := nvServesAConnection(port)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after one transient EMFILE from accept the inbound no longer serves: %v (Started()=%v)", err, b.Started())
		}
	}
}

func TestNodeVerifyDirectInboundSurvivesATransientAcceptError(t *testing.T) {
	nvAcceptTransientCase(t, `{"tag": "nv-plain", "port": %d, "mode": "plain"}`)
}

func TestNodeVerifyMiddleProxyInboundSurvivesATransientAcceptError(t *testing.T) {
	nvAcceptTransientCase(t, `{"tag": "nv-mp", "port": %d, "fake_tls_domain": "localhost", "ad_tag": "0123456789abcdef0123456789abcdef"}`)
}
