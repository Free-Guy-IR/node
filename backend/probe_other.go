//go:build !unix

package backend

import (
	"os/exec"
	"time"
)

const ProbeWaitDelay = 2 * time.Second

func ConfigureProbe(cmd *exec.Cmd) {
	cmd.WaitDelay = ProbeWaitDelay
}
