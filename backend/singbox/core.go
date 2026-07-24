package singbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Core manages the sing-box subprocess lifecycle: spawn, log capture, health
// bookkeeping, stop/restart, and orphan cleanup. Mirrors backend/xray/core.go,
// trimmed to what sing-box needs - notably there is no stdin config mode (see
// package doc comment), so configPath is always a directory we write
// "singbox.json" into and launch with "sing-box run -c <path>", and there is no
// xray-style access/error file logger split (all stdout/stderr is captured into
// a single ring buffer + channel).
type Core struct {
	executablePath  string
	configDir       string
	version         string
	process         *exec.Cmd
	processPID      int
	restarting      bool
	stopping        bool
	waitDone        chan struct{}
	logsChan        chan string
	logs            *logRing
	startupLogSize  int
	startupFailure  string
	cancelFunc      context.CancelFunc
	mu              sync.Mutex
	startupFailureM sync.RWMutex
}

func NewCore(executablePath, configDir string, logBufferSize, startupLogTailSize int) (*Core, error) {
	if logBufferSize <= 0 {
		logBufferSize = 1
	}
	if startupLogTailSize <= 0 {
		startupLogTailSize = 200
	}

	c := &Core{
		executablePath: executablePath,
		configDir:      configDir,
		logsChan:       make(chan string, logBufferSize),
		logs:           newLogRing(startupLogTailSize),
		startupLogSize: startupLogTailSize,
	}

	c.version = c.refreshVersion()

	return c, nil
}

func (c *Core) configFilePath() string {
	return filepath.Join(c.configDir, "singbox.json")
}

func (c *Core) writeConfigFile(config []byte) error {
	if err := os.MkdirAll(c.configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	return os.WriteFile(c.configFilePath(), config, 0644)
}

// refreshVersion best-effort parses `sing-box version` output. Unlike xray's
// equivalent, a parse failure here is NOT treated as fatal: a source build
// without version ldflags set (as used by this integration's build recipe)
// prints "sing-box version unknown", which is a legitimate, working binary -
// failing NewCore over a cosmetic string would be wrong.
func (c *Core) refreshVersion() string {
	cmd := exec.Command(c.executablePath, "version")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}

	r := regexp.MustCompile(`sing-box version (\S+)`)
	if m := r.FindStringSubmatch(string(out)); len(m) > 1 {
		return m[1]
	}

	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

func (c *Core) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

func (c *Core) Started() bool {
	if c.process == nil || c.process.Process == nil {
		return false
	}
	if c.process.ProcessState == nil {
		return true
	}
	return false
}

func (c *Core) Stopping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopping
}

func (c *Core) Start(sbConfig *Config) error {
	bytesConfig, err := sbConfig.ToBytes()
	if err != nil {
		return err
	}

	if err = c.writeConfigFile(bytesConfig); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Started() {
		return errors.New("sing-box is started already")
	}

	c.logs.reset()
	c.setStartupFailure("")

	// Clean up any orphaned sing-box processes before starting a new one.
	if err := c.cleanupOrphanedProcesses(); err != nil {
		log.Printf("warning: failed to cleanup orphaned sing-box processes: %v", err)
	}

	if c.process != nil && c.process.Process != nil {
		pid := c.process.Process.Pid
		_ = c.process.Process.Kill()
		_ = killProcessTree(pid)
		c.process = nil
		c.processPID = 0
	}

	cmd := exec.Command(c.executablePath, "run", "-c", c.configFilePath())
	setProcAttributes(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err = cmd.Start(); err != nil {
		return err
	}

	c.process = cmd
	c.processPID = cmd.Process.Pid
	c.stopping = false
	c.waitDone = make(chan struct{})

	waitDone := c.waitDone
	go func() {
		waitErr := cmd.Wait()
		close(waitDone)
		c.handleProcessExit(cmd, waitErr)
	}()

	ctxCore, cancel := context.WithCancel(context.Background())
	c.cancelFunc = cancel

	go c.captureProcessLogs(ctxCore, stdout)
	go c.captureProcessLogs(ctxCore, stderr)

	return nil
}

func (c *Core) handleProcessExit(cmd *exec.Cmd, err error) {
	c.mu.Lock()
	expected := c.stopping || c.process != cmd
	c.mu.Unlock()

	if expected {
		return
	}

	message := "sing-box process exited unexpectedly"
	if err != nil {
		message = fmt.Sprintf("%s: %v", message, err)
		if strings.Contains(strings.ToLower(err.Error()), "signal: killed") {
			message += " (process was killed externally; check container/system OOM and memory limits)"
		}
	}

	log.Println(message)
	c.recordLog(message)
}

func (c *Core) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	started := c.Started()
	if !started && c.process == nil && c.cancelFunc == nil {
		return
	}

	if started {
		pid := c.process.Process.Pid
		c.processPID = pid
		waitDone := c.waitDone
		c.stopping = true

		_ = c.process.Process.Kill()

		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			log.Printf("sing-box process %d did not terminate within timeout, force killing", pid)
			_ = killProcessTree(pid)
		}

		if err := verifyProcessDead(pid); err != nil {
			log.Printf("warning: sing-box process %d may still be running: %v", pid, err)
			_ = killProcessTree(pid)
		}
	}

	c.process = nil
	c.processPID = 0
	c.stopping = false
	c.waitDone = nil

	if c.cancelFunc != nil {
		c.cancelFunc()
		c.cancelFunc = nil
	}

	log.Println("sing-box core stopped")
}

func (c *Core) Restart(sbConfig *Config) error {
	c.mu.Lock()
	if c.restarting {
		c.mu.Unlock()
		return errors.New("sing-box is already restarting")
	}
	c.restarting = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.restarting = false
		c.mu.Unlock()
	}()

	log.Println("restarting sing-box core...")
	c.Stop()
	if err := c.Start(sbConfig); err != nil {
		return err
	}
	return nil
}

func (c *Core) Restarting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restarting
}

func (c *Core) Logs() <-chan string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logsChan
}

// ProcessInfo holds information about a process.
type ProcessInfo struct {
	PID      int
	PPID     int
	IsZombie bool
}

// cleanupOrphanedProcesses finds and kills sing-box processes that are zombies
// or parented by this node process. Mirrors xray's Core.cleanupOrphanedProcesses.
func (c *Core) cleanupOrphanedProcesses() error {
	processes, err := findSingBoxProcesses(c.executablePath)
	if err != nil {
		return fmt.Errorf("failed to find sing-box processes: %w", err)
	}

	currentPID := 0
	if c.process != nil && c.process.Process != nil {
		currentPID = c.process.Process.Pid
	}

	nodePID := os.Getpid()

	killedCount := 0
	for _, procInfo := range processes {
		if procInfo.PID == currentPID {
			continue
		}

		kill := false
		reason := ""
		if procInfo.IsZombie && (procInfo.PPID == 0 || procInfo.PPID == 1) {
			kill = true
			reason = "zombie sing-box process without parent"
		} else if procInfo.PPID == nodePID {
			kill = true
			reason = fmt.Sprintf("orphaned sing-box process with node as parent (PPID: %d)", procInfo.PPID)
		}

		if !kill {
			continue
		}

		log.Printf("%s %d (PPID: %d), killing it", reason, procInfo.PID, procInfo.PPID)
		if err := killProcessTree(procInfo.PID); err != nil {
			log.Printf("warning: failed to kill orphaned process %d: %v", procInfo.PID, err)
		} else {
			killedCount++
		}
	}

	if killedCount > 0 {
		log.Printf("cleaned up %d orphaned sing-box process(es)", killedCount)
	}

	return nil
}
