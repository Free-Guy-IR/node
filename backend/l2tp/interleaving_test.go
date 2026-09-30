package l2tp

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const (
	interleavingSeeds = 24
	interleavingSteps = 70
	racingPolls       = 4
)

type interleaving struct {
	t          *testing.T
	r          *rand.Rand
	e          *acctEnv
	h          *fakeHost
	o          *L2TP
	users      []string
	live       map[int]*fakePppd
	truth      map[string]int64
	billed     map[string]int64
	node       map[common.StatType]int64
	nextPid    int
	failOpen   atomic.Int64
	failProc   atomic.Bool
	failRemove atomic.Bool
	failList   [2]atomic.Bool
	failRead   atomic.Bool
	failCount  atomic.Bool
}

func TestL2TPBillingStaysExactAcrossRandomInterleavings(t *testing.T) {
	for seed := int64(1); seed <= interleavingSeeds; seed++ {
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
			newInterleaving(t, seed).run(interleavingSteps)
		})
	}
}

func newInterleaving(t *testing.T, seed int64) *interleaving {
	e := newAcctEnv(t)
	x := &interleaving{
		t:       t,
		r:       rand.New(rand.NewSource(seed)),
		e:       e,
		h:       newFakeHost(t, e),
		users:   []string{"u1", "u2", "u2="},
		live:    make(map[int]*fakePppd),
		truth:   make(map[string]int64),
		billed:  make(map[string]int64),
		node:    make(map[common.StatType]int64),
		nextPid: 3000,
	}
	sessionIsAlive = pppdSessionIsAlive
	prevOpen, prevProc, prevRemove := openFinalRecord, readProcFile, removeFinalRecord
	prevList, prevRead, prevCounter, prevClock := listDir, readSessionFile, readCounterFile, wallClock
	t.Cleanup(func() {
		openFinalRecord, readProcFile, removeFinalRecord = prevOpen, prevProc, prevRemove
		listDir, readSessionFile, readCounterFile, wallClock = prevList, prevRead, prevCounter, prevClock
	})
	listDir = func(name string) ([]os.DirEntry, error) {
		for i, dir := range []string{sessionStateDir, sessionFinalDir} {
			if name == dir && x.failList[i].Load() {
				return nil, tooManyOpenFiles(name)
			}
		}
		return prevList(name)
	}
	readCounterFile = func(path string) ([]byte, error) {
		if x.failCount.Load() {
			return nil, tooManyOpenFiles(path)
		}
		return prevCounter(path)
	}
	wallClock = func() time.Time { return time.Unix(x.h.clock, 0) }
	readSessionFile = func(path string) ([]byte, error) {
		if x.failRead.Load() {
			return nil, tooManyOpenFiles(path)
		}
		return prevRead(path)
	}
	openFinalRecord = func(path string) (io.ReadCloser, error) {
		if x.failOpen.Add(-1) >= 0 {
			return nil, tooManyOpenFiles(path)
		}
		return prevOpen(path)
	}
	readProcFile = func(name string) ([]byte, error) {
		if x.failProc.Load() {
			return nil, tooManyOpenFiles(name)
		}
		return prevProc(name)
	}
	removeFinalRecord = func(path string) error {
		if x.failRemove.Load() {
			return errors.New("device or resource busy")
		}
		return prevRemove(path)
	}
	x.o = e.instance(x.users...)
	return x
}

func (x *interleaving) livePppds() []*fakePppd {
	out := make([]*fakePppd, 0, len(x.live))
	for _, p := range x.live {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

func (x *interleaving) pick() *fakePppd {
	list := x.livePppds()
	if len(list) == 0 {
		return nil
	}
	return list[x.r.Intn(len(list))]
}

var nodeStatTypes = []common.StatType{common.StatType_Outbounds, common.StatType_Inbounds}

func (x *interleaving) take() {
	for u, v := range takeUsers(x.t, x.o) {
		x.billed[u] += v
	}
	for _, typ := range nodeStatTypes {
		x.node[typ] += takeNode(x.t, x.o, typ)
	}
}

func (x *interleaving) pauses() []time.Duration {
	out := make([]time.Duration, 1+x.r.Intn(racingPolls))
	for i := range out {
		out[i] = time.Duration(x.r.Intn(8000)) * time.Microsecond
	}
	return out
}

func (x *interleaving) racing(scripts ...func() error) {
	x.t.Helper()
	o := x.o
	stop := make(chan struct{})
	var poller sync.WaitGroup
	if x.r.Intn(3) != 0 {
		pauses := x.pauses()
		poller.Add(1)
		go func() {
			defer poller.Done()
			for _, d := range pauses {
				select {
				case <-stop:
					return
				case <-time.After(d):
					o.poll()
				}
			}
		}()
	}
	var nodeReads [2]int64
	var nodeErr error
	if x.r.Intn(2) == 0 {
		pauses := x.pauses()
		poller.Add(1)
		go func() {
			defer poller.Done()
			for i, d := range pauses {
				select {
				case <-stop:
					return
				case <-time.After(d):
					v, err := nodeRead(o, nodeStatTypes[i%2])
					if err != nil {
						nodeErr = err
						return
					}
					nodeReads[i%2] += v
				}
			}
		}()
	}
	errs := make([]error, len(scripts))
	var wg sync.WaitGroup
	for i, s := range scripts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s()
		}()
	}
	wg.Wait()
	close(stop)
	poller.Wait()
	for i, typ := range nodeStatTypes {
		x.node[typ] += nodeReads[i]
	}
	errs = append(errs, nodeErr)
	for _, err := range errs {
		if err != nil {
			x.t.Fatal(err)
		}
	}
}

func (x *interleaving) up(p *fakePppd) func() error {
	env := x.h.upEnv(p)
	return func() error { return x.h.script(x.h.scripts.ipUpScriptPath(), env...) }
}

func (x *interleaving) down(p *fakePppd) func() error {
	env := x.h.downEnv(p)
	return func() error { return x.h.script(x.h.scripts.ipDownScriptPath(), env...) }
}

func (x *interleaving) spawn() *fakePppd {
	p := x.h.spawn(x.nextPid, x.users[x.r.Intn(len(x.users))])
	x.nextPid++
	x.live[p.pid] = p
	x.h.grow(p, int64(x.r.Intn(80)), int64(x.r.Intn(80)))
	return p
}

func (x *interleaving) finish(p *fakePppd) {
	x.h.exit(p)
	delete(x.live, p.pid)
	x.truth[p.user] += p.rx + p.tx
}

func (x *interleaving) endGracefully() {
	for _, p := range x.livePppds() {
		x.racing(x.down(p))
		x.finish(p)
	}
}

func (x *interleaving) clearFailures() {
	x.failOpen.Store(0)
	x.failProc.Store(false)
	x.failRemove.Store(false)
	x.failList[0].Store(false)
	x.failList[1].Store(false)
	x.failRead.Store(false)
	x.failCount.Store(false)
}

func (x *interleaving) step() {
	switch x.r.Intn(18) {
	case 0, 1:
		if len(x.live) < 4 {
			x.racing(x.up(x.spawn()))
		}
	case 2, 3:
		for _, p := range x.livePppds() {
			if x.r.Intn(3) != 0 {
				x.h.grow(p, int64(x.r.Intn(5000)), int64(x.r.Intn(5000)))
			}
		}
	case 4:
		if p := x.pick(); p != nil {
			x.racing(x.down(p))
			if x.r.Intn(2) == 0 {
				x.h.grow(p, int64(x.r.Intn(20)), int64(x.r.Intn(20)))
			}
			x.h.advance(int64(x.r.Intn(3)))
			x.racing(x.up(p))
		}
	case 5:
		if p := x.pick(); p != nil {
			x.racing(x.down(p))
			x.finish(p)
		}
	case 6:
		if p := x.pick(); p != nil {
			x.finish(p)
			if x.r.Intn(2) == 0 {
				x.o.poll()
			}
			late := x.down(p)
			if x.r.Intn(3) != 0 && len(x.live) < 4 {
				next := x.spawn()
				if x.r.Intn(2) == 0 {
					x.racing(x.up(next), late)
					return
				}
				x.racing(x.up(next))
			}
			x.racing(late)
		}
	case 7, 8:
		x.o.poll()
	case 9:
		x.take()
	case 10:
		stopAccounting(x.o)
		x.o = x.e.instance(x.users...)
	case 11:
		x.take()
		x.o = x.e.instance(x.users...)
	case 12:
		x.endGracefully()
		x.o.poll()
		x.take()
		x.nextPid = 3000
		x.h.advance(int64(x.r.Intn(3)))
		if x.r.Intn(2) == 0 {
			x.h.reboot(fmt.Sprintf("0f1e2d3c-4b5a-6978-8796-%012d", x.r.Intn(1000000)))
			x.h.ticks = 90000
			x.h.ifindex = 40
		}
		x.o = x.e.instance(x.users...)
	case 13:
		x.failOpen.Store(int64(x.r.Intn(3 * countedGraceRounds / 10)))
		x.failProc.Store(x.r.Intn(3) == 0)
		x.failRemove.Store(x.r.Intn(4) == 0)
		x.failList[0].Store(x.r.Intn(5) == 0)
		x.failList[1].Store(x.r.Intn(5) == 0)
		x.failRead.Store(x.r.Intn(5) == 0)
		x.failCount.Store(x.r.Intn(5) == 0)
	case 16:
		x.clearFailures()
		x.o.poll()
		x.o.poll()
		x.take()
		x.h.advance(0)
		x.e.write(x.e.countedPath(), "not a counted state\n")
		switch x.r.Intn(3) {
		case 0:
			x.failList[0].Store(true)
		case 1:
			x.failCount.Store(true)
		}
		x.o = x.e.instance(x.users...)
		x.clearFailures()
		x.o.poll()
	case 17:
		x.h.stepClock(-int64(1 + x.r.Intn(7200)))
	case 14, 15:
		x.h.advance(int64(1 + x.r.Intn(90)))
	}
}

func (x *interleaving) run(steps int) {
	for i := 0; i < steps; i++ {
		x.step()
	}
	x.clearFailures()
	x.endGracefully()
	x.o.poll()
	x.o.poll()
	x.take()
	var all int64
	for _, u := range x.users {
		all += x.truth[u]
		if x.billed[u] != x.truth[u] {
			x.t.Fatalf("user %s billed %d bytes, its sessions ended at %d (billed %v, truth %v)", u, x.billed[u], x.truth[u], x.billed, x.truth)
		}
	}
	for _, typ := range nodeStatTypes {
		if x.node[typ] != all {
			x.t.Fatalf("%v reads reported %d bytes, the sessions moved %d", typ, x.node[typ], all)
		}
	}
	for _, dir := range []string{sessionStateDir, sessionFinalDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			x.t.Fatal(err)
		}
		for _, en := range entries {
			x.t.Fatalf("%s left behind in %s", en.Name(), dir)
		}
	}
}
