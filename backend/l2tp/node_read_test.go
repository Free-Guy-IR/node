package l2tp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func nodeRead(o *L2TP, typ common.StatType) (int64, error) {
	resp, err := o.GetStats(context.Background(), &common.StatRequest{Type: typ, Reset_: true})
	if err != nil {
		return 0, err
	}
	var total int64
	for _, s := range resp.GetStats() {
		total += s.GetValue()
	}
	return total, nil
}

type nodeReadResult struct {
	value int64
	err   error
}

func awaitNodeRead(t *testing.T, results <-chan nodeReadResult, what string) int64 {
	t.Helper()
	select {
	case r := <-results:
		if r.err != nil {
			t.Fatalf("%s: %v", what, r.err)
		}
		return r.value
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never finished", what)
		return 0
	}
}

func TestNodeLevelReadsAppliedOutOfSamplingOrderAreCountedOnce(t *testing.T) {
	for _, typ := range []common.StatType{common.StatType_Outbounds, common.StatType_Inbounds} {
		t.Run(typ.String(), func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			a := e.instance("7")
			takeNode(t, a, typ)
			a.poll()
			e.counters("ppp0", 1100, 600)

			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			prev := interfaceSampled
			t.Cleanup(func() { interfaceSampled = prev })
			interfaceSampled = func() {
				gated := false
				once.Do(func() { gated = true })
				if gated {
					close(entered)
					<-release
				}
			}

			slow := make(chan nodeReadResult, 1)
			go func() {
				v, err := nodeRead(a, typ)
				slow <- nodeReadResult{v, err}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the slow read never sampled the totals")
			}

			fast := make(chan nodeReadResult, 1)
			go func() {
				a.poll()
				v, err := nodeRead(a, typ)
				fast <- nodeReadResult{v, err}
			}()
			select {
			case r := <-fast:
				fast <- r
			case <-time.After(200 * time.Millisecond):
			}
			close(release)

			total := awaitNodeRead(t, slow, "the slow read") + awaitNodeRead(t, fast, "the fast read")
			last, err := nodeRead(a, typ)
			if err != nil {
				t.Fatal(err)
			}
			if total+last != 1700 {
				t.Fatalf("node-level reads reported %d bytes, the sessions moved 1700", total+last)
			}
		})
	}
}

func TestNodeLevelReadsOfANewInstanceCountFromZero(t *testing.T) {
	for _, typ := range []common.StatType{common.StatType_Outbounds, common.StatType_Inbounds} {
		t.Run(typ.String(), func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			a := e.instance("7")
			a.poll()
			if got := takeNode(t, a, typ); got != 1500 {
				t.Fatalf("the first node-level read after a poll reported %d bytes, the poll counted 1500", got)
			}
			e.counters("ppp0", 1100, 600)
			a.poll()
			if got := takeNode(t, a, typ); got != 200 {
				t.Fatalf("the second node-level read reported %d bytes, want 200", got)
			}
		})
	}
}
