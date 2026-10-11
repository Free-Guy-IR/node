package rest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/config"
)

const oldSharedNodeStartDeadline = 10 * time.Second

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

func TestTheSharedNodeWaitsForACoreThatStartsSlowerThanTheOldDeadline(t *testing.T) {
	realXray, err := filepath.Abs(config.NewTestConfig(t.TempDir(), uuid.New()).XrayExecutablePath)
	if err != nil {
		t.Fatal(err)
	}
	slowXray := filepath.Join(t.TempDir(), "xray")
	delay := int((oldSharedNodeStartDeadline + time.Second).Seconds())
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" != version ]; then sleep %d; fi\nexec \"$PG_NODE_TEST_REAL_XRAY\" \"$@\"\n", delay)
	if err := os.WriteFile(slowXray, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runASecondCopyOfThisTestBinary(t, "XRAY_EXECUTABLE_PATH="+slowXray, "PG_NODE_TEST_REAL_XRAY="+realXray)
}
