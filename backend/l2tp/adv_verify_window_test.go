package l2tp

import (
	"os"
	"strconv"
	"testing"
	"time"
)

const (
	windowSeeds = 24
	windowSteps = 90
)

type windowInterleaving struct {
	*interleaving
	frozen map[int]bool
}

func TestL2TPFallbackWindowStaysExactAcrossRandomInterleavings(t *testing.T) {
	from, _ := strconv.ParseInt(os.Getenv("VSEED_FROM"), 10, 64)
	to, _ := strconv.ParseInt(os.Getenv("VSEED_TO"), 10, 64)
	if from == 0 {
		from, to = 1, windowSeeds
	}
	for seed := from; seed <= to; seed++ {
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
			x := &windowInterleaving{frozen: make(map[int]bool)}
			prev := wallClock
			t.Cleanup(func() { wallClock = prev })
			x.interleaving = newInterleaving(t, seed)
			wallClock = func() time.Time { return time.Unix(x.h.clock, 0) }
			x.run(windowSteps)
		})
	}
}

func (x *windowInterleaving) movable() []*fakePppd {
	var out []*fakePppd
	for _, p := range x.livePppds() {
		if !x.frozen[p.pid] {
			out = append(out, p)
		}
	}
	return out
}

func (x *windowInterleaving) pickMovable() *fakePppd {
	list := x.movable()
	if len(list) == 0 {
		return nil
	}
	return list[x.r.Intn(len(list))]
}

func (x *windowInterleaving) settle() {
	x.clearFailures()
	x.o.poll()
	x.o.poll()
	x.take()
}

func (x *windowInterleaving) recoverAll() {
	x.clearFailures()
	x.o.poll()
	x.frozen = make(map[int]bool)
}

func (x *windowInterleaving) failWhileCorrupt() {
	x.settle()
	x.h.advance(int64(3 + x.r.Intn(5)))
	for _, p := range x.livePppds() {
		x.frozen[p.pid] = true
	}
	x.e.write(x.e.countedPath(), "not a counted state\n")
	switch x.r.Intn(4) {
	case 0:
		x.failList[0].Store(true)
	case 1:
		x.failList[1].Store(true)
	case 2:
		x.failRead.Store(true)
	default:
		x.failList[0].Store(true)
		x.failList[1].Store(true)
	}
	x.o = x.e.instance(x.users...)
}

func (x *windowInterleaving) step() {
	switch x.r.Intn(16) {
	case 0, 1:
		if len(x.live) < 5 {
			x.racing(x.up(x.spawn()))
		}
	case 2, 3:
		for _, p := range x.movable() {
			if x.r.Intn(3) != 0 {
				x.h.grow(p, int64(x.r.Intn(5000)), int64(x.r.Intn(5000)))
			}
		}
	case 4:
		if p := x.pickMovable(); p != nil {
			x.racing(x.down(p))
			if x.r.Intn(2) == 0 {
				x.h.grow(p, int64(x.r.Intn(20)), int64(x.r.Intn(20)))
			}
			x.h.advance(int64(x.r.Intn(3)))
			x.racing(x.up(p))
		}
	case 5:
		if p := x.pickMovable(); p != nil {
			x.racing(x.down(p))
			x.finish(p)
		}
	case 6:
		if p := x.pickMovable(); p != nil {
			x.finish(p)
			if x.r.Intn(2) == 0 {
				x.o.poll()
			}
			x.racing(x.down(p))
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
		x.failWhileCorrupt()
	case 13:
		x.recoverAll()
	case 14, 15:
		x.h.advance(int64(1 + x.r.Intn(90)))
	}
}

func (x *windowInterleaving) run(steps int) {
	for i := 0; i < steps; i++ {
		x.step()
	}
	x.recoverAll()
	x.endGracefully()
	x.o.poll()
	x.o.poll()
	x.take()
	for _, u := range x.users {
		if x.billed[u] != x.truth[u] {
			x.t.Fatalf("user %s billed %d bytes, its sessions ended at %d (billed %v, truth %v)", u, x.billed[u], x.truth[u], x.billed, x.truth)
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
