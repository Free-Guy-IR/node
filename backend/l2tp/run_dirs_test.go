package l2tp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func hideDirectory(t *testing.T, dir func() string, hidden *bool) {
	t.Helper()
	prev := listDir
	t.Cleanup(func() { listDir = prev })
	listDir = func(name string) ([]os.DirEntry, error) {
		if *hidden && name == dir() {
			return nil, &fs.PathError{Op: "open", Path: name, Err: syscall.ENOENT}
		}
		return prev(name)
	}
}

func moveAway(t *testing.T, dir string) func() {
	t.Helper()
	if err := os.Rename(dir, dir+".away"); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Rename(dir+".away", dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunDirectoriesExistBeforeTheCountedStateIsLoaded(t *testing.T) {
	for name, state := range map[string]string{
		"unreadable counted state":            "not a counted state\n",
		"fallback awaiting its first listing": "v2\nsince 1800000000 " + fallbackBoot + ":500000\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := newAcctEnv(t)
			pinWallClock(t, fallbackLoad)
			e.bootAt(nextBoot, 1500)
			for _, dir := range []string{sessionStateDir, sessionFinalDir} {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			e.write(e.countedPath(), state)
			var missing []string
			prev := listDir
			t.Cleanup(func() { listDir = prev })
			listDir = func(name string) ([]os.DirEntry, error) {
				entries, err := prev(name)
				if errors.Is(err, fs.ErrNotExist) {
					missing = append(missing, name)
				}
				return entries, err
			}
			e.instance("7")
			if len(missing) > 0 {
				t.Fatalf("the load listed %v before the run directories were created", missing)
			}
		})
	}
}

func TestRecordsMissingFromTheRebuildOfAnUnreadableCountedStateAreTakenAsCounted(t *testing.T) {
	for _, tc := range []struct {
		name             string
		sessions, finals bool
	}{{"sessions", true, false}, {"final records", false, true}, {"both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAcctEnv(t)
			pinWallClock(t, fallbackLoad)
			e.session("ppp0", "7", 100, fallbackLoad-3600, 4000, 1000)
			e.final("ppp1", "8", 200, fallbackLoad-7200, 900, 100)
			e.write(e.countedPath(), "not a counted state\n")
			sessions, finals := tc.sessions, tc.finals
			hideDirectory(t, func() string { return sessionStateDir }, &sessions)
			hideDirectory(t, func() string { return sessionFinalDir }, &finals)
			a := e.instance("7", "8")
			sessions, finals = false, false
			a.poll()
			expectUsers(t, "a session and a final record the load could not list", takeUsers(t, a), map[string]int64{})
			e.counters("ppp0", 4100, 1050)
			a.poll()
			expectUsers(t, "growth after the load", takeUsers(t, a), map[string]int64{"7": 150})
		})
	}
}

func TestFinalRecordsMissingFromTheFirstListingOfAFallbackAreTakenAsPrior(t *testing.T) {
	e := newAcctEnv(t)
	pinWallClock(t, fallbackLoad)
	e.bootAt(nextBoot, 1500)
	e.finalWithMono("ppp0", "7", 100, fallbackLoad+600, earlierBoot+":2000", 4000, 1000)
	e.write(e.countedPath(), "v2\nsince 1800000000 "+fallbackBoot+":500000\n")
	hidden := true
	hideDirectory(t, func() string { return sessionFinalDir }, &hidden)
	a := e.instance("7")
	hidden = false
	a.poll()
	expectUsers(t, "a final record of an unplaced boot the load could not list", takeUsers(t, a), map[string]int64{})
}

func TestRunDirectoryGoneForTheWholeGraceKeepsItsWatermarks(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  func() string
		log  string
		end  bool
	}{
		{"sessions", func() string { return sessionStateDir }, "cannot list the sessions", false},
		{"final records", func() string { return sessionFinalDir }, "cannot list the final records", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 4000, 1000)
			a := e.instance("7")
			a.poll()
			expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 5000})
			if tc.end {
				e.end("ppp0", "7", 100, 1000, 4100, 1050)
			}
			restore := moveAway(t, tc.dir())
			for i := 0; i <= countedGraceRounds; i++ {
				a.poll()
			}
			if !slices.Contains(readCountedLines(t, e.countedPath()), "ppp0 100 1000 4000 1000") {
				t.Fatalf("the watermark of ppp0 expired while its directory was gone: %v", readCountedLines(t, e.countedPath()))
			}
			logged := 0
			for _, line := range drainLogs(a) {
				if strings.Contains(line, tc.log) {
					logged++
				}
			}
			if logged != 1 {
				t.Fatalf("a directory gone for the whole grace must be logged once, got %d", logged)
			}
			restore()
			if !tc.end {
				e.counters("ppp0", 4100, 1050)
			}
			a.poll()
			expectUsers(t, "the same session once its directory is back", takeUsers(t, a), map[string]int64{"7": 150})
		})
	}
}

type vanishingSession struct {
	ifname, user    string
	pid             int
	started, rx, tx int64
}

type vanishing struct {
	t      *testing.T
	e      *acctEnv
	r      *rand.Rand
	o      *L2TP
	mu     sync.Mutex
	away   map[string]bool
	live   []*vanishingSession
	next   int
	truth  map[string]int64
	polls  atomic.Int64
	billMu sync.Mutex
	billed map[string]int64
}

func (x *vanishing) loc(dir string) string {
	if x.away[dir] {
		return dir + ".away"
	}
	return dir
}

func (x *vanishing) put(path, content string) {
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		x.t.Error(err)
	}
}

func (x *vanishing) counters(s *vanishingSession) {
	dir := filepath.Join(sysClassNet, s.ifname, "statistics")
	x.put(filepath.Join(dir, "rx_bytes"), strconv.FormatInt(s.rx, 10)+"\n")
	x.put(filepath.Join(dir, "tx_bytes"), strconv.FormatInt(s.tx, 10)+"\n")
}

func (x *vanishing) start() {
	x.next++
	s := &vanishingSession{ifname: fmt.Sprintf("ppp%d", x.next), user: []string{"u1", "u2"}[x.r.Intn(2)], pid: 4000 + x.next, started: int64(2000 + x.next)}
	s.rx, s.tx = int64(x.r.Intn(900)), int64(x.r.Intn(900))
	x.counters(s)
	x.put(filepath.Join(x.loc(sessionStateDir), s.ifname), fmt.Sprintf("user=%s\ntunnel_ip=127.0.0.1\nclient=\ntag=%s\npid=%d\nstarted=%d\n", s.user, x.e.tag, s.pid, s.started))
	x.live = append(x.live, s)
}

func (x *vanishing) grow() {
	if len(x.live) == 0 {
		return
	}
	s := x.live[x.r.Intn(len(x.live))]
	s.rx += int64(x.r.Intn(5000))
	s.tx += int64(x.r.Intn(5000))
	x.counters(s)
}

func (x *vanishing) end() {
	if len(x.live) == 0 {
		return
	}
	i := x.r.Intn(len(x.live))
	s := x.live[i]
	s.rx += int64(x.r.Intn(50))
	s.tx += int64(x.r.Intn(50))
	name := fmt.Sprintf("%s.%d.%d", s.ifname, s.pid, 1700000000+x.next)
	x.put(filepath.Join(x.loc(sessionFinalDir), name), fmt.Sprintf("user=%s\ntag=%s\nifname=%s\npid=%d\nstarted=%d\nrx=%d\ntx=%d\n", s.user, x.e.tag, s.ifname, s.pid, s.started, s.rx, s.tx))
	if err := os.Remove(filepath.Join(x.loc(sessionStateDir), s.ifname)); err != nil {
		x.t.Error(err)
	}
	if err := os.RemoveAll(filepath.Join(sysClassNet, s.ifname)); err != nil {
		x.t.Error(err)
	}
	x.truth[s.user] += s.rx + s.tx
	x.live = slices.Delete(x.live, i, i+1)
}

func (x *vanishing) take() {
	resp, err := x.o.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if err != nil {
		x.t.Error(err)
		return
	}
	x.billMu.Lock()
	defer x.billMu.Unlock()
	for _, s := range resp.GetStats() {
		x.billed[s.GetName()] += s.GetValue()
	}
}

func (x *vanishing) waitPolls(n int64) {
	deadline := time.Now().Add(2 * time.Minute)
	for target := x.polls.Load() + n; x.polls.Load() < target; {
		if time.Now().After(deadline) {
			x.t.Fatalf("only %d polls ran", x.polls.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func (x *vanishing) move(dirs []string, away bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, dir := range dirs {
		from, to := dir, dir+".away"
		if !away {
			from, to = to, from
		}
		if err := os.Rename(from, to); err != nil {
			x.t.Fatal(err)
		}
		x.away[dir] = away
	}
	if !away {
		return
	}
	if slices.Contains(dirs, sessionFinalDir) {
		x.end()
		x.start()
	}
	if slices.Contains(dirs, sessionStateDir) {
		x.grow()
	}
}

func TestBillingStaysExactWhileRunDirectoriesVanishAndReturnUnderConcurrentPolls(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
			e := newAcctEnv(t)
			x := &vanishing{t: t, e: e, r: rand.New(rand.NewSource(seed)), away: make(map[string]bool), truth: make(map[string]int64), billed: make(map[string]int64)}
			x.o = e.instance("u1", "u2")
			x.start()
			x.start()
			stop := make(chan struct{})
			var wg sync.WaitGroup
			var halt sync.Once
			t.Cleanup(func() {
				halt.Do(func() { close(stop) })
				wg.Wait()
			})
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
							x.o.poll()
							x.polls.Add(1)
						}
					}
				}()
			}
			wg.Add(2)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					case <-time.After(time.Millisecond):
						x.take()
					}
				}
			}()
			ops := rand.New(rand.NewSource(seed + 100))
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					case <-time.After(300 * time.Microsecond):
						x.mu.Lock()
						switch k := ops.Intn(10); {
						case k < 2 && len(x.live) < 4:
							x.start()
						case k < 8:
							x.grow()
						case k == 9:
							x.end()
						}
						x.mu.Unlock()
					}
				}
			}()
			plans := [][]string{{sessionStateDir}, {sessionFinalDir}, {sessionStateDir, sessionFinalDir}}
			pick := rand.New(rand.NewSource(seed + 200))
			for cycle := 0; cycle < 4; cycle++ {
				dirs := plans[min(cycle, 2)]
				if cycle == 3 {
					dirs = plans[pick.Intn(len(plans))]
				}
				x.move(dirs, true)
				x.waitPolls(countedGraceRounds + 5)
				x.move(dirs, false)
				x.waitPolls(5)
			}
			halt.Do(func() { close(stop) })
			wg.Wait()
			for len(x.live) > 0 {
				x.end()
			}
			x.o.poll()
			x.o.poll()
			x.take()
			for _, u := range []string{"u1", "u2"} {
				if x.billed[u] != x.truth[u] {
					t.Fatalf("user %s billed %d bytes, its sessions ended at %d (billed %v, truth %v)", u, x.billed[u], x.truth[u], x.billed, x.truth)
				}
			}
		})
	}
}
