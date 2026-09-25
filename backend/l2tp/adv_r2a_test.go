package l2tp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestIPDownOfAnotherPeerIgnoresAStaleRecordOfItsPid(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7", "8")
	old := h.spawn(4100, "7")
	h.grow(old, 300, 200)
	h.ipUp(old)
	h.ipDown(old)
	h.exit(old)
	a.poll()
	expectUsers(t, "the old pppd", takeUsers(t, a), map[string]int64{"7": 500})

	reused := h.spawn(4100, "8")
	if reused.ifname != old.ifname {
		t.Fatalf("the new pppd took %s, want %s", reused.ifname, old.ifname)
	}
	h.grow(reused, 900, 900)
	h.run(h.scripts.ipDownScriptPath(),
		"PEERNAME=8", "IFNAME="+reused.ifname, "PPPD_PID="+strconv.Itoa(reused.pid),
		"BYTES_RCVD=900", "BYTES_SENT=900")
	a.poll()
	expectUsers(t, "a pppd of another peer on a stale record of its pid", takeUsers(t, a), map[string]int64{"8": 1800})
}

func TestSessionsThatCannotBeListedForTheWholeGraceAreNotBilledAgain(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 4000, 1000)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})

	away := sessionStateDir + ".away"
	if err := os.Rename(sessionStateDir, away); err != nil {
		t.Fatal(err)
	}
	e.write(sessionStateDir, "")
	for i := 0; i <= countedGraceRounds; i++ {
		a.poll()
	}
	if err := os.Remove(sessionStateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, sessionStateDir); err != nil {
		t.Fatal(err)
	}

	e.counters("ppp0", 4100, 1050)
	a.poll()
	expectUsers(t, "the same live session once its directory can be read again", takeUsers(t, a), map[string]int64{"7": 150})
}

func TestSessionOfAUsernameEndingInAnEqualsSignIsBilledToThatUser(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("bob=", "bob")
	p := h.spawn(4300, "bob=")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "live session", takeUsers(t, a), map[string]int64{"bob=": 500})

	h.grow(p, 400, 100)
	h.ipDown(p)
	h.exit(p)
	a.poll()
	expectUsers(t, "the tail billed from the final record", takeUsers(t, a), map[string]int64{"bob=": 500})
}

func TestIPDownDropsInterfaceCountersWhenTheInterfaceIsReplacedWhileItReadsThem(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7")
	p := h.spawn(4400, "7")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "live session", takeUsers(t, a), map[string]int64{"7": 500})

	h.grow(p, 400, 100)
	e.counters(p.ifname, 9000, 9000)
	fifo := filepath.Join(sysClassNet, p.ifname, "statistics", "tx_bytes")
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no fifo support: %v", err)
	}
	replaced := make(chan bool, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			replaced <- false
			return
		}
		e.write(filepath.Join(sysClassNet, p.ifname, "ifindex"), strconv.Itoa(p.ifindex+50)+"\n")
		_, _ = f.WriteString("9000\n")
		_ = f.Close()
		replaced <- true
	}()
	done := make(chan error, 1)
	go func() { done <- h.script(h.scripts.ipDownScriptPath(), h.downEnv(p)...) }()
	var scriptErr error
	select {
	case scriptErr = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ip-down never finished")
	}
	select {
	case ok := <-replaced:
		if !ok {
			t.Fatal("the interface could not be replaced while ip-down read it")
		}
	case <-time.After(time.Second):
		if r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = r.Close()
		}
		<-replaced
		t.Fatal("ip-down never read the interface counters")
	}
	if scriptErr != nil {
		t.Fatal(scriptErr)
	}
	h.exit(p)
	a.poll()
	expectUsers(t, "the tail read from pppd's own totals", takeUsers(t, a), map[string]int64{"7": 500})
}

func TestIPDownPutsBackASessionFileTheNextPppdWroteWhileItWasMovingIt(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7", "8")
	old := h.spawn(4500, "7")
	h.grow(old, 300, 200)
	h.ipUp(old)
	a.poll()
	expectUsers(t, "the first pppd", takeUsers(t, a), map[string]int64{"7": 500})

	sessionPath := filepath.Join(sessionStateDir, old.ifname)
	oldContent, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	h.grow(old, 400, 100)
	h.exit(old)
	next := h.spawn(4600, "8")
	if next.ifname != old.ifname {
		t.Fatalf("the next pppd took %s, want %s", next.ifname, old.ifname)
	}
	h.grow(next, 60, 40)
	h.ipUp(next)
	nextContent, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	swap := filepath.Join(e.root, "swap")
	e.write(swap, string(nextContent))
	e.write(sessionPath, string(oldContent))

	realMv, err := findExecutable("mv")
	if err != nil {
		t.Skip(err)
	}
	fake := filepath.Join(h.bin, "mv")
	e.write(fake, fmt.Sprintf("#!/bin/sh\ncase \"$3\" in\n  */.%s.down.*) [ -f \"$SWAP\" ] && %s -f \"$SWAP\" \"$2\" ;;\nesac\nexec %s \"$@\"\n", old.ifname, realMv, realMv))
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	h.run(h.scripts.ipDownScriptPath(), append(h.downEnv(old), "SWAP="+swap)...)
	if err := os.Remove(fake); err != nil {
		t.Fatal(err)
	}

	s, ok := h.session(next.ifname)
	if !ok || s.pid != next.pid || s.user != "8" {
		t.Fatalf("ip-down of pppd %d removed the session file pppd %d wrote while it was moving it: %+v (listed %v)", old.pid, next.pid, s, ok)
	}
	h.grow(next, 100, 100)
	a.poll()
	a.poll()
	expectUsers(t, "the old tail and the next session", takeUsers(t, a), map[string]int64{"7": 500, "8": 300})
}

func findExecutable(name string) (string, error) {
	for _, dir := range []string{"/bin", "/usr/bin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s in /bin or /usr/bin", name)
}

func TestPppdStateParsesTheStartTimeAfterACommWithSpacesAndParentheses(t *testing.T) {
	newAcctEnv(t)
	boot := "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
	if err := os.MkdirAll(filepath.Join(procRoot, "sys", "kernel", "random"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"), []byte(boot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stat := "4700 (pppd) S 1 (x) 2) R 1 4700 4700 0 -1 4194560 120 0 0 0 3 5 0 0 20 0 1 0 424242 6012928 412 18446744073709551615 1 1 0 0 0 0 0 4096 2 0 0 0 17 0 0 0 0 0 0\n"
	if err := os.MkdirAll(filepath.Join(procRoot, "4700"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "4700", "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pppdState(4700, boot+":424242"); got != processRunning {
		t.Fatalf("a pppd whose comm holds spaces and parentheses and whose start time matches: state %d, want running", got)
	}
	if got := pppdState(4700, boot+":1"); got != processGone {
		t.Fatalf("a pppd whose start time differs: state %d, want gone", got)
	}
}
