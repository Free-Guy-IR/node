package rpc

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func runASecondCopyOfThisTestBinary(t *testing.T, env ...string) {
	t.Helper()
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-5*time.Second))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$", "-test.count=1")
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("a second copy of this test binary, started while this one runs its shared test node, failed after %s: %v\n%s",
			time.Since(started).Round(time.Millisecond), err, out.String())
	}
}

func TestASecondCopyOfThisPackageStartsItsOwnSharedNodeWhileTheFirstRuns(t *testing.T) {
	runASecondCopyOfThisTestBinary(t)
}
