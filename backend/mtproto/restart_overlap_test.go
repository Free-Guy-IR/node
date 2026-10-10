package mtproto

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const overlapKey = "00112233445566778899aabbccddeeff"

func overlapPool(count int, inbounds ...string) []*common.User {
	users := make([]*common.User, 0, count)
	for i := range count {
		users = append(users, credentialedUser(fmt.Sprintf("o%d@x", i), overlapKey, inbounds...))
	}
	return users
}

func TestARestartOverlappingAFullSyncKeepsEverySyncedUser(t *testing.T) {
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"mode":"plain"}]}`, port)
	pool := overlapPool(6, "mtproto-tg")
	backend := startBackend(t, raw, pool[:1])
	t.Cleanup(backend.Shutdown)

	ctx := context.Background()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed>>9))

	for round := range 200 {
		subset := pool[:1+rng.IntN(len(pool))]
		restarted := make(chan error, 1)
		go func() { restarted <- backend.Restart() }()
		time.Sleep(time.Duration(rng.IntN(1500)) * time.Microsecond)
		if err := backend.SyncUsers(ctx, subset); err != nil {
			t.Fatal(err)
		}
		if err := <-restarted; err != nil {
			t.Fatalf("round %d: the restart failed: %v", round, err)
		}

		want := make([]string, 0, len(subset))
		for _, user := range subset {
			want = append(want, user.GetEmail())
		}
		slices.Sort(want)
		if got := sortedIDs(authorisedEntries(backend)); !slices.Equal(got, want) {
			t.Fatalf("round %d: after a restart overlapped a full sync the authorised users are %v, want %v", round, got, want)
		}
	}
}
