package l2tp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSessionFileWithoutStartTimeIsLeftForTheNextPoll(t *testing.T) {
	e := newAcctEnv(t)
	a := e.instance("7")
	path := filepath.Join(sessionStateDir, "ppp0")
	e.write(path, fmt.Sprintf("user=7\ntunnel_ip=127.0.0.1\nclient=\ntag=%s\npid=100\n", e.tag))
	e.counters("ppp0", 1000, 500)

	var billed []int64
	a.poll()
	billed = append(billed, takeUsers(t, a)["7"])
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a session file still being written must be left in place: %v", err)
	}
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	for i := 0; i < 2; i++ {
		a.poll()
		billed = append(billed, takeUsers(t, a)["7"])
	}
	want := []int64{0, 1500, 0}
	for i := range want {
		if billed[i] != want[i] {
			t.Fatalf("billed per poll = %v, want %v", billed, want)
		}
	}
}

func TestSessionTempFileIsNotASession(t *testing.T) {
	e := newAcctEnv(t)
	tmp := filepath.Join(sessionStateDir, ".ppp0.4242")
	e.write(tmp, fmt.Sprintf("user=7\ntunnel_ip=127.0.0.1\nclient=\ntag=%s\npid=100\nstarted=1000\n", e.tag))
	if got := readSessions(e.tag); len(got) != 0 {
		t.Fatalf("a temporary session file was read as %d session(s)", len(got))
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("a temporary session file must be left in place: %v", err)
	}
}

func TestIPUpWritesTheSessionFileAtomically(t *testing.T) {
	e := newAcctEnv(t)
	o := e.prepare("7")
	if err := os.MkdirAll(o.config.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := o.writeIPScripts(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(o.ipUpScriptPath())
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, want := range []string{`tmp="$d/.$IF.$$"`, `} > "$tmp" && mv -f "$tmp" "$d/$IF"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("ip-up must write through a temporary file, missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, `} > "$d/$IF"`) {
		t.Fatalf("ip-up must not write the session file in place:\n%s", script)
	}

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell available")
	}
	cmd := exec.Command(sh, o.ipUpScriptPath())
	cmd.Env = append(os.Environ(), "PEERNAME=7", "IFNAME=ppp98", "IPREMOTE=127.0.0.1", "PPPD_PID=4343")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ip-up: %v: %s", err, out)
	}
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "ppp98" {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Fatalf("session directory = %v, want only ppp98", names)
	}
	sessions := readSessions(e.tag)
	if len(sessions) != 1 || sessions[0].user != "7" || sessions[0].pid != 4343 || sessions[0].started <= 0 {
		t.Fatalf("session read back = %+v", sessions)
	}
}

func TestRewritingTheScriptsLeavesACopyBeingRunIntact(t *testing.T) {
	e := newAcctEnv(t)
	o := e.prepare("7")
	if err := os.MkdirAll(o.config.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := o.writeIPScripts(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{o.ipUpScriptPath(), o.ipDownScriptPath()} {
		running, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { running.Close() })
		before, err := running.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := o.writeIPScripts(); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(before, after) {
			t.Fatalf("%s was rewritten in place, a pppd running it can read a truncated script", filepath.Base(path))
		}
		if after.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %v, want 0755", filepath.Base(path), after.Mode().Perm())
		}
		if raw, err := io.ReadAll(running); err != nil || !strings.HasPrefix(string(raw), "#!/bin/sh\n") {
			t.Fatalf("the copy being run changed: %q, %v", raw, err)
		}
	}
}

func TestScriptsKeepEveryCharacterOfAKeyValue(t *testing.T) {
	for i, name := range []string{"bob=", "bob==", "=bob", "a=b=c", `back\slash`, "q'uo\"te", "$HOME", "-n"} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			e := newAcctEnv(t)
			h := newFakeHost(t, e)
			a := e.instance(name, "bob")
			p := h.spawn(4300, name)
			h.grow(p, 300, 200)
			h.ipUp(p)
			if s, ok := h.session(p.ifname); !ok || s.user != name {
				t.Fatalf("ip-up recorded user %q (listed %v), want %q", s.user, ok, name)
			}
			a.poll()
			h.grow(p, 400, 100)
			h.ipDown(p)
			h.exit(p)
			a.poll()
			expectUsers(t, "the whole session", takeUsers(t, a), map[string]int64{name: 1000})
		})
	}
}

func TestScriptsQuoteTheirDirectories(t *testing.T) {
	e := newAcctEnv(t)
	root := filepath.Join(e.root, "a b$HOME'q")
	prevState, prevFinal, prevRecords := sessionStateDir, sessionFinalDir, pppdRecordDir
	t.Cleanup(func() { sessionStateDir, sessionFinalDir, pppdRecordDir = prevState, prevFinal, prevRecords })
	sessionStateDir = filepath.Join(root, "sessions")
	sessionFinalDir = filepath.Join(root, "final")
	pppdRecordDir = filepath.Join(root, "pppd")
	h := newFakeHost(t, e)
	a := e.instance("7")
	p := h.spawn(4300, "7")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	h.grow(p, 400, 100)
	h.ipDown(p)
	h.exit(p)
	a.poll()
	expectUsers(t, "a session under directories that need quoting", takeUsers(t, a), map[string]int64{"7": 1000})
}
