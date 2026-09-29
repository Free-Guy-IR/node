package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	statscommand "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

type statsModel struct {
	statscommand.UnimplementedStatsServiceServer
	mu      sync.Mutex
	held    bool
	entered chan struct{}
}

func (s *statsModel) GetSysStats(context.Context, *statscommand.SysStatsRequest) (*statscommand.SysStatsResponse, error) {
	s.mu.Lock()
	held := s.held
	entered := s.entered
	s.entered = nil
	s.mu.Unlock()

	if entered != nil {
		close(entered)
	}
	if held {
		return nil, status.Error(codes.Unavailable, "the core API is not ready")
	}
	return &statscommand.SysStatsResponse{}, nil
}

func (s *statsModel) hold() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = true
	s.entered = make(chan struct{})
	return s.entered
}

func (s *statsModel) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = false
}

func (c *modelCore) replaceUsers(state map[string]map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users = make(map[string]map[string]string, len(state))
	for tag, users := range state {
		c.users[tag] = maps.Clone(users)
	}
}

type restartHarness struct {
	x       *Xray
	core    *modelCore
	stats   *statsModel
	written string
}

func newRestartHarness(t *testing.T, inbounds string) *restartHarness {
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
	stats := &statsModel{}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, model)
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
	}
}

func decodeAccounts[T any, P interface {
	*T
	api.Account
}](raw json.RawMessage) ([]api.Account, error) {
	var decoded []T
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	accounts := make([]api.Account, 0, len(decoded))
	for index := range decoded {
		accounts = append(accounts, P(&decoded[index]))
	}
	return accounts, nil
}

func decodeWireAccounts(inbound *Inbound, raw json.RawMessage) ([]api.Account, error) {
	switch inbound.Protocol {
	case Vmess:
		return decodeAccounts[api.VmessAccount](raw)
	case Vless:
		return decodeAccounts[api.VlessAccount](raw)
	case Trojan:
		return decodeAccounts[api.TrojanAccount](raw)
	case Shadowsocks:
		if method, _ := inbound.Settings["method"].(string); strings.HasPrefix(method, "2022-blake3") {
			return decodeAccounts[api.ShadowsocksAccount](raw)
		}
		return decodeAccounts[api.ShadowsocksTcpAccount](raw)
	case Hysteria:
		return decodeAccounts[api.HysteriaAccount](raw)
	}
	return nil, nil
}

func writtenState(x *Xray, raw []byte) (map[string]map[string]string, error) {
	var parsed struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients json.RawMessage `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}

	inbounds := make(map[string]*Inbound, len(x.config.InboundConfigs))
	for _, inbound := range x.config.InboundConfigs {
		inbounds[inbound.Tag] = inbound
	}

	state := make(map[string]map[string]string)
	for _, wire := range parsed.Inbounds {
		inbound := inbounds[wire.Tag]
		if inbound == nil || inbound.exclude || len(wire.Settings.Clients) == 0 {
			continue
		}
		accounts, err := decodeWireAccounts(inbound, wire.Settings.Clients)
		if err != nil {
			return nil, fmt.Errorf("inbound %s: %w", wire.Tag, err)
		}
		placeholder := shadowsocks2022Placeholder(inbound)
		for _, account := range accounts {
			if placeholder != nil && len(accounts) == 1 && localFingerprint(inbound, account) == localFingerprint(inbound, placeholder) {
				continue
			}
			if state[wire.Tag] == nil {
				state[wire.Tag] = make(map[string]string)
			}
			state[wire.Tag][account.GetEmail()] = localFingerprint(inbound, account)
		}
	}
	return state, nil
}

func sameUserState(a, b map[string]map[string]string) bool {
	return maps.EqualFunc(a, b, func(x, y map[string]string) bool { return maps.Equal(x, y) })
}

func (h *restartHarness) requireWrittenMatchesLocal(t *testing.T) {
	t.Helper()

	raw, err := os.ReadFile(h.written)
	if err != nil {
		t.Fatal(err)
	}
	written, err := writtenState(h.x, raw)
	if err != nil {
		t.Fatal(err)
	}
	if local := localState(h.x); !sameUserState(written, local) {
		t.Fatalf("the core was last started with a config that is not the node's state:\n written %v\n local   %v", written, local)
	}
}

func (h *restartHarness) writtenEmails(t *testing.T) map[string][]string {
	t.Helper()

	raw, err := os.ReadFile(h.written)
	if err != nil {
		t.Fatal(err)
	}
	return emittedClients(t, raw)
}

func TestAHealthRestartDuringAFullSyncRestartsTheCoreWithTheSyncedUsers(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	alice := syncTestUser("1.alice", syncTestTags...)
	bob := syncTestUser("2.bob", syncTestTags...)
	if err := h.x.UpdateUsers(ctx, []*common.User{alice}); err != nil {
		t.Fatal(err)
	}

	probed := h.stats.hold()
	synced := make(chan error, 1)
	go func() { synced <- h.x.SyncUsers(ctx, []*common.User{alice, bob}) }()
	select {
	case <-probed:
	case <-time.After(30 * time.Second):
		t.Fatal("the full sync never waited for the restarted core's API")
	}

	restarted := make(chan error, 1)
	go func() { restarted <- h.x.Restart() }()
	var restartErr error
	restartDone := false
	select {
	case restartErr = <-restarted:
		restartDone = true
	case <-time.After(2 * time.Second):
	}
	h.stats.release()

	if err := <-synced; err != nil {
		t.Fatalf("a health restart that raced the full sync made the sync fail: %v", err)
	}
	if !restartDone {
		restartErr = <-restarted
	}
	if restartErr != nil {
		t.Fatal(restartErr)
	}

	written := h.writtenEmails(t)
	for _, tag := range syncTestTags {
		if !slices.Equal(written[tag], []string{"1.alice", "2.bob"}) {
			t.Fatalf("the health restart that raced the full sync left the core with %v on %s, want both synced users", written[tag], tag)
		}
	}
	h.requireWrittenMatchesLocal(t)
}

type restartCall struct {
	id    string
	kind  string
	users []*common.User
}

func (c restartCall) run(ctx context.Context, x *Xray) error {
	ctx = withCall(ctx, c.id)
	switch c.kind {
	case "health":
		return x.Restart()
	case "sync":
		return x.SyncUser(ctx, c.users[0])
	case "bulk":
		return x.UpdateUsers(ctx, c.users)
	case "full":
		return x.SyncUsers(ctx, c.users)
	case "bulk-restart":
		return x.UpdateUsersAndRestart(ctx, c.users)
	}
	return fmt.Errorf("unknown call kind %s", c.kind)
}

func restartsTheCore(kind string) bool {
	return kind == "health" || kind == "full" || kind == "bulk-restart"
}

func restartRound(u interleaveUsers, round int) []restartCall {
	version := func(stage string) string { return fmt.Sprintf("%s-%d", stage, round) }

	bulk := slices.Clone(u.bystanders)
	bulk = append(bulk,
		versionedUser(u.other, version("bulk"), "trojan", "ss"),
		versionedUser(u.revoked, version("bulk"), "vless", "ss"),
	)
	full := slices.Clone(u.bystanders)
	full = append(full,
		versionedUser(u.target, version("full"), "vless", "trojan"),
		versionedUser(u.single, version("full"), "hysteria"),
		versionedUser(u.revoked, version("full"), "vmess"),
	)

	calls := []restartCall{
		{id: fmt.Sprintf("%d/health-a", round), kind: "health"},
		{id: fmt.Sprintf("%d/health-b", round), kind: "health"},
		{id: fmt.Sprintf("%d/sync-target", round), kind: "sync", users: []*common.User{versionedUser(u.target, version("sync"), "vless", "trojan", "vmess")}},
		{id: fmt.Sprintf("%d/revoke-revoked", round), kind: "sync", users: []*common.User{versionedUser(u.revoked, "disabled")}},
		{id: fmt.Sprintf("%d/sync-single", round), kind: "sync", users: []*common.User{versionedUser(u.single, version("single"), "hysteria", "ss2022")}},
		{id: fmt.Sprintf("%d/bulk", round), kind: "bulk", users: bulk},
		{id: fmt.Sprintf("%d/full", round), kind: "full", users: full},
	}
	if round%3 == 0 {
		calls = append(calls, restartCall{
			id:   fmt.Sprintf("%d/bulk-restart", round),
			kind: "bulk-restart",
			users: []*common.User{
				versionedUser(u.target, version("bulk-restart"), "trojan"),
				versionedUser(u.other, version("bulk-restart"), "vless", "hysteria"),
			},
		})
	}
	if round%2 == 1 {
		calls = append(calls, restartCall{id: fmt.Sprintf("%d/revoke-target", round), kind: "sync", users: []*common.User{versionedUser(u.target, "disabled")}})
	}
	return calls
}

type restartAudit struct {
	h         *restartHarness
	mu        sync.Mutex
	order     []string
	pending   bool
	pendingBy string
	problems  []string
}

func (a *restartAudit) entered(ctx context.Context) {
	md, _ := metadata.FromOutgoingContext(ctx)
	id := "health"
	if values := md.Get(syncCallKey); len(values) > 0 {
		id = values[0]
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.settleLocked()
	a.order = append(a.order, id)
	a.pending = id == "health" || strings.HasSuffix(id, "/full") || strings.HasSuffix(id, "/bulk-restart")
	a.pendingBy = id
}

func (a *restartAudit) settle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settleLocked()
}

func (a *restartAudit) settleLocked() {
	if !a.pending {
		return
	}
	a.pending = false

	raw, err := os.ReadFile(a.h.written)
	if err != nil {
		a.problems = append(a.problems, fmt.Sprintf("%s restarted the core but no config was written: %v", a.pendingBy, err))
		return
	}
	written, err := writtenState(a.h.x, raw)
	if err != nil {
		a.problems = append(a.problems, fmt.Sprintf("%s wrote an unreadable config: %v", a.pendingBy, err))
		return
	}
	if local := localState(a.h.x); !sameUserState(written, local) {
		a.problems = append(a.problems, fmt.Sprintf("%s restarted the core with a config that is not the node's committed state:\n written %v\n local   %v", a.pendingBy, written, local))
	}
	a.h.core.replaceUsers(written)
}

func (a *restartAudit) take() ([]string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	order, problems := a.order, a.problems
	a.order, a.problems = nil, nil
	return order, problems
}

func commitCall(committed map[string]*common.User, call restartCall) map[string]*common.User {
	switch call.kind {
	case "full":
		committed = make(map[string]*common.User, len(call.users))
		for _, user := range lastEntryPerEmail(call.users) {
			committed[user.GetEmail()] = user
		}
	case "sync", "bulk", "bulk-restart":
		for _, user := range lastEntryPerEmail(call.users) {
			committed[user.GetEmail()] = user
		}
	}
	return committed
}

func committedState(x *Xray, committed map[string]*common.User) map[string]map[string]string {
	state := make(map[string]map[string]string)
	for email, user := range committed {
		for _, tag := range syncTestTags {
			fingerprint, active := expectedFingerprint(x, user, tag)
			if !active {
				continue
			}
			if state[tag] == nil {
				state[tag] = make(map[string]string)
			}
			state[tag][email] = fingerprint
		}
	}
	return state
}

const restartInterleaveRounds = 12

func TestHealthRestartsRacingUserSyncsNeverReplayAStaleConfig(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)
	audit := &restartAudit{h: h}
	h.x.syncEntered = audit.entered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	users := newInterleaveUsers()
	setup := users.setup()
	if err := h.x.UpdateUsers(withCall(ctx, setup.id), setup.users); err != nil {
		t.Fatal(err)
	}
	committed := commitCall(make(map[string]*common.User), restartCall{kind: "bulk", users: setup.users})
	audit.settle()
	if _, problems := audit.take(); len(problems) != 0 {
		t.Fatal(problems)
	}

	restarts, healthCalls, healthEntries := 0, 0, 0
	for round := range restartInterleaveRounds {
		calls := restartRound(users, round)
		byID := make(map[string]restartCall, len(calls))
		for _, call := range calls {
			byID[call.id] = call
		}

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
		wg.Wait()
		for index, err := range errs {
			if err != nil {
				t.Fatalf("round %d: call %s failed: %v", round, calls[index].id, err)
			}
		}

		audit.settle()
		order, problems := audit.take()
		if len(problems) != 0 {
			t.Fatalf("round %d:\n%s", round, strings.Join(problems, "\n"))
		}

		roundHealthCalls, roundHealthEntries := 0, 0
		for _, call := range calls {
			if call.kind == "health" {
				roundHealthCalls++
			}
		}
		for _, id := range order {
			if id == "health" {
				roundHealthEntries++
				restarts++
				continue
			}
			call, known := byID[id]
			if !known {
				t.Fatalf("round %d: unknown or stale call %s took the sync lock", round, id)
			}
			if restartsTheCore(call.kind) {
				restarts++
			}
			committed = commitCall(committed, call)
		}
		if len(order)-roundHealthEntries != len(calls)-roundHealthCalls {
			t.Fatalf("round %d: %d user calls took the sync lock, want %d: %v", round, len(order)-roundHealthEntries, len(calls)-roundHealthCalls, order)
		}
		healthCalls += roundHealthCalls
		healthEntries += roundHealthEntries

		if want, got := committedState(h.x, committed), localState(h.x); !sameUserState(want, got) {
			t.Fatalf("round %d: the node lost a user update:\n want %v\n got  %v", round, want, got)
		}
		requireCoreMatchesLocal(t, h.x, h.core)
	}

	if err := h.x.Restart(); err != nil {
		t.Fatal(err)
	}
	healthCalls++
	audit.settle()
	order, problems := audit.take()
	if len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	healthEntries += len(order)
	if healthEntries != healthCalls {
		t.Fatalf("%d of %d health restarts took the sync lock", healthEntries, healthCalls)
	}
	h.requireWrittenMatchesLocal(t)
	if want, got := committedState(h.x, committed), localState(h.x); !sameUserState(want, got) {
		t.Fatalf("the node lost a user update:\n want %v\n got  %v", want, got)
	}
	requireCoreMatchesLocal(t, h.x, h.core)
	t.Logf("%d core restarts audited across %d rounds", restarts+1, restartInterleaveRounds)
}
