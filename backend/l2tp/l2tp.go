package l2tp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pasarguard/node/backend/ipsec"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/stats"
)

var errNotStarted = errors.New("l2tp not started")

const (
	onlineActivityThreshold = 60 * time.Second
	xl2tpdBinary            = "/usr/sbin/xl2tpd"
	pppdBinary              = "/usr/sbin/pppd"
	pppDevice               = "/dev/ppp"
	chapSecretsPath         = "/etc/ppp/chap-secrets"
	xl2tpdStopGrace         = 5 * time.Second
)

type lifecycleState uint8

const (
	lifecycleStopped lifecycleState = iota
	lifecycleRunning
)

func CheckDeps() error {
	if err := ipsec.CheckDeps(); err != nil {
		return err
	}
	for _, p := range []string{xl2tpdBinary, pppdBinary} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("xl2tpd/ppp is not installed on this node (missing %s)", p)
		}
	}
	if _, err := os.Stat(pppDevice); err != nil {
		return fmt.Errorf("%s is not available; load ppp_generic on the host and pass the device into the container", pppDevice)
	}
	return checkHostPrereqs()
}

type L2TP struct {
	config *Config
	cfg    *config.Config

	users          *userStore
	statsTracker   *stats.Tracker
	interfaceStats *stats.InterfaceCountersTracker
	totalRx        int64
	totalTx        int64
	ifSeen         map[string][2]int64
	cumRx          map[string]int64
	cumTx          map[string]int64
	onlineIPs      map[string]map[string]int64

	logChan        chan string
	startTime      time.Time
	updateInterval time.Duration

	mu                 sync.RWMutex
	state              lifecycleState
	process            *exec.Cmd
	processDone        chan struct{}
	cancelProcess      context.CancelFunc
	cancelPoll         context.CancelFunc
	hostRoutingCleanup func()
}

func New(_ context.Context, l2Config *Config, users []*common.User, nodeCfg *config.Config) (*L2TP, error) {
	if l2Config == nil {
		return nil, errors.New("l2tp config must not be nil")
	}
	l2Config.workDir = filepath.Join(nodeCfg.GeneratedConfigPath, "l2tp", l2Config.InboundTag)

	interval := time.Duration(nodeCfg.StatsUpdateIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}
	bufferSize := nodeCfg.LogBufferSize
	if bufferSize <= 0 {
		bufferSize = 1
	}

	o := &L2TP{
		config:         l2Config,
		cfg:            nodeCfg,
		users:          newUserStore(l2Config.InboundTag),
		statsTracker:   stats.New(),
		interfaceStats: stats.NewInterfaceCountersTracker(),
		ifSeen:         make(map[string][2]int64),
		cumRx:          make(map[string]int64),
		cumTx:          make(map[string]int64),
		onlineIPs:      make(map[string]map[string]int64),
		logChan:        make(chan string, bufferSize),
		startTime:      time.Now(),
		updateInterval: interval,
	}
	o.users.replaceAll(users)

	o.mu.Lock()
	err := o.startLocked()
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (o *L2TP) startLocked() error {
	if err := checkHostPrereqs(); err != nil {
		return err
	}
	if err := o.writeConfig(); err != nil {
		return fmt.Errorf("write l2tp config: %w", err)
	}

	cleanup, err := applyHostRouting(o.config.Pool, o.config.EgressInterface, o.config.InboundTag, func(format string, args ...any) {
		o.emitLogf("Info", format, args...)
	})
	if err != nil {
		return fmt.Errorf("l2tp host routing: %w", err)
	}
	o.hostRoutingCleanup = cleanup

	if err := ipsec.Start(o.emitLog); err != nil {
		o.runHostRoutingCleanup()
		return fmt.Errorf("start charon: %w", err)
	}
	if err := ipsec.LoadAll(); err != nil {
		ipsec.Stop()
		o.runHostRoutingCleanup()
		return fmt.Errorf("swanctl load: %w", err)
	}

	if err := o.startXl2tpdLocked(); err != nil {
		ipsec.Stop()
		o.runHostRoutingCleanup()
		return err
	}

	pollCtx, cancelPoll := context.WithCancel(context.Background())
	o.cancelPoll = cancelPoll
	go o.pollLoop(pollCtx)

	o.state = lifecycleRunning
	o.emitLogf("Info", "l2tp: started (%s)", o.config.InboundTag)
	return nil
}

func (o *L2TP) runHostRoutingCleanup() {
	if o.hostRoutingCleanup != nil {
		o.hostRoutingCleanup()
		o.hostRoutingCleanup = nil
	}
}

func (o *L2TP) startXl2tpdLocked() error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, xl2tpdBinary, "-D",
		"-c", o.xl2tpdConfPath(),
		"-C", o.controlSocketPath(),
		"-p", o.pidPath(),
	)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = xl2tpdStopGrace
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start xl2tpd: %w", err)
	}
	done := make(chan struct{})
	o.process = cmd
	o.processDone = done
	o.cancelProcess = cancel
	go o.pump(stdout)
	go o.pump(stderr)
	go func() {
		err := cmd.Wait()
		close(done)
		if ctx.Err() == nil {
			o.emitLogf("Error", "l2tp: xl2tpd exited unexpectedly: %v", err)
		}
	}()
	return nil
}

func (o *L2TP) stopXl2tpdLocked() {
	if o.cancelProcess != nil {
		o.cancelProcess()
		o.cancelProcess = nil
	}
	if o.processDone != nil {
		select {
		case <-o.processDone:
		case <-time.After(xl2tpdStopGrace + time.Second):
		}
		o.processDone = nil
	}
	o.process = nil
}

func (o *L2TP) pump(r interface{ Read([]byte) (int, error) }) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			o.emitLog("Info", string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (o *L2TP) Started() bool {
	o.mu.RLock()
	running := o.state == lifecycleRunning && o.process != nil
	o.mu.RUnlock()
	return running && ipsec.Running()
}

func (o *L2TP) Version() string { return DetectVersion() }

func (o *L2TP) Logs() <-chan string { return o.logChan }

func (o *L2TP) Restart() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != lifecycleRunning {
		return errNotStarted
	}
	o.stopXl2tpdLocked()
	if err := o.writeConfig(); err != nil {
		return fmt.Errorf("write l2tp config: %w", err)
	}
	if err := ipsec.LoadAll(); err != nil {
		return fmt.Errorf("swanctl load: %w", err)
	}
	if err := o.startXl2tpdLocked(); err != nil {
		return err
	}
	o.emitLogf("Info", "l2tp: restarted (%s)", o.config.InboundTag)
	return nil
}

func (o *L2TP) Shutdown() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state == lifecycleStopped && o.process == nil {
		return
	}
	o.state = lifecycleStopped
	if o.cancelPoll != nil {
		o.cancelPoll()
		o.cancelPoll = nil
	}
	o.stopXl2tpdLocked()
	_ = os.Remove(o.swanctlFragmentPath())
	if ipsec.Running() {
		_ = ipsec.LoadAll()
	}
	ipsec.Stop()
	o.removeChapSecrets()
	o.runHostRoutingCleanup()
	o.emitLog("Info", "l2tp: shutdown complete")
}

func (o *L2TP) SyncUser(_ context.Context, user *common.User) error {
	return o.applyUsers([]*common.User{user})
}

func (o *L2TP) SyncUsers(_ context.Context, users []*common.User) error {
	removed := o.users.replaceAll(users)
	err := o.writeChapSecrets()
	o.killSessions(removed)
	o.forgetUsers(removed)
	return err
}

func (o *L2TP) UpdateUsers(_ context.Context, users []*common.User) error {
	return o.applyUsers(users)
}

func (o *L2TP) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	if err := o.applyUsers(users); err != nil {
		return err
	}
	return o.Restart()
}

func (o *L2TP) applyUsers(users []*common.User) error {
	var revoked, rotated []string
	for _, u := range users {
		username, changed, removed := o.users.applyUser(u)
		switch {
		case removed:
			revoked = append(revoked, username)
		case changed:
			rotated = append(rotated, username)
		}
	}
	err := o.writeChapSecrets()
	o.killSessions(append(append([]string(nil), revoked...), rotated...))
	o.forgetUsers(revoked)
	return err
}

func (o *L2TP) killSessions(usernames []string) {
	if len(usernames) == 0 {
		return
	}
	targets := make(map[string]struct{}, len(usernames))
	for _, u := range usernames {
		targets[u] = struct{}{}
	}
	for _, s := range readSessions(o.config.InboundTag) {
		if _, ok := targets[s.user]; !ok || s.pid <= 0 || !processIsPppd(s.pid) {
			continue
		}
		if p, err := os.FindProcess(s.pid); err == nil {
			if err := p.Signal(syscall.SIGTERM); err == nil {
				o.emitLogf("Info", "l2tp: terminated session of user %s (%s)", s.user, s.ifname)
			}
		}
	}
}

func (o *L2TP) GetOutboundsLatency(_ context.Context, _ *common.LatencyRequest) (*common.LatencyResponse, error) {
	return &common.LatencyResponse{Latencies: []*common.Latency{}}, nil
}
