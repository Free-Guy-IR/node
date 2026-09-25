package l2tp

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

const (
	lifecycleSeeds = 60
	lifecycleSteps = 200
)

type probeSession struct {
	ifname  string
	user    string
	pid     int
	started int64
	rx, tx  int64
	ended   bool
}

func TestRandomLifecyclesBillEveryFinalCounterExactly(t *testing.T) {
	for seed := int64(1); seed <= lifecycleSeeds; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			e := newAcctEnv(t)
			dead := map[int]bool{}
			sessionIsAlive = func(s l2tpSession) bool { return !dead[s.pid] }
			failRemove := false
			prev := removeFinalRecord
			t.Cleanup(func() { removeFinalRecord = prev })
			removeFinalRecord = func(p string) error {
				if failRemove {
					return errors.New("device or resource busy")
				}
				return prev(p)
			}
			users := []string{"u1", "u2", "u3"}
			o := e.instance(users...)
			billed := map[string]int64{}
			truth := map[string]int64{}
			byIf := map[string]*probeSession{}
			var late []*probeSession
			nextPid := 100
			nextStarted := int64(1000)
			take := func() {
				for k, v := range takeUsers(t, o) {
					billed[k] += v
				}
			}
			for step := 0; step < lifecycleSteps; step++ {
				switch r.Intn(12) {
				case 0, 1:
					ifn := fmt.Sprintf("ppp%d", r.Intn(3))
					if byIf[ifn] != nil {
						continue
					}
					nextPid++
					nextStarted++
					s := &probeSession{ifname: ifn, user: users[r.Intn(len(users))], pid: nextPid, started: nextStarted}
					s.rx, s.tx = int64(r.Intn(50)), int64(r.Intn(50))
					byIf[ifn] = s
					e.session(ifn, s.user, s.pid, s.started, s.rx, s.tx)
				case 2, 3:
					for _, s := range byIf {
						if !s.ended {
							s.rx += int64(r.Intn(1000))
							s.tx += int64(r.Intn(1000))
							e.counters(s.ifname, s.rx, s.tx)
						}
					}
				case 4:
					for _, s := range byIf {
						if !s.ended && r.Intn(2) == 0 {
							e.final(s.ifname, s.user, s.pid, s.started, s.rx, s.tx)
							s.ended = true
							truth[s.user] += s.rx + s.tx
						}
					}
				case 5:
					for ifn, s := range byIf {
						if s.ended && r.Intn(2) == 0 {
							e.removeSession(ifn)
							delete(byIf, ifn)
						}
					}
				case 6:
					o.poll()
				case 7:
					take()
				case 8:
					if r.Intn(2) == 0 {
						o = e.restart(o, true, users...)
					} else {
						take()
						o = e.restart(o, false, users...)
					}
				case 9:
					for ifn, s := range byIf {
						if !s.ended && r.Intn(3) == 0 {
							dead[s.pid] = true
							if err := os.RemoveAll(filepath.Join(sysClassNet, ifn)); err != nil {
								t.Fatal(err)
							}
							delete(byIf, ifn)
							late = append(late, s)
							truth[s.user] += s.rx + s.tx
						}
					}
					for i, s := range late {
						if s != nil && r.Intn(3) == 0 {
							e.final(s.ifname, s.user, s.pid, s.started, s.rx, s.tx)
							late[i] = nil
						}
					}
				case 10:
					failRemove = r.Intn(3) == 0
				case 11:
					for ifn, s := range byIf {
						if s.ended || r.Intn(2) != 0 {
							continue
						}
						e.final(ifn, s.user, s.pid, s.started, s.rx, s.tx)
						if err := os.Remove(filepath.Join(sessionStateDir, ifn)); err != nil {
							t.Fatal(err)
						}
						if r.Intn(2) == 0 {
							o.poll()
						}
						nextStarted++
						s.started = nextStarted
						s.rx += int64(r.Intn(20))
						s.tx += int64(r.Intn(20))
						e.session(ifn, s.user, s.pid, s.started, s.rx, s.tx)
					}
				}
			}
			failRemove = false
			for _, s := range late {
				if s != nil {
					e.final(s.ifname, s.user, s.pid, s.started, s.rx, s.tx)
				}
			}
			for ifn, s := range byIf {
				if !s.ended {
					e.final(ifn, s.user, s.pid, s.started, s.rx, s.tx)
					truth[s.user] += s.rx + s.tx
				}
				e.removeSession(ifn)
			}
			o.poll()
			o.poll()
			take()
			for _, u := range users {
				if billed[u] != truth[u] {
					t.Fatalf("user %s billed %d, its sessions ended at %d", u, billed[u], truth[u])
				}
			}
		})
	}
}
