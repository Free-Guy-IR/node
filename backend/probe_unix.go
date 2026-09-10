//go:build unix

package backend

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const ProbeWaitDelay = 2 * time.Second

func ConfigureProbe(cmd *exec.Cmd) {
	cmd.WaitDelay = ProbeWaitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}

func ReapProbe(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 1 {
		return
	}
	if selfPgid, err := syscall.Getpgid(os.Getpid()); err == nil && pid != selfPgid {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
