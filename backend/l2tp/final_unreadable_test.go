package l2tp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func blockFinalRecords(t *testing.T, blocked func(path string) bool) {
	t.Helper()
	prev := openFinalRecord
	t.Cleanup(func() { openFinalRecord = prev })
	openFinalRecord = func(path string) (io.ReadCloser, error) {
		if blocked(path) {
			return nil, tooManyOpenFiles(path)
		}
		return prev(path)
	}
}

func finalRecordPaths(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(sessionFinalDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func TestUnopenableFinalRecordIsRetriedAndBilledOnce(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	e.end("ppp0", "7", 100, 1000, 1300, 700)
	blocked := true
	blockFinalRecords(t, func(string) bool { return blocked })
	drainLogs(a)
	for i := 0; i < 2; i++ {
		a.poll()
		expectUsers(t, "while the record cannot be opened", takeUsers(t, a), map[string]int64{})
		if names := finalRecordPaths(t); len(names) != 1 {
			t.Fatalf("an unopenable final record must stay for a later poll, found %v", names)
		}
	}
	if logs := drainLogs(a); len(logs) != 1 {
		t.Fatalf("an unopenable final record must log once per streak, got %d: %v", len(logs), logs)
	}

	blocked = false
	a.poll()
	expectUsers(t, "once it can be opened", takeUsers(t, a), map[string]int64{"7": 500})
	a.poll()
	expectUsers(t, "second poll", takeUsers(t, a), map[string]int64{})
	if names := finalRecordPaths(t); len(names) != 0 {
		t.Fatalf("the billed record must be removed, found %v", names)
	}
}

func TestFinalRecordThatCannotBeUsedIsSetAsideWithOneLogLine(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"no user", "user=\ntag=%s\nifname=ppp0\npid=100\nstarted=1000\nrx=1300\ntx=700\n"},
		{"no interface", "user=7\ntag=%s\nifname=\npid=100\nstarted=1000\nrx=1300\ntx=700\n"},
		{"malformed counter", "user=7\ntag=%s\nifname=ppp0\npid=100\nstarted=1000\nrx=13x0\ntx=700\n"},
		{"malformed identity", "user=7\ntag=%s\nifname=ppp0\npid=1x0\nstarted=1000\nrx=1300\ntx=700\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			a := e.instance("7")
			a.poll()
			expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

			path := e.finalName("ppp0")
			content := strings.Replace(c.content, "%s", e.tag, 1)
			e.write(path, content)
			drainLogs(a)
			a.poll()
			expectUsers(t, "a record that cannot be used", takeUsers(t, a), map[string]int64{})
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("the record must be moved out of the way, stat err = %v", err)
			}
			aside := filepath.Join(sessionFinalDir, "."+filepath.Base(path)+".unreadable")
			kept, err := os.ReadFile(aside)
			if err != nil {
				t.Fatalf("the record must be kept aside as %s: %v", aside, err)
			}
			if string(kept) != content {
				t.Fatalf("the record kept aside changed:\n%s\nwant:\n%s", kept, content)
			}
			if logs := drainLogs(a); len(logs) != 1 || !strings.Contains(logs[0], filepath.Base(path)) {
				t.Fatalf("setting a record aside must log one line naming it, got %v", logs)
			}
			a.poll()
			if logs := drainLogs(a); len(logs) != 0 {
				t.Fatalf("a record already set aside must not log again, got %v", logs)
			}
		})
	}
}

func TestFinalRecordCutShortByAReadErrorIsRetried(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	e.end("ppp0", "7", 100, 1000, 1300, 700)
	failing := true
	prev := openFinalRecord
	t.Cleanup(func() { openFinalRecord = prev })
	openFinalRecord = func(path string) (io.ReadCloser, error) {
		f, err := prev(path)
		if err != nil || !failing {
			return f, err
		}
		return &cutShortReader{f: f, left: 60}, nil
	}
	a.poll()
	expectUsers(t, "a record read only in part", takeUsers(t, a), map[string]int64{})
	if names := finalRecordPaths(t); len(names) != 1 {
		t.Fatalf("a record read only in part must stay, found %v", names)
	}
	failing = false
	a.poll()
	expectUsers(t, "the whole record", takeUsers(t, a), map[string]int64{"7": 500})
}

type cutShortReader struct {
	f    io.ReadCloser
	left int
}

func (r *cutShortReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, &os.PathError{Op: "read", Path: "final", Err: syscall.EIO}
	}
	if len(p) > r.left {
		p = p[:r.left]
	}
	n, err := r.f.Read(p)
	r.left -= n
	if errors.Is(err, io.EOF) {
		return n, &os.PathError{Op: "read", Path: "final", Err: syscall.EIO}
	}
	return n, err
}

func (r *cutShortReader) Close() error { return r.f.Close() }

func TestFinalRecordUnopenableForTheWholeGraceIsSetAside(t *testing.T) {
	e := newAcctEnv(t)
	a := e.instance("7")
	e.final("ppp0", "7", 100, 1000, 10, 10)
	names := finalRecordPaths(t)
	blockFinalRecords(t, func(string) bool { return true })
	drainLogs(a)
	for i := 0; i < countedGraceRounds; i++ {
		a.poll()
	}
	if got := finalRecordPaths(t); len(got) != 1 || got[0] != names[0] {
		t.Fatalf("an unopenable record must stay through the grace, found %v", got)
	}
	a.poll()
	if got := finalRecordPaths(t); len(got) != 1 || got[0] != "."+names[0]+".unreadable" {
		t.Fatalf("an unopenable record must be set aside after the grace, found %v", got)
	}
	if logs := drainLogs(a); len(logs) != 2 {
		t.Fatalf("want one line when the streak starts and one when the record is set aside, got %d: %v", len(logs), logs)
	}
	expectUsers(t, "a record never read", takeUsers(t, a), map[string]int64{})
}

func TestUnreadableCountedStateCountsAnUnopenableFinalRecordAsAlreadyCounted(t *testing.T) {
	e := newAcctEnv(t)
	e.final("ppp1", "8", 200, 1100, 300, 100)
	e.write(e.countedPath(), "not a counted state\n")
	blocked := true
	blockFinalRecords(t, func(string) bool { return blocked })

	a := e.instance("8")
	a.poll()
	blocked = false
	a.poll()
	expectUsers(t, "a record present while the counted state was unreadable", takeUsers(t, a), map[string]int64{})
	if names := finalRecordPaths(t); len(names) != 0 {
		t.Fatalf("the record must be consumed, found %v", names)
	}
}

func TestFinalRecordOfAnotherInboundIsNeverRemovedWhenItCannotBeRead(t *testing.T) {
	e := newAcctEnv(t)
	a := e.instance("7")
	other := filepath.Join(sessionFinalDir, "ppp3.30001.1700000001")
	e.write(other, "user=9\ntag=another-inbound\nifname=ppp3\npid=300\nstarted=1300\nrx=10\ntx=10\n")
	blockFinalRecords(t, func(string) bool { return true })
	a.poll()
	a.poll()
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a record this instance cannot read must stay for its owner: %v", err)
	}
}
