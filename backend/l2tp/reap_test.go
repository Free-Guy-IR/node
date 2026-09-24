package l2tp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func blockProcReads(t *testing.T, blocked func(name string) bool) {
	t.Helper()
	prev := readProcFile
	t.Cleanup(func() { readProcFile = prev })
	readProcFile = func(name string) ([]byte, error) {
		if blocked(name) {
			return nil, tooManyOpenFiles(filepath.Join(procRoot, name))
		}
		return prev(name)
	}
}

func TestSessionIsNotReapedWhileItsProcessCannotBeRead(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	sessionIsAlive = pppdSessionIsAlive
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.grow(p, 1000, 500)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	blocked := true
	blockProcReads(t, func(name string) bool { return blocked && strings.HasPrefix(name, strconv.Itoa(p.pid)+"/") })
	h.grow(p, 100, 100)
	a.poll()
	if _, err := os.Stat(filepath.Join(sessionStateDir, p.ifname)); err != nil {
		t.Fatalf("a session whose pppd cannot be read must not be reaped: %v", err)
	}
	expectUsers(t, "while /proc cannot be read", takeUsers(t, a), map[string]int64{"7": 200})

	blocked = false
	h.grow(p, 100, 100)
	a.poll()
	expectUsers(t, "after /proc can be read again", takeUsers(t, a), map[string]int64{"7": 200})

	h.grow(p, 50, 50)
	h.ipDown(p)
	h.exit(p)
	a.poll()
	expectUsers(t, "end of the session", takeUsers(t, a), map[string]int64{"7": 100})
}

func TestSessionOfAnExitedPppdIsReaped(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	sessionIsAlive = pppdSessionIsAlive
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.ipUp(p)
	a.poll()
	h.exit(p)
	a.poll()
	if _, err := os.Stat(filepath.Join(sessionStateDir, p.ifname)); !os.IsNotExist(err) {
		t.Fatalf("the session of an exited pppd must be reaped, stat err = %v", err)
	}
}

func TestSessionWhosePidNowBelongsToAnotherProgramIsReaped(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	sessionIsAlive = pppdSessionIsAlive
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.ipUp(p)
	a.poll()
	e.write(filepath.Join(procRoot, strconv.Itoa(p.pid), "stat"), strconv.Itoa(p.pid)+" (sleep) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 424242 0 0\n")
	e.write(filepath.Join(procRoot, strconv.Itoa(p.pid), "comm"), "sleep\n")
	a.poll()
	if _, err := os.Stat(filepath.Join(sessionStateDir, p.ifname)); !os.IsNotExist(err) {
		t.Fatalf("a session whose pid now runs another program must be reaped, stat err = %v", err)
	}
}

func TestSessionWhosePidWasReusedByANewPppdIsReaped(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	sessionIsAlive = pppdSessionIsAlive
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.ipUp(p)
	a.poll()
	h.exit(p)
	h.spawn(4100, "8")
	a.poll()
	if _, err := os.Stat(filepath.Join(sessionStateDir, p.ifname)); !os.IsNotExist(err) {
		t.Fatalf("a session whose pid was taken by a new pppd must be reaped, stat err = %v", err)
	}
}

func TestPppdRecordIsDroppedOnlyAfterItsPppdIsGoneForTheGrace(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.ipUp(p)
	record := filepath.Join(pppdRecordDir, strconv.Itoa(p.pid))
	for i := 0; i < countedGraceRounds+2; i++ {
		a.poll()
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("the record of a running pppd must be kept: %v", err)
	}

	h.ipDown(p)
	h.exit(p)
	for i := 0; i < countedGraceRounds; i++ {
		a.poll()
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("the record of an exited pppd must be kept through the grace: %v", err)
	}
	a.poll()
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("the record of an exited pppd must be dropped after the grace, stat err = %v", err)
	}
}

func TestPppdRecordIsKeptWhileItsProcessCannotBeRead(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("7")
	p := h.spawn(4100, "7")
	h.ipUp(p)
	record := filepath.Join(pppdRecordDir, strconv.Itoa(p.pid))
	blockProcReads(t, func(name string) bool { return strings.HasPrefix(name, strconv.Itoa(p.pid)+"/") })
	for i := 0; i < countedGraceRounds+2; i++ {
		a.poll()
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("a record whose pppd cannot be read must be kept: %v", err)
	}
}

func TestSessionPppdIsProvenByItsStartTimeWhenTheBootIDCannotBeRead(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	p := h.spawn(4100, "7")
	h.ipUp(p)
	s, ok := h.session(p.ifname)
	if !ok || s.proc == "" {
		t.Fatalf("ip-up recorded no process fingerprint: %+v", s)
	}
	blockProcReads(t, func(name string) bool { return name == "sys/kernel/random/boot_id" })
	if got := pppdState(p.pid, s.proc); got != processRunning {
		t.Fatalf("a pppd whose pid and start time match must count as running, got %v", got)
	}
	h.exit(p)
	h.spawn(4100, "8")
	if got := pppdState(p.pid, s.proc); got != processGone {
		t.Fatalf("a new pppd on the same pid must not count as the session's pppd, got %v", got)
	}
}
