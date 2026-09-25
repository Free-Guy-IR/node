package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func versionedUser(email string, version int) *common.User {
	return &common.User{Email: email, Inbounds: []string{strconv.Itoa(version)}}
}

func userVersion(u *common.User) int {
	if len(u.GetInbounds()) == 0 {
		return 0
	}
	v, _ := strconv.Atoi(u.GetInbounds()[0])
	return v
}

type panelLedger struct {
	mu        sync.Mutex
	written   map[string]int
	acked     map[string]int
	delivered map[string]int
	capture   bool
	snapshot  map[string]int
}

func newPanelLedger() *panelLedger {
	return &panelLedger{written: map[string]int{}, acked: map[string]int{}, delivered: map[string]int{}}
}

func (p *panelLedger) deliver(users []*common.User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range users {
		if v := userVersion(u); v > p.delivered[u.GetEmail()] {
			p.delivered[u.GetEmail()] = v
		}
	}
}

func (p *panelLedger) undelivered(users []*common.User) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var missing []string
	for _, u := range users {
		if p.delivered[u.GetEmail()] < userVersion(u) {
			missing = append(missing, fmt.Sprintf("%s@%d", u.GetEmail(), userVersion(u)))
		}
	}
	return missing
}

func (p *panelLedger) write(email string) *common.User {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written[email]++
	return versionedUser(email, p.written[email])
}

func (p *panelLedger) ack(users []*common.User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range users {
		if v := userVersion(u); v > p.acked[u.GetEmail()] {
			p.acked[u.GetEmail()] = v
		}
	}
}

func (p *panelLedger) beginLifecycleOp() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.capture = true
	p.snapshot = nil
}

func (p *panelLedger) endLifecycleOp() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.capture = false
	p.snapshot = nil
}

func (p *panelLedger) lifecycleChangeReachedABackend() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.capture && p.snapshot == nil {
		p.snapshot = maps.Clone(p.written)
	}
}

func (p *panelLedger) startSnapshot() map[string]int {
	p.lifecycleChangeReachedABackend()
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.snapshot)
}

type ledgerBackend struct {
	backend.Backend
	ledger  *panelLedger
	started atomic.Bool
	mu      sync.Mutex
	users   map[string]int
}

func newLedgerBackend(ledger *panelLedger, snapshot map[string]int) *ledgerBackend {
	if snapshot == nil {
		snapshot = map[string]int{}
	}
	b := &ledgerBackend{ledger: ledger, users: snapshot}
	b.started.Store(true)
	return b
}

func (b *ledgerBackend) Started() bool   { return b.started.Load() }
func (b *ledgerBackend) Version() string { return "ledger" }

func (b *ledgerBackend) Shutdown() {
	b.ledger.lifecycleChangeReachedABackend()
	time.Sleep(50 * time.Microsecond)
	b.started.Store(false)
}

func (b *ledgerBackend) apply(users []*common.User) error {
	if !b.started.Load() {
		return errors.New("core is shut down")
	}
	b.mu.Lock()
	for _, u := range users {
		b.users[u.GetEmail()] = userVersion(u)
	}
	b.mu.Unlock()
	b.ledger.deliver(users)
	return nil
}

func (b *ledgerBackend) SyncUser(_ context.Context, u *common.User) error {
	return b.apply([]*common.User{u})
}

func (b *ledgerBackend) QueueUser(_ context.Context, u *common.User) error {
	return b.apply([]*common.User{u})
}

func (b *ledgerBackend) SyncUsers(_ context.Context, users []*common.User) error {
	return b.apply(users)
}

func (b *ledgerBackend) UpdateUsers(_ context.Context, users []*common.User) error {
	return b.apply(users)
}

func (b *ledgerBackend) GetStats(context.Context, *common.StatRequest) (*common.StatResponse, error) {
	return &common.StatResponse{}, nil
}

func (b *ledgerBackend) version(email string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.users[email]
}

func ledgerController(t *testing.T, ledger *panelLedger) *Controller {
	t.Helper()
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	t.Cleanup(c.Close)
	c.newBackend = func(context.Context, *common.Backend, int, int) (backend.Backend, error) {
		snapshot := ledger.startSnapshot()
		time.Sleep(100 * time.Microsecond)
		return newLedgerBackend(ledger, snapshot), nil
	}
	return c
}

var shareableTypes = []common.BackendType{
	common.BackendType_WIREGUARD,
	common.BackendType_MTPROTO,
	common.BackendType_L2TP,
}

func panelStartsTheNode(c *Controller, ledger *panelLedger) error {
	c.LockControl()
	defer c.UnlockControl()
	ledger.beginLifecycleOp()
	defer ledger.endLifecycleOp()
	if err := c.ReplaceBackend(context.Background(), &common.Backend{Type: common.BackendType_XRAY}); err != nil {
		return err
	}
	c.Connect(panelIP, 0)
	return nil
}

func panelStopsTheNode(c *Controller) {
	c.LockControl()
	defer c.UnlockControl()
	c.Disconnect()
}

func panelAddsABackend(c *Controller, ledger *panelLedger, backendType common.BackendType) {
	c.LockControl()
	defer c.UnlockControl()
	ledger.beginLifecycleOp()
	defer ledger.endLifecycleOp()
	_ = c.AttachBackend(context.Background(), &common.Backend{Type: backendType})
}

func panelRemovesABackend(c *Controller, backendType common.BackendType) {
	c.LockControl()
	defer c.UnlockControl()
	_ = c.DetachBackend(backendType)
}

func keepAliveWatchdogFires(c *Controller) {
	silentFor(c, time.Minute)
	c.disconnectIfStale(context.Background(), 0)
}

type syncWriter struct {
	id     int
	users  []string
	ledger *panelLedger
	c      *Controller
	rng    *rand.Rand
	errs   *errorLog
}

type errorLog struct {
	mu   sync.Mutex
	errs []error
}

func (l *errorLog) add(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.errs) < 20 {
		l.errs = append(l.errs, err)
	}
}

func (w *syncWriter) settle(kind string, users []*common.User, err error) {
	if err == nil {
		if missing := w.ledger.undelivered(users); len(missing) > 0 {
			w.errs.add(fmt.Errorf("%s from writer %d was acknowledged although no core received %v", kind, w.id, missing))
		}
		w.ledger.ack(users)
		return
	}
	if !errors.Is(err, ErrBackendNotInitialized) {
		w.errs.add(fmt.Errorf("%s from writer %d failed with something other than backend-not-initialized: %w", kind, w.id, err))
	}
}

func (w *syncWriter) step(ctx context.Context) {
	switch w.rng.IntN(4) {
	case 0:
		u := w.ledger.write(w.users[w.rng.IntN(len(w.users))])
		w.settle("SyncUser stream message", []*common.User{u}, w.c.QueueUserAll(ctx, u))
	case 1:
		u := w.ledger.write(w.users[w.rng.IntN(len(w.users))])
		w.settle("REST SyncUser", []*common.User{u}, w.c.SyncUserAll(ctx, u))
	case 2:
		batch := make([]*common.User, 0, len(w.users))
		for _, email := range w.users {
			batch = append(batch, w.ledger.write(email))
		}
		w.settle("SyncUsers", batch, w.c.SyncUsersAll(ctx, batch))
	default:
		batch := make([]*common.User, 0, len(w.users))
		for _, email := range w.users {
			batch = append(batch, w.ledger.write(email))
		}
		w.settle("SyncUsersChunked", batch, w.c.UpdateUsersAll(ctx, batch))
	}
}

func runLifecycleChaos(t *testing.T, seed uint64) {
	ledger := newPanelLedger()
	c := ledgerController(t, ledger)
	errs := &errorLog{}

	const writers = 6
	stop := make(chan struct{})
	var writersDone sync.WaitGroup
	for i := 0; i < writers; i++ {
		w := &syncWriter{
			id:     i,
			users:  []string{fmt.Sprintf("w%d-a", i), fmt.Sprintf("w%d-b", i), fmt.Sprintf("w%d-c", i)},
			ledger: ledger,
			c:      c,
			rng:    rand.New(rand.NewPCG(seed, uint64(i))),
			errs:   errs,
		}
		writersDone.Add(1)
		go func() {
			defer writersDone.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				w.step(context.Background())
			}
		}()
	}

	var control sync.WaitGroup
	control.Add(3)
	go func() {
		defer control.Done()
		rng := rand.New(rand.NewPCG(seed, 100))
		for i := 0; i < 6; i++ {
			if rng.IntN(3) == 0 {
				panelStopsTheNode(c)
				continue
			}
			if err := panelStartsTheNode(c, ledger); err != nil {
				errs.add(fmt.Errorf("Start: %w", err))
			}
		}
	}()
	go func() {
		defer control.Done()
		rng := rand.New(rand.NewPCG(seed, 200))
		for i := 0; i < 8; i++ {
			backendType := shareableTypes[rng.IntN(len(shareableTypes))]
			if rng.IntN(2) == 0 {
				panelAddsABackend(c, ledger, backendType)
			} else {
				panelRemovesABackend(c, backendType)
			}
		}
	}()
	go func() {
		defer control.Done()
		for i := 0; i < 2; i++ {
			keepAliveWatchdogFires(c)
		}
	}()
	control.Wait()

	if err := panelStartsTheNode(c, ledger); err != nil {
		errs.add(fmt.Errorf("final Start: %w", err))
	}
	panelAddsABackend(c, ledger, common.BackendType_MTPROTO)
	panelAddsABackend(c, ledger, common.BackendType_L2TP)
	close(stop)
	writersDone.Wait()

	for _, err := range errs.errs {
		t.Error(err)
	}

	running := c.AllBackends()
	if len(running) == 0 {
		t.Fatal("the node ended the run with no backend although the last lifecycle change was a Start")
	}
	ledger.mu.Lock()
	acked := maps.Clone(ledger.acked)
	written := maps.Clone(ledger.written)
	ledger.mu.Unlock()
	lost := 0
	for i, b := range running {
		lb := b.(*ledgerBackend)
		for email, version := range acked {
			got := lb.version(email)
			if got < version {
				lost++
				if lost <= 10 {
					t.Errorf("seed %d, running backend %d: user %s was acknowledged at version %d but the core running now holds version %d", seed, i, email, version, got)
				}
			}
			if got > written[email] {
				t.Errorf("seed %d, running backend %d: user %s holds version %d that the panel never wrote (newest %d)", seed, i, email, got, written[email])
			}
		}
	}
	if lost > 0 {
		t.Fatalf("seed %d: %d acknowledged user updates are missing or stale on the backends running when the dust settled", seed, lost)
	}
}

func TestUserSyncsRacingEveryLifecycleChangeAreNeverAcknowledgedAndLost(t *testing.T) {
	iterations := 150
	if testing.Short() {
		iterations = 20
	}
	for i := 0; i < iterations; i++ {
		runLifecycleChaos(t, uint64(i)+1)
		if t.Failed() {
			return
		}
	}
}

type gatedLedgerBackend struct {
	*ledgerBackend
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (g *gatedLedgerBackend) Shutdown() {
	g.ledger.lifecycleChangeReachedABackend()
	g.once.Do(func() { close(g.entered) })
	<-g.gate
	g.ledgerBackend.Shutdown()
}

func TestAUserSyncSentWhileStartTearsTheOldCoreDownWaitsAndLandsOnTheNewCore(t *testing.T) {
	ledger := newPanelLedger()
	c := ledgerController(t, ledger)
	old := &gatedLedgerBackend{ledgerBackend: newLedgerBackend(ledger, nil), gate: make(chan struct{}), entered: make(chan struct{})}
	c.mu.Lock()
	c.backend = old
	c.mu.Unlock()

	startDone := make(chan error, 1)
	go func() { startDone <- panelStartsTheNode(c, ledger) }()
	select {
	case <-old.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start never began tearing the old core down")
	}

	late := ledger.write("changed-while-start-runs")
	syncDone := make(chan error, 1)
	go func() { syncDone <- c.QueueUserAll(context.Background(), late) }()

	select {
	case err := <-syncDone:
		t.Fatalf("a user sync returned (%v) while Start still held the old core; it must wait for the new core", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(old.gate)
	if err := <-startDone; err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("the sync that waited behind Start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sync that waited behind Start never completed")
	}

	fresh, ok := c.Backend().(*ledgerBackend)
	if !ok || fresh == old.ledgerBackend {
		t.Fatal("Start did not leave a new core running")
	}
	if fresh.version(late.GetEmail()) != userVersion(late) {
		t.Fatalf("the acknowledged sync is not on the core Start left running (have version %d, want %d)", fresh.version(late.GetEmail()), userVersion(late))
	}
	if old.version(late.GetEmail()) != 0 {
		t.Fatal("the sync reached the core that was being torn down")
	}
}

func TestAUserSyncSentWhileAddBackendBuildsTheExtraReachesTheExtraToo(t *testing.T) {
	ledger := newPanelLedger()
	c := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	t.Cleanup(c.Close)
	primary := newLedgerBackend(ledger, nil)
	c.mu.Lock()
	c.backend = primary
	c.mu.Unlock()

	building := make(chan struct{})
	release := make(chan struct{})
	c.newBackend = func(context.Context, *common.Backend, int, int) (backend.Backend, error) {
		snapshot := ledger.startSnapshot()
		close(building)
		<-release
		return newLedgerBackend(ledger, snapshot), nil
	}

	addDone := make(chan struct{})
	go func() {
		defer close(addDone)
		panelAddsABackend(c, ledger, common.BackendType_MTPROTO)
	}()
	select {
	case <-building:
	case <-time.After(5 * time.Second):
		t.Fatal("AddBackend never started building the extra")
	}

	late := ledger.write("changed-while-the-extra-builds")
	syncDone := make(chan error, 1)
	go func() { syncDone <- c.SyncUserAll(context.Background(), late) }()
	select {
	case err := <-syncDone:
		t.Fatalf("a user sync returned (%v) while AddBackend was still building the extra; it must wait so the extra receives it", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	<-addDone
	if err := <-syncDone; err != nil {
		t.Fatalf("sync: %v", err)
	}
	extras := c.AllBackends()
	if len(extras) != 2 {
		t.Fatalf("AddBackend did not attach the extra (backends: %d)", len(extras))
	}
	for i, b := range extras {
		if got := b.(*ledgerBackend).version(late.GetEmail()); got != userVersion(late) {
			t.Fatalf("backend %d holds version %d of the acknowledged user, want %d", i, got, userVersion(late))
		}
	}
}

type wedgedSyncBackend struct {
	backend.Backend
	entered chan struct{}
	once    sync.Once
}

func (w *wedgedSyncBackend) Started() bool   { return true }
func (w *wedgedSyncBackend) Version() string { return "wedged" }
func (w *wedgedSyncBackend) Shutdown()       {}

func (w *wedgedSyncBackend) SyncUser(ctx context.Context, _ *common.User) error {
	w.once.Do(func() { close(w.entered) })
	<-ctx.Done()
	return ctx.Err()
}

func TestAUserSyncWedgedInsideACoreDoesNotHoldStartHostage(t *testing.T) {
	ledger := newPanelLedger()
	c := ledgerController(t, ledger)
	c.syncDrainGrace = 100 * time.Millisecond
	wedged := &wedgedSyncBackend{entered: make(chan struct{})}
	c.mu.Lock()
	c.backend = wedged
	c.mu.Unlock()

	syncDone := make(chan error, 1)
	go func() { syncDone <- c.SyncUserAll(context.Background(), &common.User{Email: "stuck"}) }()
	<-wedged.entered

	startDone := make(chan error, 1)
	go func() { startDone <- panelStartsTheNode(c, ledger) }()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start is still waiting behind a user sync that is wedged inside the core it is meant to replace")
	}

	select {
	case err := <-syncDone:
		if err == nil {
			t.Fatal("the wedged user sync that Start cut short reported success; it was never applied and must not be acknowledged")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wedged user sync never returned")
	}
}

func TestCloseRefusesALifecycleChangeThatWasWaitingBehindIt(t *testing.T) {
	ledger := newPanelLedger()
	c := ledgerController(t, ledger)
	built := atomic.Int32{}
	c.newBackend = func(context.Context, *common.Backend, int, int) (backend.Backend, error) {
		built.Add(1)
		return newLedgerBackend(ledger, nil), nil
	}

	c.Close()

	if err := c.ReplaceBackend(context.Background(), &common.Backend{Type: common.BackendType_XRAY}); err == nil {
		t.Fatal("a Start that reached the node after it began shutting down built a core that nothing will ever stop")
	}
	if err := c.StartBackend(context.Background(), &common.Backend{Type: common.BackendType_XRAY}); err == nil {
		t.Fatal("StartBackend after Close must be refused")
	}
	if n := built.Load(); n != 0 || c.Backend() != nil {
		t.Fatalf("a core was built after Close (builds=%d, primary installed=%v)", n, c.Backend() != nil)
	}
}

type sysStatsBackend struct {
	backend.Backend
	started bool
}

func (s *sysStatsBackend) Started() bool { return s.started }

func (s *sysStatsBackend) GetSysStats(context.Context) (*common.BackendStatsResponse, error) {
	return &common.BackendStatsResponse{Uptime: 42}, nil
}

func TestBackendStatsReportTheCoreNotStartedWhenThePrimaryIsDownEvenWithHealthyExtras(t *testing.T) {
	c := controllerWithExtras(&sysStatsBackend{started: false}, common.BackendType_OPEN_VPN,
		extraBackend{backendType: common.BackendType_L2TP, backend: &sysStatsBackend{started: true}})
	resp, err := c.PrimarySysStats(context.Background())
	if st, ok := status.FromError(err); err == nil || !ok || st.Code() != codes.Unavailable || st.Message() != "core is not started yet" {
		t.Fatalf("GetBackendStats for a dead primary with a healthy extra returned (%v, %v); the panel's node checker needs codes.Unavailable \"core is not started yet\"", resp, err)
	}

	none := &Controller{}
	if _, err := none.PrimarySysStats(context.Background()); status.Code(err) != codes.Unavailable {
		t.Fatalf("GetBackendStats with no primary returned %v, want codes.Unavailable", err)
	}

	healthy := controllerWithExtras(&sysStatsBackend{started: true}, common.BackendType_OPEN_VPN)
	resp, err = healthy.PrimarySysStats(context.Background())
	if err != nil || resp.GetUptime() != 42 {
		t.Fatalf("GetBackendStats for a running primary returned (%v, %v), want its own stats", resp, err)
	}
}
