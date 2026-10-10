//go:build linux

package singbox

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend/singbox/api"
	statscmd "github.com/pasarguard/node/backend/singbox/api/proto"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const restartModelConfig = `{"log":{"level":"error"},"inbounds":[` +
	`{"type":"hysteria2","tag":"hy2-a","listen":"127.0.0.1","listen_port":1,"users":[]},` +
	`{"type":"hysteria2","tag":"hy2-b","listen":"127.0.0.1","listen_port":2,"users":[]}` +
	`],"outbounds":[{"type":"direct"}]}`

var restartModelTags = []string{"hy2-a", "hy2-b"}

func restartModelUser(name string) *common.User {
	return hy2User(name, "k-"+name, restartModelTags...)
}

type sysStatsModel struct {
	statscmd.UnimplementedStatsServiceServer
	mu      sync.Mutex
	held    bool
	entered chan struct{}
}

func (m *sysStatsModel) GetSysStats(context.Context, *statscmd.SysStatsRequest) (*statscmd.SysStatsResponse, error) {
	m.mu.Lock()
	held := m.held
	entered := m.entered
	m.entered = nil
	m.mu.Unlock()

	if entered != nil {
		close(entered)
	}
	if held {
		return nil, status.Error(codes.Unavailable, "the core API is not ready")
	}
	return &statscmd.SysStatsResponse{Uptime: 1}, nil
}

func (m *sysStatsModel) hold() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = true
	m.entered = make(chan struct{})
	return m.entered
}

func (m *sysStatsModel) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = false
}

type clashUsersModel struct {
	core   *Core
	fail   atomic.Bool
	mu     sync.Mutex
	puts   int
	pid    int
	byTag  map[string][]string
	server *httptest.Server
}

func (c *clashUsersModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tag, ok := strings.CutPrefix(r.URL.Path, "/inbounds/")
	tag, ok2 := strings.CutSuffix(tag, "/users")
	if r.Method != http.MethodPut || !ok || !ok2 {
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Users []struct {
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	c.mu.Lock()
	c.puts++
	c.mu.Unlock()

	if c.fail.Load() {
		http.Error(w, "the hot reload is unavailable", http.StatusServiceUnavailable)
		return
	}

	names := make([]string, 0, len(body.Users))
	for _, user := range body.Users {
		names = append(names, user.Name)
	}
	sort.Strings(names)

	pid := corePID(c.core)
	c.mu.Lock()
	if c.pid != pid {
		c.byTag = make(map[string][]string)
		c.pid = pid
	}
	c.byTag[tag] = names
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *clashUsersModel) putCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts
}

func (c *clashUsersModel) reloaded() (map[string][]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.byTag), c.pid
}

func corePID(c *Core) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.processPID
}

func restartModelExecutable(t *testing.T) string {
	t.Helper()

	exe := filepath.Join(t.TempDir(), "sing-box-restart-model")
	script := "#!/bin/sh\ncase \"$1\" in version) echo \"sing-box version 1.0.0-restart-model\"; exit 0;; esac\nexec sleep 300\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

type restartModel struct {
	sb     *SingBox
	stats  *sysStatsModel
	clash  *clashUsersModel
	dir    string
	health context.Context
}

func newRestartModel(t *testing.T, exe, dir string, users ...*common.User) *restartModel {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stats := &sysStatsModel{}
	server := grpc.NewServer()
	statscmd.RegisterStatsServiceServer(server, stats)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	client, err := api.NewClient(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := NewConfig(restartModelConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg.syncUsers(users)

	core, err := NewCore(exe, dir, 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Start(cfg); err != nil {
		t.Fatal(err)
	}

	health, stopHealth := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stopHealth()
		core.Stop()
		client.Close()
	})

	return &restartModel{
		sb: &SingBox{
			config:     cfg,
			cfg:        &config.Config{StartupLogTailSize: 64},
			core:       core,
			client:     client,
			cancelFunc: stopHealth,
		},
		stats:  stats,
		dir:    dir,
		health: health,
	}
}

func (m *restartModel) enableHotReload(t *testing.T) *clashUsersModel {
	t.Helper()

	clash := &clashUsersModel{core: m.sb.core, byTag: make(map[string][]string)}
	clash.server = httptest.NewServer(clash)
	t.Cleanup(clash.server.Close)

	port := clash.server.Listener.Addr().(*net.TCPAddr).Port
	m.sb.mu.RLock()
	cfg := m.sb.config
	m.sb.mu.RUnlock()
	cfg.mu.Lock()
	cfg.clashPort = port
	cfg.mu.Unlock()

	m.clash = clash
	return clash
}

func userNamesOf(cfg *Config) map[string][]string {
	out := make(map[string][]string)
	for tag, set := range cfg.UserSets() {
		names := make([]string, 0, len(set.Users))
		for _, raw := range set.Users {
			if entry, ok := raw.(map[string]any); ok {
				if name, ok := entry["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
		sort.Strings(names)
		out[tag] = names
	}
	return out
}

func expectUsers(users ...*common.User) map[string][]string {
	names := make([]string, 0, len(users))
	for _, user := range users {
		names = append(names, user.GetEmail())
	}
	sort.Strings(names)
	out := make(map[string][]string, len(restartModelTags))
	for _, tag := range restartModelTags {
		out[tag] = slices.Clone(names)
	}
	return out
}

func sameUserNames(a, b map[string][]string) bool {
	return maps.EqualFunc(a, b, func(x, y []string) bool { return slices.Equal(x, y) })
}

func (m *restartModel) committed() map[string][]string {
	m.sb.mu.RLock()
	cfg := m.sb.config
	m.sb.mu.RUnlock()
	return userNamesOf(cfg)
}

func (m *restartModel) launched(t *testing.T) map[string][]string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(m.dir, "singbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Inbounds []struct {
			Tag   string `json:"tag"`
			Users []struct {
				Name string `json:"name"`
			} `json:"users"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]string)
	for _, inbound := range parsed.Inbounds {
		names := make([]string, 0, len(inbound.Users))
		for _, user := range inbound.Users {
			names = append(names, user.Name)
		}
		sort.Strings(names)
		out[inbound.Tag] = names
	}
	return out
}

func (m *restartModel) live(t *testing.T) map[string][]string {
	t.Helper()

	if m.clash != nil {
		if users, pid := m.clash.reloaded(); pid != 0 && pid == corePID(m.sb.core) && len(users) == len(restartModelTags) {
			return users
		}
	}
	return m.launched(t)
}

func goroutineIn(frames ...string) bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for stack := range strings.SplitSeq(string(buf), "\n\n") {
		matched := true
		for _, frame := range frames {
			if !strings.Contains(stack, frame) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func waitForGoroutineIn(timeout time.Duration, frames ...string) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if goroutineIn(frames...) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestAHealthRestartDoesNotRaceASyncCommittingItsConfig(t *testing.T) {
	alice := restartModelUser("1.alice")
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), alice)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = m.sb.Restart()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	for range 2 {
		if err := m.sb.SyncUsers(ctx, []*common.User{alice}); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestAHealthRestartDuringAFullSyncRestartsTheCoreWithTheSyncedUsers(t *testing.T) {
	alice := restartModelUser("1.alice")
	bob := restartModelUser("2.bob")
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), alice)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	probed := m.stats.hold()
	synced := make(chan error, 1)
	go func() { synced <- m.sb.SyncUsers(ctx, []*common.User{alice, bob}) }()
	select {
	case <-probed:
	case <-time.After(30 * time.Second):
		t.Fatal("the full sync never waited for the restarted core's API")
	}

	restarted := make(chan error, 1)
	go func() { restarted <- m.sb.Restart() }()

	finished := false
	var restartErr error
	deadline := time.Now().Add(15 * time.Second)
	for !finished && !goroutineIn(".(*SingBox).Restart", "sync.(*Mutex).Lock") {
		select {
		case restartErr = <-restarted:
			finished = true
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the health restart neither waited for the running sync nor finished")
		}
	}
	m.stats.release()

	select {
	case err := <-synced:
		if err != nil {
			t.Fatalf("the full sync failed: %v", err)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the full sync never returned")
	}
	if !finished {
		select {
		case restartErr = <-restarted:
		case <-time.After(60 * time.Second):
			t.Fatal("the health restart never returned")
		}
	}
	if restartErr != nil {
		t.Fatalf("the health restart failed: %v", restartErr)
	}

	want := expectUsers(alice, bob)
	if got := m.committed(); !sameUserNames(got, want) {
		t.Fatalf("the sync committed %v, want %v", got, want)
	}
	if got := m.launched(t); !sameUserNames(got, want) {
		t.Fatalf("the core was restarted with %v after the sync committed %v: the health restart replayed the previous user set", got, want)
	}
	if !m.sb.core.Started() {
		t.Fatal("the core must be running after the sync and the health restart")
	}
}

func TestAHealthRestartThatWaitedForASyncDoesNotReviveTheCoreAfterShutdown(t *testing.T) {
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), restartModelUser("1.alice"))
	m.stats.hold()
	m.sb.core.Stop()

	m.sb.syncMu.Lock()
	syncHeld := true
	releaseSync := func() {
		if syncHeld {
			syncHeld = false
			m.sb.syncMu.Unlock()
		}
	}

	healthStopped := make(chan struct{})
	go func() {
		defer close(healthStopped)
		m.sb.checkHealth(m.health)
	}()
	t.Cleanup(func() {
		m.sb.cancelFunc()
		releaseSync()
		select {
		case <-healthStopped:
		case <-time.After(30 * time.Second):
			t.Error("the health loop never stopped")
		}
	})

	if !waitForGoroutineIn(15*time.Second, ".(*SingBox).checkHealth", ".(*SingBox).Restart", "sync.(*Mutex).Lock") {
		t.Fatal("the health restart never waited for the running sync")
	}

	m.sb.Shutdown()
	releaseSync()

	select {
	case <-healthStopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the health loop never stopped after the node shut down")
	}
	if m.sb.core.Started() {
		t.Fatal("a health restart that waited for a sync restarted the core after the node shut down")
	}
}

func TestARestartAfterShutdownIsRefusedAndRevivesNothing(t *testing.T) {
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), restartModelUser("1.alice"))

	m.sb.Shutdown()
	err := m.sb.Restart()
	if m.sb.core.Started() {
		t.Fatal("a restart after shutdown brought the core back")
	}
	if err == nil {
		t.Fatal("a restart after shutdown must be refused")
	}
}

func TestAFullSyncCutShortByShutdownDoesNotReviveTheCore(t *testing.T) {
	alice := restartModelUser("1.alice")
	bob := restartModelUser("2.bob")
	m := newRestartModel(t, restartModelExecutable(t), t.TempDir(), alice)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	probed := m.stats.hold()
	synced := make(chan error, 1)
	go func() { synced <- m.sb.SyncUsers(ctx, []*common.User{alice, bob}) }()
	select {
	case <-probed:
	case <-time.After(30 * time.Second):
		t.Fatal("the full sync never waited for the restarted core's API")
	}

	m.sb.Shutdown()

	revived := false
	for done := false; !done; {
		select {
		case err := <-synced:
			done = true
			if err == nil {
				t.Error("a full sync cut short by shutdown reported success")
			}
		case <-time.After(20 * time.Millisecond):
			if m.sb.core.Started() {
				revived = true
			}
		case <-ctx.Done():
			t.Fatal("the full sync never returned after the node shut down")
		}
	}
	if revived || m.sb.core.Started() {
		t.Fatal("a full sync cut short by shutdown restarted the core after the node shut down")
	}
}

func TestABatchFlushedAfterShutdownLeavesTheReplacementCoreAlone(t *testing.T) {
	for _, hotReload := range []bool{true, false} {
		name := "restart fallback"
		if hotReload {
			name = "hot reload"
		}
		t.Run(name, func(t *testing.T) { batchFlushedAfterShutdown(t, hotReload) })
	}
}

func batchFlushedAfterShutdown(t *testing.T, hotReload bool) {
	exe := restartModelExecutable(t)
	dir := t.TempDir()
	alice := restartModelUser("1.alice")
	bob := restartModelUser("2.bob")
	carol := restartModelUser("3.carol")

	old := newRestartModel(t, exe, dir, alice)
	clash := old.enableHotReload(t)
	clash.fail.Store(!hotReload)
	batch := old.sb.enqueue(func(c *Config) { c.upsertUser(bob) })
	old.sb.Shutdown()

	replacement := newRestartModel(t, exe, dir, carol)
	pid := corePID(replacement.sb.core)

	revived := false
	deadline := time.After(3 * time.Minute)
	for done := false; !done; {
		select {
		case <-batch.done:
			done = true
		case <-time.After(20 * time.Millisecond):
			if old.sb.core.Started() {
				revived = true
			}
		case <-deadline:
			t.Fatal("the batch queued before shutdown never flushed")
		}
	}

	if revived || old.sb.core.Started() {
		t.Error("a batch queued before shutdown restarted the old core after the node shut down")
	}
	if puts := clash.putCount(); puts != 0 {
		t.Errorf("a batch queued before shutdown pushed its user set to the old clash port %d time(s) after the node shut down", puts)
	}
	if batch.err == nil {
		t.Error("a batch that cannot be applied after shutdown must report failure")
	}
	if got, want := replacement.launched(t), expectUsers(carol); !sameUserNames(got, want) {
		t.Errorf("the shut-down backend rewrote the replacement core's config to %v, want %v", got, want)
	}
	if !replacement.sb.core.Started() || corePID(replacement.sb.core) != pid {
		t.Error("the replacement core was restarted or stopped by the shut-down backend")
	}
}
