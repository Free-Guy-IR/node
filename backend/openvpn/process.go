package openvpn

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pasarguard/node/pkg/stats"
)

// instanceProcess owns the full lifecycle of exactly one OpenVPN server
// subprocess: config rendering, spawn, management-socket client, NAT rule,
// log capture, crash/restart bookkeeping, and graceful stop. It is the
// per-instance analogue of backend/singbox/core.go's Core, generalized to run
// N of them concurrently - one per configured instance (see Backend in
// openvpn.go).
type instanceProcess struct {
	tag            string
	inst           *InstanceConfig
	pki            PKI
	tunIndex       int
	configDir      string
	executablePath string

	auth *authStore

	logs           *logRing
	logsChan       chan string
	startupLogSize int

	statsTracker *stats.InterfaceCountersTracker

	mu         sync.Mutex
	process    *exec.Cmd
	processPID int
	stopping   bool
	restarting bool
	waitDone   chan struct{}
	cancelFunc context.CancelFunc
	mgmt       *ManagementClient
	natApplied bool

	startupFailureMu sync.RWMutex
	startupFailure   string
}

func newInstanceProcess(inst *InstanceConfig, pki PKI, tunIndex int, baseDir, executablePath string, logsChan chan string, startupLogTailSize int) *instanceProcess {
	if startupLogTailSize <= 0 {
		startupLogTailSize = 200
	}

	return &instanceProcess{
		tag:      inst.Tag,
		inst:     inst,
		pki:      pki,
		tunIndex: tunIndex,
		// Named by tun index rather than the (admin-controlled, arbitrary)
		// tag: keeps the directory/socket path short and filesystem-safe.
		// Notably, Unix domain socket paths are capped at ~108 bytes on
		// Linux, and a long or unusually-charactered tag could blow that
		// budget or need escaping otherwise.
		configDir:      filepath.Join(baseDir, fmt.Sprintf("instance-%d", tunIndex)),
		executablePath: executablePath,
		auth:           newAuthStore(),
		logs:           newLogRing(startupLogTailSize),
		logsChan:       logsChan,
		startupLogSize: startupLogTailSize,
		statsTracker:   stats.NewInterfaceCountersTracker(),
	}
}

func (p *instanceProcess) configFilePath() string {
	return filepath.Join(p.configDir, "server.conf")
}

func (p *instanceProcess) managementSocketPath() string {
	return filepath.Join(p.configDir, "management.sock")
}

func (p *instanceProcess) writeConfigFile() error {
	text, err := renderInstanceConfig(p.inst, p.pki, p.tunIndex, p.managementSocketPath())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.configDir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	// 0600: unlike sing-box's generated config (backend/singbox/core.go's
	// writeConfigFile, 0644), this file embeds the server's PEM-encoded
	// private key inline (see render.go) and must not be world/group
	// readable.
	return os.WriteFile(p.configFilePath(), []byte(text), 0o600)
}

func (p *instanceProcess) isStartedLocked() bool {
	if p.process == nil || p.process.Process == nil {
		return false
	}
	return p.process.ProcessState == nil
}

func (p *instanceProcess) Started() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isStartedLocked()
}

func (p *instanceProcess) Stopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopping
}

func (p *instanceProcess) Restarting() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restarting
}

func (p *instanceProcess) managementClient() *ManagementClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mgmt
}

func (p *instanceProcess) enforceAuthorized() {
	if mgmt := p.managementClient(); mgmt != nil {
		mgmt.EnforceAuthorized(p.auth.contains)
	}
}

// aggregateStats sums live per-user cumulative counters across every user
// currently known to this instance's ManagementClient - used by stats.go for
// the per-instance ("Inbound") stat type.
func (p *instanceProcess) aggregateStats() (rx, tx int64) {
	mgmt := p.managementClient()
	if mgmt == nil {
		return 0, 0
	}
	for _, s := range mgmt.AllUserStats() {
		rx += int64(s.Downlink)
		tx += int64(s.Uplink)
	}
	return rx, tx
}

func (p *instanceProcess) applyNAT() {
	p.mu.Lock()
	applied := p.natApplied
	p.mu.Unlock()
	if applied {
		return
	}
	if err := addNATRule(p.inst.Network); err != nil {
		log.Printf("warning: openvpn instance %q: failed to add NAT rule: %v", p.tag, err)
		return
	}
	p.mu.Lock()
	p.natApplied = true
	p.mu.Unlock()
}

func (p *instanceProcess) removeNAT() {
	p.mu.Lock()
	applied := p.natApplied
	p.mu.Unlock()
	if !applied {
		return
	}
	if err := removeNATRule(p.inst.Network); err != nil {
		log.Printf("warning: openvpn instance %q: failed to remove NAT rule: %v", p.tag, err)
	}
	p.mu.Lock()
	p.natApplied = false
	p.mu.Unlock()
}

// Start renders this instance's config, applies its NAT rule, spawns the
// openvpn subprocess, and waits for the management socket to come up and
// accept the initial "state on"/"bytecount 5" subscription before returning.
func (p *instanceProcess) Start() error {
	if err := p.writeConfigFile(); err != nil {
		return err
	}

	p.mu.Lock()
	if p.isStartedLocked() {
		p.mu.Unlock()
		return fmt.Errorf("openvpn instance %q is already started", p.tag)
	}
	p.mu.Unlock()

	p.logs.reset()
	p.setStartupFailure("")

	if err := p.cleanupOrphanedProcesses(); err != nil {
		log.Printf("warning: openvpn instance %q: failed to cleanup orphaned processes: %v", p.tag, err)
	}

	p.applyNAT()

	cmd := exec.Command(p.executablePath, "--config", p.configFilePath())
	setProcAttributes(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		p.removeNAT()
		return fmt.Errorf("openvpn instance %q: failed to start process: %w", p.tag, err)
	}

	p.mu.Lock()
	p.process = cmd
	p.processPID = cmd.Process.Pid
	p.stopping = false
	p.waitDone = make(chan struct{})
	waitDone := p.waitDone
	p.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		close(waitDone)
		p.handleProcessExit(cmd, waitErr)
	}()

	logCtx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelFunc = cancel
	p.mu.Unlock()

	go p.captureProcessLogs(logCtx, stdout)
	go p.captureProcessLogs(logCtx, stderr)

	connectCtx, connectCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer connectCancel()

	mgmt := newManagementClient(p.tag, p.managementSocketPath(), p.auth.authenticate)
	if err := mgmt.Connect(connectCtx); err != nil {
		tail := p.logs.tail(p.startupLogSize)
		failure := p.LatestStartupFailure()
		p.Stop()

		switch {
		case failure != "":
			return fmt.Errorf("openvpn instance %q: failed to start: %s", p.tag, failure)
		case len(tail) > 0:
			return fmt.Errorf("openvpn instance %q: management socket never became ready: %w; recent logs:\n%s", p.tag, err, strings.Join(tail, "\n"))
		default:
			return fmt.Errorf("openvpn instance %q: management socket never became ready: %w", p.tag, err)
		}
	}

	p.mu.Lock()
	p.mgmt = mgmt
	p.mu.Unlock()

	return nil
}

func (p *instanceProcess) handleProcessExit(cmd *exec.Cmd, err error) {
	p.mu.Lock()
	expected := p.stopping || p.process != cmd
	p.mu.Unlock()

	if expected {
		return
	}

	message := fmt.Sprintf("openvpn instance %q process exited unexpectedly", p.tag)
	if err != nil {
		message = fmt.Sprintf("%s: %v", message, err)
	}
	log.Println(message)
	p.recordLog(message)
}

// Stop gracefully stops the process: SIGTERM (OpenVPN's clean-shutdown
// signal - it notifies connected clients and exits, unlike sing-box's Stop()
// in backend/singbox/core.go, which calls Process.Kill() (SIGKILL) directly;
// worth the extra step here since a hard-killed openvpn server leaves
// clients without a clean disconnect signal), wait with a timeout, then
// escalate to killProcessTree's SIGTERM+SIGKILL+process-group fallback.
func (p *instanceProcess) Stop() {
	p.mu.Lock()

	started := p.isStartedLocked()
	if !started && p.process == nil {
		p.mu.Unlock()
		return
	}

	mgmt := p.mgmt
	p.mgmt = nil

	var osProc *os.Process
	var pid int
	var waitDone chan struct{}
	if started {
		osProc = p.process.Process
		pid = osProc.Pid
		p.processPID = pid
		waitDone = p.waitDone
		p.stopping = true
	}
	p.mu.Unlock()

	if mgmt != nil {
		mgmt.Close()
	}

	if started {
		if err := osProc.Signal(syscall.SIGTERM); err != nil {
			log.Printf("openvpn instance %q: failed to send SIGTERM to %d: %v", p.tag, pid, err)
		}

		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			log.Printf("openvpn instance %q: process %d did not terminate within timeout, force killing", p.tag, pid)
			_ = killProcessTree(pid)
		}

		if err := verifyProcessDead(pid); err != nil {
			log.Printf("warning: openvpn instance %q: process %d may still be running: %v", p.tag, pid, err)
			_ = killProcessTree(pid)
		}
	}

	p.removeNAT()

	p.mu.Lock()
	p.process = nil
	p.processPID = 0
	p.stopping = false
	p.waitDone = nil
	if p.cancelFunc != nil {
		p.cancelFunc()
		p.cancelFunc = nil
	}
	p.mu.Unlock()

	log.Printf("openvpn instance %q stopped", p.tag)
}

func (p *instanceProcess) Restart() error {
	p.mu.Lock()
	if p.restarting {
		p.mu.Unlock()
		return fmt.Errorf("openvpn instance %q is already restarting", p.tag)
	}
	p.restarting = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.restarting = false
		p.mu.Unlock()
	}()

	log.Printf("restarting openvpn instance %q...", p.tag)
	p.Stop()
	return p.Start()
}

// cleanupOrphanedProcesses finds and kills openvpn processes for this
// specific instance (matched by executable path + --config path, see
// process_unix.go) that are zombies or parented by this node process.
// Mirrors backend/singbox/core.go's Core.cleanupOrphanedProcesses.
func (p *instanceProcess) cleanupOrphanedProcesses() error {
	processes, err := findOpenVPNProcesses(p.executablePath, p.configFilePath())
	if err != nil {
		return fmt.Errorf("failed to find openvpn processes for instance %q: %w", p.tag, err)
	}

	p.mu.Lock()
	currentPID := 0
	if p.process != nil && p.process.Process != nil {
		currentPID = p.process.Process.Pid
	}
	p.mu.Unlock()

	nodePID := os.Getpid()
	killed := 0
	for _, proc := range processes {
		if proc.PID == currentPID {
			continue
		}

		kill := false
		reason := ""
		if proc.IsZombie && (proc.PPID == 0 || proc.PPID == 1) {
			kill = true
			reason = "zombie openvpn process without parent"
		} else if proc.PPID == nodePID {
			kill = true
			reason = fmt.Sprintf("orphaned openvpn process with node as parent (PPID: %d)", proc.PPID)
		}
		if !kill {
			continue
		}

		log.Printf("openvpn instance %q: %s %d (PPID: %d), killing it", p.tag, reason, proc.PID, proc.PPID)
		if err := killProcessTree(proc.PID); err != nil {
			log.Printf("warning: openvpn instance %q: failed to kill orphaned process %d: %v", p.tag, proc.PID, err)
		} else {
			killed++
		}
	}

	if killed > 0 {
		log.Printf("openvpn instance %q: cleaned up %d orphaned process(es)", p.tag, killed)
	}

	return nil
}
