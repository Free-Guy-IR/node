//go:build linux

package singbox

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

type syncRound struct {
	name  string
	apply func(context.Context, *SingBox) error
	want  func(map[string]*common.User)
}

func randomSyncRound(rng *rand.Rand, pool []*common.User, committed map[string]*common.User) syncRound {
	switch rng.IntN(4) {
	case 0:
		var subset []*common.User
		for _, user := range pool {
			if rng.IntN(2) == 0 {
				subset = append(subset, user)
			}
		}
		if len(subset) == 0 {
			subset = append(subset, pool[rng.IntN(len(pool))])
		}
		return syncRound{
			name:  fmt.Sprintf("full sync of %d users", len(subset)),
			apply: func(ctx context.Context, sb *SingBox) error { return sb.SyncUsers(ctx, subset) },
			want: func(state map[string]*common.User) {
				clear(state)
				for _, user := range subset {
					state[user.GetEmail()] = user
				}
			},
		}
	case 1:
		user := pool[rng.IntN(len(pool))]
		return syncRound{
			name:  "single sync adding " + user.GetEmail(),
			apply: func(ctx context.Context, sb *SingBox) error { return sb.SyncUser(ctx, user) },
			want:  func(state map[string]*common.User) { state[user.GetEmail()] = user },
		}
	case 2:
		var present []string
		for name := range committed {
			present = append(present, name)
		}
		name := pool[rng.IntN(len(pool))].GetEmail()
		if len(present) > 0 {
			name = present[rng.IntN(len(present))]
		}
		revoked := &common.User{Email: name}
		return syncRound{
			name:  "update revoking " + name,
			apply: func(ctx context.Context, sb *SingBox) error { return sb.UpdateUsers(ctx, []*common.User{revoked}) },
			want:  func(state map[string]*common.User) { delete(state, name) },
		}
	default:
		first := pool[rng.IntN(len(pool))]
		second := pool[rng.IntN(len(pool))]
		return syncRound{
			name: "queued " + first.GetEmail() + " then single sync " + second.GetEmail(),
			apply: func(ctx context.Context, sb *SingBox) error {
				if err := sb.QueueUser(ctx, first); err != nil {
					return err
				}
				return sb.SyncUser(ctx, second)
			},
			want: func(state map[string]*common.User) {
				state[first.GetEmail()] = first
				state[second.GetEmail()] = second
			},
		}
	}
}

func usersOf(state map[string]*common.User) []*common.User {
	users := make([]*common.User, 0, len(state))
	for _, user := range state {
		users = append(users, user)
	}
	return users
}

func fireHealthRestarts(rng *rand.Rand, sb *SingBox, count int, within time.Duration) <-chan error {
	delays := make([]time.Duration, count)
	for i := range delays {
		delays[i] = time.Duration(rng.Int64N(int64(within)))
	}
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for _, delay := range delays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(delay)
			errs <- sb.Restart()
		}()
	}
	go func() {
		wg.Wait()
		close(errs)
	}()
	return errs
}

func TestHealthRestartsRacingUserSyncsKeepTheCoreOnTheCommittedUsers(t *testing.T) {
	for _, hotReload := range []bool{false, true} {
		name := "restart fallback"
		if hotReload {
			name = "hot reload"
		}
		t.Run(name, func(t *testing.T) { healthRestartsRacingSyncs(t, hotReload) })
	}
}

func healthRestartsRacingSyncs(t *testing.T, hotReload bool) {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	pool := make([]*common.User, 0, 6)
	for i := range 6 {
		pool = append(pool, restartModelUser(fmt.Sprintf("%d.user", i)))
	}
	exe := restartModelExecutable(t)
	dir := t.TempDir()
	m := newRestartModel(t, exe, dir, pool[0])
	clash := m.enableHotReload(t)
	clash.fail.Store(!hotReload)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	committed := map[string]*common.User{pool[0].GetEmail(): pool[0]}
	for round := range 4 {
		step := randomSyncRound(rng, pool, committed)
		restarts := fireHealthRestarts(rng, m.sb, 3, 3500*time.Millisecond)
		if err := step.apply(ctx, m.sb); err != nil {
			t.Fatalf("round %d (%s) failed: %v", round, step.name, err)
		}
		step.want(committed)
		for err := range restarts {
			if err != nil {
				t.Fatalf("round %d (%s): a health restart failed: %v", round, step.name, err)
			}
		}

		want := expectUsers(usersOf(committed)...)
		if got := m.committed(); !sameUserNames(got, want) {
			t.Fatalf("round %d (%s): the backend committed %v, want %v", round, step.name, got, want)
		}
		if got := m.live(t); !sameUserNames(got, want) {
			t.Fatalf("round %d (%s): the running core serves %v while the backend committed %v", round, step.name, got, want)
		}
		if !m.sb.core.Started() {
			t.Fatalf("round %d (%s): the core is not running", round, step.name)
		}
	}

	final := randomSyncRound(rng, pool, committed)
	finalDone := make(chan error, 1)
	go func() { finalDone <- final.apply(ctx, m.sb) }()
	restarts := fireHealthRestarts(rng, m.sb, 3, 3500*time.Millisecond)
	time.Sleep(time.Duration(rng.Int64N(int64(3 * time.Second))))
	m.sb.Shutdown()

	carol := restartModelUser("9.carol")
	replacement := newRestartModel(t, exe, dir, carol)
	pid := corePID(replacement.sb.core)

	select {
	case <-finalDone:
	case <-ctx.Done():
		t.Fatal("the sync racing shutdown never returned")
	}
	for range restarts {
	}

	if err := m.sb.Restart(); err == nil {
		t.Error("a restart after shutdown must be refused")
	}
	if m.sb.core.Started() {
		t.Error("the shut-down backend's core is running again")
	}
	if got, want := replacement.launched(t), expectUsers(carol); !sameUserNames(got, want) {
		t.Errorf("the shut-down backend rewrote the replacement core's config to %v, want %v", got, want)
	}
	if !replacement.sb.core.Started() || corePID(replacement.sb.core) != pid {
		t.Error("the replacement core was restarted or stopped by the shut-down backend")
	}
}
