//go:build !windows

package openvpn

import (
	"os/exec"
	"syscall"
)

// setProcAttributes sets Unix-specific process attributes for proper process
// group management (used by Stop()'s SIGTERM/SIGKILL escalation and by
// killProcessTree). Mirrors backend/singbox/core_unix.go.
func setProcAttributes(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}
}
