package mtproto

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/mtglib"

	"github.com/pasarguard/node/backend/mtproto/middleproxy"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/stats"
)

const (
	handoverTagA = "mtproto-ho-a"
	handoverTagB = "mtproto-ho-b"
	handoverNode = "mtproto"

	aliceEmail = "alice@example.com"
	bobEmail   = "bob@example.com"
)

func clearAccountingHandOver(t *testing.T) {
	t.Helper()
	takeHandOver()
	t.Cleanup(func() { takeHandOver() })
}

func handoverConfig(t *testing.T, tags ...string) string {
	t.Helper()
	ports := unusedListenPorts(t, len(tags))
	instances := make([]string, 0, len(tags))
	for i, tag := range tags {
		instances = append(instances, fmt.Sprintf(`{"tag":%q,"port":%d,"mode":"plain"}`, tag, ports[i]))
	}
	return `{"instances":[` + strings.Join(instances, ",") + `]}`
}

func startHandover(t *testing.T, users []*common.User, tags ...string) *Backend {
	t.Helper()
	b := startBackend(t, handoverConfig(t, tags...), users)
	t.Cleanup(b.Shutdown)
	return b
}

func handoverStream(t *testing.T, b *Backend, tag string) *mtprotoEventStream {
	t.Helper()
	b.mu.RLock()
	inst, ok := b.instances[tag]
	b.mu.RUnlock()
	if !ok || inst.accumulator == nil {
		t.Fatalf("instance %q is not running", tag)
	}
	return newMtprotoEventStream(inst.accumulator, b.authenticatedEmail)
}

func sendTraffic(t *testing.T, b *Backend, tag, streamID, username string, up, down uint) {
	t.Helper()
	stream := handoverStream(t, b, tag)
	ctx := context.Background()
	stream.Send(ctx, mtglib.NewEventAuthenticated(streamID, username))
	if up > 0 {
		stream.Send(ctx, mtglib.NewEventTraffic(streamID, up, false))
	}
	if down > 0 {
		stream.Send(ctx, mtglib.NewEventTraffic(streamID, down, true))
	}
}

func finishStream(t *testing.T, b *Backend, tag, streamID string) {
	t.Helper()
	handoverStream(t, b, tag).Send(context.Background(), mtglib.NewEventFinish(streamID))
}

func readHandover(t *testing.T, b *Backend, statType common.StatType, name string, reset bool) map[string]int64 {
	t.Helper()
	resp, err := b.GetStats(context.Background(), &common.StatRequest{Type: statType, Name: name, Reset_: reset})
	if err != nil {
		t.Fatalf("GetStats(%v, %q, reset=%v): %v", statType, name, reset, err)
	}
	out := map[string]int64{}
	for _, s := range resp.GetStats() {
		key := s.GetName() + "|" + s.GetLink() + "|" + s.GetType()
		if _, dup := out[key]; dup {
			t.Fatalf("GetStats(%v, reset=%v) reported %s twice", statType, reset, key)
		}
		out[key] = s.GetValue()
	}
	return out
}

func describeTraffic(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.FormatInt(m[k], 10))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func expectTraffic(t *testing.T, what string, got, want map[string]int64) {
	t.Helper()
	if describeTraffic(got) != describeTraffic(want) {
		t.Fatalf("%s: got %s, want %s", what, describeTraffic(got), describeTraffic(want))
	}
}

func userTraffic(email, tag string, up, down int64) map[string]int64 {
	out := map[string]int64{}
	if up > 0 {
		out[email+"|"+tag+"|uplink"] = up
	}
	if down > 0 {
		out[email+"|"+tag+"|downlink"] = down
	}
	return out
}

func mergeTraffic(parts ...map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for _, part := range parts {
		for k, v := range part {
			out[k] += v
		}
	}
	return out
}

func expectAllRead(t *testing.T, what string, b *Backend, users, inbounds, outbounds map[string]int64) {
	t.Helper()
	expectTraffic(t, what+": users", readHandover(t, b, common.StatType_UsersStat, "", true), users)
	expectTraffic(t, what+": inbounds", readHandover(t, b, common.StatType_Inbounds, "", true), inbounds)
	expectTraffic(t, what+": outbounds", readHandover(t, b, common.StatType_Outbounds, "", true), outbounds)
}

func expectNothingLeft(t *testing.T, what string, b *Backend) {
	t.Helper()
	expectAllRead(t, what, b, map[string]int64{}, map[string]int64{}, map[string]int64{})
}

func TestMTProtoStartCarriesUnreadTrafficExactlyOnce(t *testing.T) {
	clearAccountingHandOver(t)
	users := []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}

	first := startHandover(t, users, handoverTagA)
	sendTraffic(t, first, handoverTagA, "s1", aliceEmail, 120, 900)
	finishStream(t, first, handoverTagA, "s1")
	first.Shutdown()

	second := startHandover(t, users, handoverTagA)

	expectTraffic(t, "a peek right after the Start",
		readHandover(t, second, common.StatType_UserStat, aliceEmail, false),
		userTraffic(aliceEmail, handoverTagA, 120, 900))
	expectAllRead(t, "first read after the Start", second,
		userTraffic(aliceEmail, handoverTagA, 120, 900),
		userTraffic(handoverTagA, handoverTagA, 120, 900),
		userTraffic(handoverNode, handoverNode, 120, 900))
	expectNothingLeft(t, "second read after the Start", second)

	sendTraffic(t, second, handoverTagA, "s2", aliceEmail, 7, 11)
	expectAllRead(t, "read after new growth", second,
		userTraffic(aliceEmail, handoverTagA, 7, 11),
		userTraffic(handoverTagA, handoverTagA, 7, 11),
		userTraffic(handoverNode, handoverNode, 7, 11))
	expectNothingLeft(t, "read after the growth was reported", second)
}

func TestMTProtoTwoStartsWithoutAReadCarryEveryByteOnce(t *testing.T) {
	clearAccountingHandOver(t)
	users := []*common.User{
		credentialedUser(aliceEmail, initialSecretA, handoverTagA),
		credentialedUser(bobEmail, initialSecretB, handoverTagA),
	}

	first := startHandover(t, users, handoverTagA)
	sendTraffic(t, first, handoverTagA, "s1", aliceEmail, 100, 200)
	first.Shutdown()

	second := startHandover(t, users, handoverTagA)
	sendTraffic(t, second, handoverTagA, "s2", aliceEmail, 10, 20)
	sendTraffic(t, second, handoverTagA, "s3", bobEmail, 1, 2)
	second.Shutdown()

	third := startHandover(t, users, handoverTagA)
	expectAllRead(t, "first read on the third backend", third,
		mergeTraffic(userTraffic(aliceEmail, handoverTagA, 110, 220), userTraffic(bobEmail, handoverTagA, 1, 2)),
		userTraffic(handoverTagA, handoverTagA, 111, 222),
		userTraffic(handoverNode, handoverNode, 111, 222))
	expectNothingLeft(t, "second read on the third backend", third)
}

func TestMTProtoFailedNewPutsTheHandOverBack(t *testing.T) {
	clearAccountingHandOver(t)
	users := []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA, handoverTagB)}

	first := startHandover(t, users, handoverTagA)
	sendTraffic(t, first, handoverTagA, "s1", aliceEmail, 300, 4000)
	first.Shutdown()

	ports := unusedListenPorts(t, 2)
	occupant, err := net.Listen("tcp", ":"+strconv.Itoa(ports[1]))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	raw := fmt.Sprintf(`{"instances":[{"tag":%q,"port":%d,"mode":"plain"},{"tag":%q,"port":%d,"mode":"plain"}]}`,
		handoverTagA, ports[0], handoverTagB, ports[1])
	mtCfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if failed, err := New(context.Background(), mtCfg, users, &config.Config{LogBufferSize: 4096, StatsUpdateIntervalSeconds: 3600}); err == nil {
		failed.Shutdown()
		occupant.Close()
		t.Fatal("New must fail while an instance port is taken")
	}
	occupant.Close()

	second := startHandover(t, users, handoverTagA)
	expectAllRead(t, "first read after the failed Start", second,
		userTraffic(aliceEmail, handoverTagA, 300, 4000),
		userTraffic(handoverTagA, handoverTagA, 300, 4000),
		userTraffic(handoverNode, handoverNode, 300, 4000))
	expectNothingLeft(t, "second read after the failed Start", second)
}

func TestMTProtoStartReportsAbsentUsersAndTagsOnceThenForgetsThem(t *testing.T) {
	clearAccountingHandOver(t)

	first := startHandover(t, []*common.User{
		credentialedUser(aliceEmail, initialSecretA, handoverTagA, handoverTagB),
		credentialedUser(bobEmail, initialSecretB, handoverTagA),
	}, handoverTagA, handoverTagB)
	sendTraffic(t, first, handoverTagA, "s1", aliceEmail, 10, 20)
	sendTraffic(t, first, handoverTagB, "s2", aliceEmail, 30, 40)
	sendTraffic(t, first, handoverTagA, "s3", bobEmail, 5, 6)
	first.Shutdown()

	second := startHandover(t, []*common.User{
		credentialedUser(aliceEmail, initialSecretA, handoverTagA),
	}, handoverTagA)

	expectAllRead(t, "first read after the Start", second,
		mergeTraffic(
			userTraffic(aliceEmail, handoverTagA, 10, 20),
			userTraffic(aliceEmail, handoverTagB, 30, 40),
			userTraffic(bobEmail, handoverTagA, 5, 6),
		),
		mergeTraffic(
			userTraffic(handoverTagA, handoverTagA, 15, 26),
			userTraffic(handoverTagB, handoverTagB, 30, 40),
		),
		userTraffic(handoverNode, handoverNode, 45, 66))
	expectNothingLeft(t, "second read after the Start", second)

	keys := second.statsTracker.Keys()
	sort.Strings(keys)
	if strings.Join(keys, ",") != statsKey(handoverTagA, aliceEmail) {
		t.Fatalf("absent users and tags must be forgotten once reported, tracker keys: %v", keys)
	}

	sendTraffic(t, second, handoverTagA, "s4", aliceEmail, 2, 3)
	expectAllRead(t, "read after new growth", second,
		userTraffic(aliceEmail, handoverTagA, 2, 3),
		userTraffic(handoverTagA, handoverTagA, 2, 3),
		userTraffic(handoverNode, handoverNode, 2, 3))
}

func TestMTProtoStartKeepsOnlineAnswersLive(t *testing.T) {
	clearAccountingHandOver(t)
	ctx := context.Background()

	first := startHandover(t, []*common.User{
		credentialedUser(aliceEmail, initialSecretA, handoverTagA),
		credentialedUser(bobEmail, initialSecretB, handoverTagA),
	}, handoverTagA)
	sendTraffic(t, first, handoverTagA, "live-alice", aliceEmail, 50, 60)
	sendTraffic(t, first, handoverTagA, "live-bob", bobEmail, 5, 6)
	first.Shutdown()

	second := startHandover(t, []*common.User{
		credentialedUser(aliceEmail, initialSecretA, handoverTagA),
	}, handoverTagA)

	for _, email := range []string{aliceEmail, bobEmail} {
		online, err := second.GetUserOnlineStats(ctx, email)
		if err != nil {
			t.Fatalf("GetUserOnlineStats(%s): %v", email, err)
		}
		if online.GetValue() != 0 {
			t.Fatalf("%s has no stream on the new backend and must be offline, got %d", email, online.GetValue())
		}
		ips, err := second.GetUserOnlineIpListStats(ctx, email)
		if err != nil {
			t.Fatalf("GetUserOnlineIpListStats(%s): %v", email, err)
		}
		if len(ips.GetIps()) != 0 {
			t.Fatalf("%s: mtproto has no ip source, got %v", email, ips.GetIps())
		}
	}

	expectTraffic(t, "carried traffic of the live streams",
		readHandover(t, second, common.StatType_UsersStat, "", true),
		mergeTraffic(userTraffic(aliceEmail, handoverTagA, 50, 60), userTraffic(bobEmail, handoverTagA, 5, 6)))

	sendTraffic(t, second, handoverTagA, "again-alice", aliceEmail, 1, 1)
	online, err := second.GetUserOnlineStats(ctx, aliceEmail)
	if err != nil {
		t.Fatalf("GetUserOnlineStats: %v", err)
	}
	if online.GetValue() != 1 {
		t.Fatalf("alice streams on the new backend and must be online, got %d", online.GetValue())
	}
}

func TestMTProtoStoppedBackendCannotReportWhatItHandedOver(t *testing.T) {
	clearAccountingHandOver(t)
	users := []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}

	first := startHandover(t, users, handoverTagA)
	sendTraffic(t, first, handoverTagA, "s1", aliceEmail, 40, 400)
	first.Shutdown()

	for _, statType := range []common.StatType{common.StatType_UsersStat, common.StatType_Inbounds, common.StatType_Outbounds} {
		resp, err := first.GetStats(context.Background(), &common.StatRequest{Type: statType, Reset_: true})
		if err == nil && len(resp.GetStats()) != 0 {
			t.Fatalf("a stopped backend reported %v for %v after handing its accounting over", resp.GetStats(), statType)
		}
	}
	first.refreshUserStats()

	second := startHandover(t, users, handoverTagA)
	expectAllRead(t, "first read on the new backend", second,
		userTraffic(aliceEmail, handoverTagA, 40, 400),
		userTraffic(handoverTagA, handoverTagA, 40, 400),
		userTraffic(handoverNode, handoverNode, 40, 400))
}

func TestMTProtoDoubleShutdownKeepsTheHandOver(t *testing.T) {
	clearAccountingHandOver(t)
	users := []*common.User{credentialedUser(aliceEmail, initialSecretA, handoverTagA)}

	first := startHandover(t, users, handoverTagA)
	sendTraffic(t, first, handoverTagA, "s1", aliceEmail, 8, 9)
	first.Shutdown()
	first.Shutdown()

	second := startHandover(t, users, handoverTagA)
	expectTraffic(t, "users after a repeated Shutdown",
		readHandover(t, second, common.StatType_UsersStat, "", true),
		userTraffic(aliceEmail, handoverTagA, 8, 9))
}

func TestMTProtoShutdownDrainsMiddleProxyStreamsBeforeHandingOver(t *testing.T) {
	clearAccountingHandOver(t)

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	b := &Backend{
		instances:       map[string]*proxyInstance{},
		order:           []string{handoverTagA},
		inboundCounters: map[string]*inboundCounter{handoverTagA: {}},
		statsTracker:    stats.New(),
		logsChan:        make(chan string, 64),
		secretsByID:     map[string]secretEntry{aliceEmail: {email: aliceEmail}},
	}
	counter := b.inboundCounters[handoverTagA]
	accumulator := newEventAccumulator(&b.outboundRx, &b.outboundTx, &counter.rx, &counter.tx)
	pi := &proxyInstance{
		tag:         handoverTagA,
		mp:          middleproxy.New(middleproxy.Options{}),
		accumulator: accumulator,
		streams:     newStreamSet(),
	}
	b.instances[handoverTagA] = pi

	listener := pi.streams.track(raw)
	serveDone := make(chan struct{})
	pi.listener = listener
	pi.serveDone = serveDone

	var relayed atomic.Bool
	accepted := make(chan struct{})
	go func() {
		defer close(serveDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accumulator.authenticated("mp-stream", aliceEmail, aliceEmail)
		accumulator.traffic("mp-stream", 25, false)
		close(accepted)
		go func() {
			defer conn.Close()
			buf := make([]byte, 16)
			for {
				if _, err := conn.Read(buf); err != nil {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			accumulator.traffic("mp-stream", 700, true)
			relayed.Store(true)
			accumulator.finished("mp-stream")
		}()
		for {
			if _, err := listener.Accept(); err != nil {
				return
			}
		}
	}()

	client, err := net.Dial("tcp", raw.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was never accepted")
	}

	b.Shutdown()

	if !relayed.Load() {
		t.Fatal("Shutdown handed the accounting over while a middle-proxy stream was still relaying")
	}
	h := takeHandOver()
	if h == nil || h.statsTracker == nil {
		t.Fatal("Shutdown left no accounting hand-over")
	}
	expectTraffic(t, "handed-over users",
		rewriteTrafficLinks(h.statsTracker.GetUsersStats(context.Background(), true)),
		userTraffic(aliceEmail, handoverTagA, 25, 700))
}

func rewriteTrafficLinks(resp *common.StatResponse) map[string]int64 {
	out := map[string]int64{}
	for _, s := range rewriteLinkToTag(resp).GetStats() {
		out[s.GetName()+"|"+s.GetLink()+"|"+s.GetType()] += s.GetValue()
	}
	return out
}
