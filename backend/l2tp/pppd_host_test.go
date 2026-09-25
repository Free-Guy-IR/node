package l2tp

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func tooManyOpenFiles(path string) error {
	return &fs.PathError{Op: "open", Path: path, Err: syscall.EMFILE}
}

type fakePppd struct {
	pid     int
	ticks   int64
	ifname  string
	ifindex int
	user    string
	rx      int64
	tx      int64
}

type fakeHost struct {
	t       *testing.T
	e       *acctEnv
	scripts *L2TP
	sh      string
	bin     string
	boot    string
	clock   int64
	ifindex int
	ticks   int64
}

func newFakeHost(t *testing.T, e *acctEnv) *fakeHost {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell available")
	}
	h := &fakeHost{t: t, e: e, sh: sh, bin: filepath.Join(e.root, "bin"), clock: 1700000000, ifindex: 40, ticks: 90000}
	h.scripts = e.prepare()
	if err := os.MkdirAll(h.scripts.config.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.scripts.writeIPScripts(); err != nil {
		t.Fatal(err)
	}
	h.reboot("0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0")
	e.write(filepath.Join(h.bin, "date"), "#!/bin/sh\nexec cat "+filepath.Join(h.bin, "clock")+"\n")
	if err := os.Chmod(filepath.Join(h.bin, "date"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.advance(0)
	return h
}

func (h *fakeHost) reboot(boot string) {
	h.boot = boot
	h.e.write(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"), boot+"\n")
}

func (h *fakeHost) advance(seconds int64) {
	h.clock += seconds
	h.e.write(filepath.Join(h.bin, "clock"), strconv.FormatInt(h.clock, 10)+"\n")
}

func (h *fakeHost) freeIfname() string {
	for n := 0; ; n++ {
		name := fmt.Sprintf("ppp%d", n)
		if _, err := os.Stat(filepath.Join(sysClassNet, name)); os.IsNotExist(err) {
			return name
		}
	}
}

func (h *fakeHost) spawn(pid int, user string) *fakePppd {
	h.t.Helper()
	h.ifindex++
	h.ticks += 137
	p := &fakePppd{pid: pid, ticks: h.ticks, ifname: h.freeIfname(), ifindex: h.ifindex, user: user}
	stat := fmt.Sprintf("%d (pppd) S 1 %d %d 0 -1 4194560 120 0 0 0 3 5 0 0 20 0 1 0 %d 6012928 412 18446744073709551615 1 1 0 0 0 0 0 4096 2 0 0 0 17 0 0 0 0 0 0\n", pid, pid, pid, p.ticks)
	h.e.write(filepath.Join(procRoot, strconv.Itoa(pid), "stat"), stat)
	h.e.write(filepath.Join(procRoot, strconv.Itoa(pid), "comm"), "pppd\n")
	h.e.write(filepath.Join(sysClassNet, p.ifname, "ifindex"), strconv.Itoa(p.ifindex)+"\n")
	h.counters(p)
	return p
}

func (h *fakeHost) counters(p *fakePppd) {
	h.t.Helper()
	h.e.counters(p.ifname, p.rx, p.tx)
}

func (h *fakeHost) grow(p *fakePppd, rx, tx int64) {
	h.t.Helper()
	p.rx += rx
	p.tx += tx
	h.counters(p)
}

func (h *fakeHost) exit(p *fakePppd) {
	h.t.Helper()
	if err := os.RemoveAll(filepath.Join(procRoot, strconv.Itoa(p.pid))); err != nil {
		h.t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(sysClassNet, p.ifname)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) script(script string, env ...string) error {
	cmd := exec.Command(h.sh, script)
	cmd.Env = append([]string{"PATH=" + h.bin + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(script), err, out)
	}
	return nil
}

func (h *fakeHost) run(script string, env ...string) {
	h.t.Helper()
	if err := h.script(script, env...); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) upEnv(p *fakePppd) []string {
	return []string{"PEERNAME=" + p.user, "IFNAME=" + p.ifname, "IPREMOTE=127.0.0.1", "PPPD_PID=" + strconv.Itoa(p.pid)}
}

func (h *fakeHost) downEnv(p *fakePppd) []string {
	return []string{
		"PEERNAME=" + p.user, "IFNAME=" + p.ifname, "PPPD_PID=" + strconv.Itoa(p.pid),
		"BYTES_RCVD=" + strconv.FormatInt(p.rx, 10), "BYTES_SENT=" + strconv.FormatInt(p.tx, 10),
	}
}

func (h *fakeHost) ipUp(p *fakePppd) {
	h.t.Helper()
	h.run(h.scripts.ipUpScriptPath(), h.upEnv(p)...)
}

func (h *fakeHost) ipDown(p *fakePppd) {
	h.t.Helper()
	h.run(h.scripts.ipDownScriptPath(), h.downEnv(p)...)
}

func (h *fakeHost) session(ifname string) (l2tpSession, bool) {
	for _, s := range readSessions(h.e.tag) {
		if s.ifname == ifname {
			return s, true
		}
	}
	return l2tpSession{}, false
}
