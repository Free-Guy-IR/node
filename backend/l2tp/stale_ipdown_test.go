package l2tp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLateIPDownOfAnExitedPppdLeavesTheNextSessionOnTheNameAlone(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell available")
	}
	e := newAcctEnv(t)
	a := e.instance("7", "8")
	if err := os.MkdirAll(a.config.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.writeIPScripts(); err != nil {
		t.Fatal(err)
	}

	e.session("ppp96", "8", 5200, 1700000200, 400, 100)
	a.poll()
	expectUsers(t, "the next session on the name", takeUsers(t, a), map[string]int64{"8": 500})

	cmd := exec.Command(sh, a.ipDownScriptPath())
	cmd.Env = append(os.Environ(), "IFNAME=ppp96", "PPPD_PID=5100", "BYTES_RCVD=9000", "BYTES_SENT=9000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ip-down: %v: %s", err, out)
	}

	if _, err := os.Stat(filepath.Join(sessionStateDir, "ppp96")); err != nil {
		t.Fatalf("the late ip-down of pppd 5100 removed the session file of pppd 5200: %v", err)
	}
	e.counters("ppp96", 900, 300)
	a.poll()
	expectUsers(t, "growth of the next session", takeUsers(t, a), map[string]int64{"8": 700})
}
