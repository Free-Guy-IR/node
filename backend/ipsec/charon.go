package ipsec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	ViciSocketPath = "/var/run/charon.vici"
	SwanctlDir     = "/etc/swanctl"
	socketTimeout  = 15 * time.Second
	stopGrace      = 5 * time.Second
	respawnMinWait = 2 * time.Second
	respawnMaxWait = 30 * time.Second
)

var (
	charonBinary = firstExisting(
		"/usr/lib/ipsec/charon",
		"/usr/lib/strongswan/charon",
		"/usr/libexec/ipsec/charon",
		"/usr/libexec/strongswan/charon",
	)
	swanctlBinary = firstExisting("/usr/sbin/swanctl", "/usr/bin/swanctl")
)

type Logf func(severity, message string)

type daemon struct {
	mu       sync.Mutex
	loadMu   sync.Mutex
	logf     atomic.Pointer[Logf]
	desired  bool
	process  *exec.Cmd
	waitDone chan struct{}
	cancel   context.CancelFunc
}

var shared = &daemon{}

func firstExisting(candidates ...string) string {
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0]
}

func CheckDeps() error {
	for _, p := range []string{charonBinary, swanctlBinary} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("strongSwan is not installed on this node (missing %s)", p)
		}
	}
	return nil
}

func Start(logf Logf) error { return shared.start(logf) }

func Stop() { shared.stop() }

func Running() bool { return shared.running() }

func LoadAll() error { return shared.loadAll() }

func (d *daemon) start(logf Logf) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.desired && d.process != nil {
		return nil
	}
	if err := CheckDeps(); err != nil {
		return err
	}
	d.logf.Store(&logf)
	d.desired = true
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	if err := d.spawnLocked(); err != nil {
		d.desired = false
		cancel()
		return err
	}
	go d.supervise(ctx)
	return nil
}

func (d *daemon) spawnLocked() error {
	cmd := exec.Command(charonBinary)
	cmd.Env = append(os.Environ(), "STRONGSWAN_CONF=/etc/strongswan.conf")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start charon: %w", err)
	}
	waitDone := make(chan struct{})
	d.process = cmd
	d.waitDone = waitDone
	go d.pump(stdout)
	go d.pump(stderr)
	go func() { _ = cmd.Wait(); close(waitDone) }()

	if err := waitForSocket(waitDone, socketTimeout); err != nil {
		_ = cmd.Process.Kill()
		<-waitDone
		d.process = nil
		return err
	}
	d.log("Info", "charon: daemon started")
	return nil
}

func (d *daemon) supervise(ctx context.Context) {
	wait := respawnMinWait
	for {
		d.mu.Lock()
		waitDone := d.waitDone
		d.mu.Unlock()
		if waitDone == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-waitDone:
		}

		d.mu.Lock()
		if !d.desired {
			d.mu.Unlock()
			return
		}
		d.process = nil
		d.mu.Unlock()
		d.log("Warning", fmt.Sprintf("charon: daemon exited unexpectedly, respawning in %s", wait))

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		d.mu.Lock()
		if !d.desired {
			d.mu.Unlock()
			return
		}
		err := d.spawnLocked()
		d.mu.Unlock()
		if err != nil {
			d.log("Error", "charon: respawn failed: "+err.Error())
			wait = min(wait*2, respawnMaxWait)
			d.mu.Lock()
			d.waitDone = closedChan()
			d.mu.Unlock()
			continue
		}
		wait = respawnMinWait
		if err := d.loadAll(); err != nil {
			d.log("Error", "charon: reload after respawn failed: "+err.Error())
		} else {
			d.log("Info", "charon: connections reloaded after respawn")
		}
	}
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (d *daemon) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.desired = false
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if d.process == nil || d.process.Process == nil {
		return
	}
	_ = d.process.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.waitDone:
	case <-time.After(stopGrace):
		_ = d.process.Process.Kill()
		<-d.waitDone
	}
	d.process = nil
	d.waitDone = nil
	d.log("Info", "charon: daemon stopped")
}

func (d *daemon) running() bool {
	d.mu.Lock()
	alive := d.desired && d.process != nil
	d.mu.Unlock()
	if !alive {
		return false
	}
	conn, err := net.DialTimeout("unix", ViciSocketPath, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitForSocket(waitDone chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", ViciSocketPath, time.Second); err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-waitDone:
			return errors.New("charon exited before the VICI socket was ready")
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("timed out waiting for the charon VICI socket")
}

func (d *daemon) loadAll() error {
	d.loadMu.Lock()
	defer d.loadMu.Unlock()
	out, err := exec.Command(swanctlBinary, "--load-all", "--noprompt").CombinedOutput()
	if err != nil {
		return fmt.Errorf("swanctl --load-all: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *daemon) pump(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n") {
				d.log("Info", "charon: "+line)
			}
		}
		if err != nil {
			return
		}
	}
}

func (d *daemon) log(severity, message string) {
	if logf := d.logf.Load(); logf != nil && *logf != nil {
		(*logf)(severity, message)
	}
}
