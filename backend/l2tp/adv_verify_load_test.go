package l2tp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIPDownReadsTheLastLineOfARecordWithoutANewline(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("bob=", "bob")
	p := h.spawn(4300, "bob=")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "live session", takeUsers(t, a), map[string]int64{"bob=": 500})

	path := filepath.Join(sessionStateDir, p.ifname)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines, user []string
	for _, l := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if strings.HasPrefix(l, "user=") {
			user = append(user, l)
			continue
		}
		lines = append(lines, l)
	}
	if len(user) != 1 {
		t.Fatalf("ip-up wrote %d user lines: %q", len(user), raw)
	}
	e.write(path, strings.Join(append(lines, user[0]), "\n"))

	h.grow(p, 400, 100)
	h.ipDown(p)
	h.exit(p)
	a.poll()
	expectUsers(t, "the tail of a record whose last line has no newline", takeUsers(t, a), map[string]int64{"bob=": 500})
}

func TestIPDownIgnoresARecordLineWithoutAnEqualsSign(t *testing.T) {
	e := newAcctEnv(t)
	h := newFakeHost(t, e)
	a := e.instance("bob=", "user")
	p := h.spawn(4300, "bob=")
	h.grow(p, 300, 200)
	h.ipUp(p)
	a.poll()
	expectUsers(t, "live session", takeUsers(t, a), map[string]int64{"bob=": 500})

	path := filepath.Join(sessionStateDir, p.ifname)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e.write(path, string(raw)+"user\n")

	h.grow(p, 400, 100)
	h.ipDown(p)
	h.exit(p)
	a.poll()
	expectUsers(t, "the tail of a record holding a line without '='", takeUsers(t, a), map[string]int64{"bob=": 500})
}
