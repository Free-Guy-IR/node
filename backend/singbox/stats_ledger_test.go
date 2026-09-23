package singbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	"github.com/pasarguard/node/backend/singbox/api"
	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"github.com/pasarguard/node/common"
)

const (
	ledgerHy2Down = "inbound>>>Hysteria2>>>traffic>>>downlink"
	ledgerHy2Up   = "inbound>>>Hysteria2>>>traffic>>>uplink"
	ledgerAltDown = "inbound>>>Hysteria2-alt>>>traffic>>>downlink"
	ledgerOldDown = "inbound>>>old>>>traffic>>>downlink"
)

type ledgerFakeCore struct {
	statscmd.UnimplementedStatsServiceServer

	mu       sync.Mutex
	counters map[string]*atomic.Int64
	requests []*statscmd.QueryStatsRequest
	failures int
}

func (f *ledgerFakeCore) add(name string, value int64) {
	f.mu.Lock()
	counter, ok := f.counters[name]
	if !ok {
		counter = new(atomic.Int64)
		f.counters[name] = counter
	}
	f.mu.Unlock()
	counter.Add(value)
}

func (f *ledgerFakeCore) remove(name string) {
	f.mu.Lock()
	delete(f.counters, name)
	f.mu.Unlock()
}

func (f *ledgerFakeCore) restart() {
	f.mu.Lock()
	f.counters = make(map[string]*atomic.Int64)
	f.mu.Unlock()
}

func (f *ledgerFakeCore) failNext() {
	f.mu.Lock()
	f.failures++
	f.mu.Unlock()
}

func (f *ledgerFakeCore) value(name string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	counter, ok := f.counters[name]
	if !ok {
		return 0
	}
	return counter.Load()
}

func (f *ledgerFakeCore) lastRequest() *statscmd.QueryStatsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

func (f *ledgerFakeCore) QueryStats(_ context.Context, req *statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, &statscmd.QueryStatsRequest{
		Patterns: append([]string(nil), req.GetPatterns()...),
		Reset_:   req.GetReset_(),
	})
	if f.failures > 0 {
		f.failures--
		f.mu.Unlock()
		return nil, errors.New("fake stats service unavailable")
	}
	names := make([]string, 0, len(f.counters))
	counters := make(map[string]*atomic.Int64, len(f.counters))
	for name, counter := range f.counters {
		if !ledgerMatches(name, req.GetPatterns()) {
			continue
		}
		names = append(names, name)
		counters[name] = counter
	}
	f.mu.Unlock()

	sort.Strings(names)
	resp := &statscmd.QueryStatsResponse{}
	for _, name := range names {
		var value int64
		if req.GetReset_() {
			value = counters[name].Swap(0)
		} else {
			value = counters[name].Load()
		}
		resp.Stat = append(resp.Stat, &statscmd.Stat{Name: name, Value: value})
	}
	return resp, nil
}

func ledgerMatches(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if strings.Contains(name, pattern) {
			return true
		}
	}
	return false
}

func newLedgerSingBox(t *testing.T) (*SingBox, *ledgerFakeCore) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fake := &ledgerFakeCore{counters: make(map[string]*atomic.Int64)}
	srv := grpc.NewServer()
	statscmd.RegisterStatsServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := api.NewClient(lis.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	return &SingBox{client: client}, fake
}

func ledgerRead(sb *SingBox, statType common.StatType, name string, reset bool) (map[string]int64, error) {
	resp, err := sb.GetStats(context.Background(), &common.StatRequest{Type: statType, Name: name, Reset_: reset})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64)
	for _, stat := range resp.GetStats() {
		out[stat.GetName()+"/"+stat.GetType()] += stat.GetValue()
	}
	return out, nil
}

func mustLedgerRead(t *testing.T, sb *SingBox, statType common.StatType, name string, reset bool) map[string]int64 {
	t.Helper()
	out, err := ledgerRead(sb, statType, name, reset)
	if err != nil {
		t.Fatalf("GetStats(%s, %q, reset=%v): %v", statType, name, reset, err)
	}
	return out
}

func expectLedger(t *testing.T, label string, got, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

func TestLedgerOutboundsThenInboundsEachReportAllTraffic(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)

	expectLedger(t, "outbounds", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{"Hysteria2/downlink": 1000})
	expectLedger(t, "inbounds", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{"Hysteria2/downlink": 1000})

	expectLedger(t, "outbounds again", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{})
	expectLedger(t, "inbounds again", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{})
}

func TestLedgerInboundsThenOutboundsEachReportAllTraffic(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)

	expectLedger(t, "inbounds", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{"Hysteria2/downlink": 1000})
	expectLedger(t, "outbounds", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{"Hysteria2/downlink": 1000})

	expectLedger(t, "inbounds again", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{})
	expectLedger(t, "outbounds again", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{})
}

func TestLedgerReadWithoutResetKeepsTheBalance(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)

	expectLedger(t, "inbounds without reset", mustLedgerRead(t, sb, common.StatType_Inbounds, "", false), map[string]int64{"Hysteria2/downlink": 1000})
	expectLedger(t, "inbounds with reset", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{"Hysteria2/downlink": 1000})

	fake.add(ledgerHy2Down, 300)
	expectLedger(t, "inbounds newer traffic", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{"Hysteria2/downlink": 300})
	expectLedger(t, "outbounds all traffic", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{"Hysteria2/downlink": 1300})
}

func TestLedgerSingleInboundAndOutboundByTag(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)
	fake.add(ledgerHy2Up, 400)
	fake.add(ledgerAltDown, 700)

	hy2 := map[string]int64{"Hysteria2/downlink": 1000, "Hysteria2/uplink": 400}
	expectLedger(t, "outbound Hysteria2", mustLedgerRead(t, sb, common.StatType_Outbound, "Hysteria2", true), hy2)
	expectLedger(t, "inbound Hysteria2", mustLedgerRead(t, sb, common.StatType_Inbound, "Hysteria2", true), hy2)

	alt := map[string]int64{"Hysteria2-alt/downlink": 700}
	expectLedger(t, "outbounds rest", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), alt)
	expectLedger(t, "inbounds rest", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), alt)

	expectLedger(t, "unknown tag", mustLedgerRead(t, sb, common.StatType_Inbound, "missing", true), map[string]int64{})
}

func TestLedgerSingleReadsStillRequireATag(t *testing.T) {
	sb, _ := newLedgerSingBox(t)

	for _, statType := range []common.StatType{common.StatType_Outbound, common.StatType_Inbound} {
		if _, err := ledgerRead(sb, statType, "", true); err == nil {
			t.Fatalf("GetStats(%s) with an empty tag returned no error", statType)
		}
	}
}

func TestLedgerCoreRestartLosesNothingCollected(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)

	expectLedger(t, "outbounds before restart", mustLedgerRead(t, sb, common.StatType_Outbounds, "", false), map[string]int64{"Hysteria2/downlink": 1000})

	fake.restart()
	fake.add(ledgerHy2Down, 500)

	want := map[string]int64{"Hysteria2/downlink": 1500}
	expectLedger(t, "outbounds after restart", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), want)
	expectLedger(t, "inbounds after restart", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), want)

	expectLedger(t, "outbounds drained", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{})
	expectLedger(t, "inbounds drained", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{})
}

func TestLedgerConcurrentReadsEachSeeTheExactTotal(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)
	fake.add(ledgerHy2Up, 1000)

	const readers = 50
	const writers = 10
	const writes = 200

	var sumMu sync.Mutex
	sums := map[common.StatType]map[string]int64{
		common.StatType_Outbounds: {},
		common.StatType_Inbounds:  {},
	}
	record := func(statType common.StatType, got map[string]int64) {
		sumMu.Lock()
		defer sumMu.Unlock()
		for key, value := range got {
			sums[statType][key] += value
		}
	}

	errs := make(chan error, 2*readers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		for _, statType := range []common.StatType{common.StatType_Outbounds, common.StatType_Inbounds} {
			wg.Add(1)
			go func(statType common.StatType) {
				defer wg.Done()
				<-start
				got, err := ledgerRead(sb, statType, "", true)
				if err != nil {
					errs <- err
					return
				}
				record(statType, got)
			}(statType)
		}
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < writes; j++ {
				fake.add(ledgerHy2Down, int64(i+1))
				fake.add(ledgerHy2Up, 3)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent read failed: %v", err)
	}

	for _, statType := range []common.StatType{common.StatType_Outbounds, common.StatType_Inbounds} {
		got := mustLedgerRead(t, sb, statType, "", true)
		record(statType, got)
	}

	var down int64 = 1000
	for i := 0; i < writers; i++ {
		down += int64(i+1) * writes
	}
	want := map[string]int64{
		"Hysteria2/downlink": down,
		"Hysteria2/uplink":   1000 + 3*writers*writes,
	}
	expectLedger(t, "outbounds series", sums[common.StatType_Outbounds], want)
	expectLedger(t, "inbounds series", sums[common.StatType_Inbounds], want)
}

func TestLedgerDrainErrorKeepsTheBalance(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerHy2Down, 1000)

	fake.failNext()
	if _, err := ledgerRead(sb, common.StatType_Outbounds, "", true); err == nil {
		t.Fatal("a failed drain returned no error")
	}

	want := map[string]int64{"Hysteria2/downlink": 1000}
	expectLedger(t, "outbounds after error", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), want)

	fake.failNext()
	if _, err := ledgerRead(sb, common.StatType_Inbounds, "", true); err == nil {
		t.Fatal("a failed drain returned no error")
	}
	expectLedger(t, "inbounds after error", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), want)
}

func TestLedgerUsersStatStillReadsTheCoreCounters(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add("user>>>alice@example.com>>>traffic>>>uplink", 111)
	fake.add("user>>>bob@example.com>>>traffic>>>downlink", 222)
	fake.add(ledgerHy2Down, 1000)

	expectLedger(t, "outbounds", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{"Hysteria2/downlink": 1000})

	resp, err := sb.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if err != nil {
		t.Fatalf("UsersStat: %v", err)
	}
	req := fake.lastRequest()
	if req == nil || len(req.GetPatterns()) != 1 || req.GetPatterns()[0] != "user>>>" || !req.GetReset_() {
		t.Fatalf("UsersStat reached the core as %+v, want Patterns=[user>>>] Reset_=true", req)
	}
	got := make([]string, 0, len(resp.GetStats()))
	for _, stat := range resp.GetStats() {
		got = append(got, fmt.Sprintf("%s|%s|%s|%d", stat.GetName(), stat.GetLink(), stat.GetType(), stat.GetValue()))
	}
	sort.Strings(got)
	want := []string{"alice@example.com|traffic|uplink|111", "bob@example.com|traffic|downlink|222"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("UsersStat = %v, want %v", got, want)
	}
	if fake.value("user>>>alice@example.com>>>traffic>>>uplink") != 0 {
		t.Fatal("UsersStat with reset did not reset the core's user counter")
	}

	if _, err := sb.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UserStat, Name: "alice@example.com"}); err != nil {
		t.Fatalf("UserStat: %v", err)
	}
	req = fake.lastRequest()
	if req == nil || len(req.GetPatterns()) != 1 || req.GetPatterns()[0] != "user>>>alice@example.com>>>" || req.GetReset_() {
		t.Fatalf("UserStat reached the core as %+v, want Patterns=[user>>>alice@example.com>>>] Reset_=false", req)
	}
}

func TestLedgerRemovedInboundIsReportedOnceMore(t *testing.T) {
	sb, fake := newLedgerSingBox(t)
	fake.add(ledgerOldDown, 900)
	fake.add(ledgerHy2Down, 100)

	both := map[string]int64{"old/downlink": 900, "Hysteria2/downlink": 100}
	expectLedger(t, "inbounds without reset", mustLedgerRead(t, sb, common.StatType_Inbounds, "", false), both)

	fake.remove(ledgerOldDown)

	expectLedger(t, "outbounds", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), both)
	expectLedger(t, "inbounds", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), both)

	expectLedger(t, "outbounds again", mustLedgerRead(t, sb, common.StatType_Outbounds, "", true), map[string]int64{})
	expectLedger(t, "inbounds again", mustLedgerRead(t, sb, common.StatType_Inbounds, "", true), map[string]int64{})
}

func TestLedgerWithoutACoreClientReturnsAnError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("GetStats without a core client panicked: %v", r)
		}
	}()

	sb := &SingBox{}
	for _, statType := range []common.StatType{common.StatType_Outbounds, common.StatType_Inbounds} {
		if _, err := ledgerRead(sb, statType, "", true); err == nil {
			t.Fatalf("GetStats(%s) without a core client returned no error", statType)
		}
	}
}
