//go:build !windows

package singbox

import (
	"os/exec"
	"syscall"
)

// setProcAttributes sets Unix-specific process attributes for proper process
// management. Mirrors backend/xray/core_unix.go.
func setProcAttributes(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}
}
