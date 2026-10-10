package mtproto

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const rsweepSecret = "00112233445566778899aabbccddeeff"

func rsweepUsers(count int, inbounds ...string) []*common.User {
	users := make([]*common.User, 0, count)
	for i := range count {
		users = append(users, credentialedUser(fmt.Sprintf("u%d@x", i), rsweepSecret, inbounds...))
	}
	return users
}

func TestMTProtoRestartConcurrentWithShutdownAndSyncsIsRaceFree(t *testing.T) {
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"mode":"plain"}]}`, port)
	pool := rsweepUsers(6, "mtproto-tg")
	backend := startBackend(t, raw, pool[:1])

	ctx := context.Background()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed>>11))
	shutdownAfter := time.Duration(rng.Int64N(int64(30 * time.Millisecond)))

	stop := make(chan struct{})
	var workers sync.WaitGroup
	worker := func(step func(i int)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				step(i)
			}
		}()
	}
	worker(func(int) { _ = backend.Restart() })
	worker(func(i int) { _ = backend.SyncUser(ctx, pool[i%len(pool)]) })
	worker(func(int) { _ = backend.SyncUsers(ctx, pool) })
	worker(func(i int) { _ = backend.UpdateUsers(ctx, []*common.User{pool[i%len(pool)]}) })
	worker(func(int) { _, _ = backend.GetSysStats(ctx) })

	time.Sleep(shutdownAfter)
	backend.Shutdown()
	time.Sleep(15 * time.Millisecond)
	close(stop)
	workers.Wait()

	if err := backend.Restart(); err == nil {
		t.Fatal("a restart after shutdown must be refused")
	}
	if err := backend.SyncUsers(ctx, pool); err != nil {
		t.Fatalf("a sync after shutdown must not error, got %v", err)
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("the backend still accepts connections after shutdown and concurrent restarts revived it")
	}
}

func TestMTProtoRestartDoesNotRevertTheAuthorisedUserSet(t *testing.T) {
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"mode":"plain"}]}`, port)
	pool := rsweepUsers(6, "mtproto-tg")
	backend := startBackend(t, raw, pool[:1])
	t.Cleanup(backend.Shutdown)

	ctx := context.Background()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, seed>>5))

	stop := make(chan struct{})
	restarted := make(chan struct{})
	go func() {
		defer close(restarted)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := backend.Restart(); err != nil {
				t.Errorf("a health restart failed while the backend runs: %v", err)
				return
			}
		}
	}()

	for range 300 {
		switch rng.IntN(3) {
		case 0:
			var subset []*common.User
			for _, user := range pool {
				if rng.IntN(2) == 0 {
					subset = append(subset, user)
				}
			}
			if err := backend.SyncUsers(ctx, subset); err != nil {
				t.Fatal(err)
			}
		case 1:
			if err := backend.SyncUser(ctx, pool[rng.IntN(len(pool))]); err != nil {
				t.Fatal(err)
			}
		default:
			if err := backend.UpdateUsers(ctx, []*common.User{pool[rng.IntN(len(pool))]}); err != nil {
				t.Fatal(err)
			}
		}
	}
	close(stop)
	<-restarted

	final := pool[:3]
	if err := backend.SyncUsers(ctx, final); err != nil {
		t.Fatal(err)
	}
	if err := backend.Restart(); err != nil {
		t.Fatalf("the settling restart failed: %v", err)
	}

	want := make([]string, 0, len(final))
	for _, user := range final {
		want = append(want, user.GetEmail())
	}
	slices.Sort(want)
	got := sortedIDs(authorisedEntries(backend))
	if !slices.Equal(got, want) {
		t.Fatalf("after a restart the authorised set is %v, want the last committed sync %v: a restart reverted the user set", got, want)
	}
}
