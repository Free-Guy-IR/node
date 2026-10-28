package xray

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	statscommand "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func cancelledBySyncAbort(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "context canceled") || strings.Contains(message, "context cancelled") || strings.Contains(message, "code = Canceled")
}

type gatedCore struct {
	*modelCore
	mu        sync.Mutex
	stuckID   string
	reached   chan struct{}
	signalled bool
}

func (g *gatedCore) gate(call string) <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stuckID = call
	g.reached = make(chan struct{})
	g.signalled = false
	return g.reached
}

func (g *gatedCore) AlterInbound(ctx context.Context, request *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	g.mu.Lock()
	held := g.stuckID != "" && callFromContext(ctx) == g.stuckID
	if held && !g.signalled {
		g.signalled = true
		close(g.reached)
	}
	g.mu.Unlock()

	if held {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return g.modelCore.AlterInbound(ctx, request)
}

func newGatedRestartHarness(t *testing.T, inbounds string) (*restartHarness, *gatedCore) {
	t.Helper()

	dir := t.TempDir()
	executable := filepath.Join(dir, "xray-restart-model")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	model := newModelCore(false)
	gated := &gatedCore{modelCore: model}
	stats := &statsModel{}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, gated)
	statscommand.RegisterStatsServiceServer(server, stats)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	handler, err := api.NewXrayAPI(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)

	cfg, err := NewConfig(inbounds, nil)
	if err != nil {
		t.Fatal(err)
	}

	core := &Core{
		executablePath: executable,
		assetsPath:     dir,
		configPath:     dir,
		logsChan:       make(chan string, 64),
		logPhase:       logPhaseRuntime,
		startupLogSize: 200,
		runtimeLogs:    newStartupLogRing(10),
	}
	t.Cleanup(core.Stop)

	return &restartHarness{
		x:       &Xray{config: cfg, handler: handler, core: core, cfg: &config.Config{Debug: true}},
		core:    model,
		stats:   stats,
		written: filepath.Join(dir, "xray.json"),
	}, gated
}

func requireHeldAccount(t *testing.T, x *Xray, user *common.User, tags ...string) {
	t.Helper()

	local := localState(x)
	for _, tag := range tags {
		want, active := expectedFingerprint(x, user, tag)
		if !active {
			t.Fatalf("%s is not active on %s in the test setup", user.GetEmail(), tag)
		}
		if got := local[tag][user.GetEmail()]; got != want {
			t.Fatalf("the node lost %s's account on %s: holds %q, want %q", user.GetEmail(), tag, got, want)
		}
	}
}

func TestAUserSyncShorterThanTheGraceIsNotCancelledByAHealthRestart(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 3 * time.Second

	stalled := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		once.Do(func() {
			close(stalled)
			time.Sleep(time.Second)
		})
		return false, nil
	})

	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUser(context.Background(), syncTestUser("1.alice", syncTestTags...)) }()
	select {
	case <-stalled:
	case <-time.After(30 * time.Second):
		t.Fatal("the user sync never reached the core")
	}

	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatalf("a health restart cancelled a user sync whose core calls kept answering within the grace: %v", err)
	}
	h.requireWrittenMatchesLocal(t)
	if len(localState(h.x)["vless"]) != 1 {
		t.Fatalf("the synced user is missing from the node's state: %v", localState(h.x))
	}
}

func TestAHealthRestartCancelsAUserSyncStuckPastTheGrace(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = time.Second

	unstick := make(chan struct{})
	stuck := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		once.Do(func() { close(stuck) })
		<-unstick
		return false, nil
	})

	alice := syncTestUser("1.alice", syncTestTags...)
	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUser(context.Background(), alice) }()
	t.Cleanup(func() { close(unstick) })
	select {
	case <-stuck:
	case <-time.After(30 * time.Second):
		t.Fatal("the user sync never reached the core")
	}

	started := time.Now()
	restarted := make(chan error, 1)
	go func() { restarted <- h.x.Restart() }()
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the health restart never recovered the core a user sync was stuck on")
	}

	select {
	case err := <-synced:
		if !cancelledBySyncAbort(err) {
			t.Fatalf("the stuck user sync returned %v, want it cancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stuck user sync was never cancelled")
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart did not leave a running core")
	}
	if elapsed := time.Since(started); elapsed > 45*time.Second {
		t.Fatalf("the health restart took %s to recover a stuck core", elapsed)
	}
	requireHeldAccount(t, h.x, alice, syncTestTags...)
	h.requireWrittenMatchesLocal(t)
}

func TestAHealthRestartKeepsTheAccountsOfTheSyncItCancelled(t *testing.T) {
	h, gated := newGatedRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	alice := versionedUser("1.alice", "old", "vless", "trojan", "vmess")
	bob := versionedUser("2.bob", "old", "vless", "trojan")
	if err := h.x.UpdateUsers(withCall(ctx, "setup"), []*common.User{alice, bob}); err != nil {
		t.Fatal(err)
	}
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}

	rotated := versionedUser("1.alice", "new", "vless", "trojan", "vmess")
	carol := versionedUser("3.carol", "new", "vless", "trojan")
	reached := gated.gate("stuck")
	updated := make(chan error, 1)
	go func() {
		updated <- h.x.UpdateUsers(withCall(ctx, "stuck"), []*common.User{rotated, carol, versionedUser("2.bob", "disabled")})
	}()
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the bulk update never reached the core")
	}

	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-updated:
		if !cancelledBySyncAbort(err) {
			t.Fatalf("the stuck bulk update returned %v, want it cancelled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the stuck bulk update was never cancelled")
	}

	requireHeldAccount(t, h.x, rotated, "vless", "trojan", "vmess")
	requireHeldAccount(t, h.x, carol, "vless", "trojan")
	for tag, users := range localState(h.x) {
		if _, held := users["2.bob"]; held {
			t.Fatalf("the node still holds the removed user on %s", tag)
		}
	}
	h.requireWrittenMatchesLocal(t)
}

func TestAHealthRestartThatCancelledASyncRestartsEvenIfTheCoreWasReplaced(t *testing.T) {
	h, gated := newGatedRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	observed := h.x.core.state.Load()
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	replaced := h.x.core.state.Load()

	alice := versionedUser("1.alice", "stuck", "vless", "trojan")
	reached := gated.gate("stuck")
	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUser(withCall(ctx, "stuck"), alice) }()
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the user sync never reached the core")
	}

	restarted, err := h.x.RestartUnlessReplaced(context.Background(), observed)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted || h.x.core.state.Load() == replaced {
		t.Fatal("a health restart that cancelled a user sync skipped the restart, so the cancelled sync's staged accounts never reached the core")
	}
	if err := <-synced; !cancelledBySyncAbort(err) {
		t.Fatalf("the stuck user sync returned %v, want it cancelled", err)
	}
	requireHeldAccount(t, h.x, alice, "vless", "trojan")
	h.requireWrittenMatchesLocal(t)
}

func TestAHealthRestartStillRestartsACoreThatWasStoppedOrNeverReplaced(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)

	observed := h.x.core.state.Load()
	restarted, err := h.x.RestartUnlessReplaced(context.Background(), observed)
	if err != nil || !restarted {
		t.Fatalf("a health restart of a core nobody replaced returned restarted=%v err=%v", restarted, err)
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart did not start the core")
	}

	observed = h.x.core.state.Load()
	h.x.core.Stop()
	restarted, err = h.x.RestartUnlessReplaced(context.Background(), observed)
	if err != nil || !restarted {
		t.Fatalf("a health restart of a core that was stopped meanwhile returned restarted=%v err=%v", restarted, err)
	}
	if !h.x.core.Started() {
		t.Fatal("the health restart left the stopped core down")
	}

	observed = h.x.core.state.Load()
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	replaced := h.x.core.state.Load()
	restarted, err = h.x.RestartUnlessReplaced(context.Background(), observed)
	if err != nil || restarted {
		t.Fatalf("a health restart of a core that was already replaced and runs returned restarted=%v err=%v", restarted, err)
	}
	if h.x.core.state.Load() != replaced {
		t.Fatal("a skipped health restart still replaced the running core")
	}
}

const stuckSyncRounds = 8

func TestHealthRestartsCancellingStuckSyncsNeverLeaveTheCoreBehindTheNode(t *testing.T) {
	h, gated := newGatedRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 2 * time.Second
	audit := &restartAudit{h: h}
	h.x.syncEntered = audit.entered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	users := newInterleaveUsers()
	setup := users.setup()
	if err := h.x.UpdateUsers(withCall(ctx, setup.id), setup.users); err != nil {
		t.Fatal(err)
	}
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(problems)
	}

	for round := range stuckSyncRounds {
		stuckID := fmt.Sprintf("%d/stuck", round)
		reached := gated.gate(stuckID)
		stuckDone := make(chan error, 1)
		go func() {
			stuckDone <- h.x.SyncUser(withCall(ctx, stuckID), versionedUser("14.stuck", stuckID, "vless", "trojan", "vmess"))
		}()
		select {
		case <-reached:
		case <-time.After(30 * time.Second):
			t.Fatalf("round %d: the stuck sync never reached the core", round)
		}

		calls := restartRound(users, round)
		start := make(chan struct{})
		errs := make([]error, len(calls))
		var wg sync.WaitGroup
		for index, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[index] = call.run(ctx, h.x)
			}()
		}
		close(start)

		finished := make(chan struct{})
		go func() {
			wg.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Minute):
			t.Fatalf("round %d: a call never returned", round)
		}
		select {
		case err := <-stuckDone:
			if !cancelledBySyncAbort(err) {
				t.Fatalf("round %d: the stuck sync returned %v, want it cancelled by a waiting health restart", round, err)
			}
		case <-time.After(time.Minute):
			t.Fatalf("round %d: the stuck sync never returned", round)
		}

		for index, err := range errs {
			switch {
			case err == nil:
			case calls[index].kind != "health" && cancelledBySyncAbort(err):
			default:
				t.Fatalf("round %d: call %s failed: %v", round, calls[index].id, err)
			}
		}

		audit.settle()
		if _, problems := audit.take(); len(problems) != 0 {
			t.Fatalf("round %d:\n%s", round, strings.Join(problems, "\n"))
		}
		requireCoreMatchesLocal(t, h.x, h.core)
	}

	gated.gate("")
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	h.requireWrittenMatchesLocal(t)
	requireCoreMatchesLocal(t, h.x, h.core)
}

func TestAHealthRestartRacingASlowSyncAndNodePushesLosesNoUpdate(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	h.x.stuckGrace = 2 * time.Second
	audit := &restartAudit{h: h}
	h.x.syncEntered = audit.entered
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	gone := versionedUser("3.gone", "setup", "vless", "trojan")
	pushed := versionedUser("2.pushed", "setup", "vless", "vmess")
	if err := h.x.UpdateUsers(withCall(ctx, "setup"), []*common.User{gone, pushed}); err != nil {
		t.Fatal(err)
	}
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(problems)
	}

	reached := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		if op == addOperation && strings.HasSuffix(email, ".slow") {
			once.Do(func() { close(reached) })
			time.Sleep(300 * time.Millisecond)
		}
		return false, nil
	})

	slow := make([]*common.User, 0, 8)
	for index := range 8 {
		slow = append(slow, versionedUser(fmt.Sprintf("%d.slow", index+10), "bulk", syncTestTags...))
	}
	rotated := versionedUser("2.pushed", "rotated", "vless", "vmess")
	revoked := versionedUser("3.gone", "disabled")

	results := make(map[string]chan error)
	run := func(id string, call func() error) {
		done := make(chan error, 1)
		results[id] = done
		go func() { done <- call() }()
	}
	run("slow", func() error { return h.x.UpdateUsers(withCall(ctx, "slow"), slow) })
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the slow sync never reached the core")
	}
	run("health", h.x.Restart)
	run("push-rotate", func() error { return h.x.SyncUser(withCall(ctx, "push-rotate"), rotated) })
	run("push-revoke", func() error { return h.x.SyncUser(withCall(ctx, "push-revoke"), revoked) })

	for id, done := range results {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s failed while a health restart raced a slow but answering sync: %v", id, err)
			}
		case <-time.After(3 * time.Minute):
			t.Fatalf("%s never returned", id)
		}
	}

	for _, user := range slow {
		requireHeldAccount(t, h.x, user, syncTestTags...)
	}
	requireHeldAccount(t, h.x, rotated, "vless", "vmess")
	for tag, users := range localState(h.x) {
		if _, held := users["3.gone"]; held {
			t.Fatalf("the revoked user is still held on %s", tag)
		}
	}

	h.core.setFault(nil)
	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	h.requireWrittenMatchesLocal(t)
	requireCoreMatchesLocal(t, h.x, h.core)
}
