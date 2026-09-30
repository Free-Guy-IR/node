package l2tp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func verifySessionWithIfindex(e *acctEnv, ifname, username string, pid int, started int64, ifindex int, rx, tx int64) {
	e.t.Helper()
	content := fmt.Sprintf("user=%s\ntunnel_ip=127.0.0.1\nclient=\ntag=%s\npid=%d\nstarted=%d\nifindex=%d\n", username, e.tag, pid, started, ifindex)
	e.write(filepath.Join(sessionStateDir, ifname), content)
	e.write(filepath.Join(sysClassNet, ifname, "ifindex"), fmt.Sprintf("%d\n", ifindex))
	e.counters(ifname, rx, tx)
}

func verifyMakeUnreadable(t *testing.T, path string) func() {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
