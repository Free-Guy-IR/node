package xray

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
)

const (
	batchKeyX = "11111111-2222-4333-8444-555555555555"
	batchKeyY = "66666666-7777-4888-9999-aaaaaaaaaaaa"
)

func batchVless(email, id string) *common.User {
	return vlessCredentialUser(id)(email, "vless")
}

func requireNoRejection(t *testing.T, logs *syncBuffer) {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, " rejected: ") {
			t.Fatalf("a legitimate user was rejected: %s", line)
		}
	}
}

func removalsOf(operations []string, email string) int {
	count := 0
	for _, operation := range operations {
		if operation == "vless:"+removeOperation+":"+email || operation == "vless:"+revokeOperation+":"+email {
			count++
		}
	}
	return count
}

func TestABatchThatMovesAHolderOffAKeyGivesItToTheClaimant(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			core := newCredentialCore(stock, "vless", vlessKey(batchKeyX))
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			if err := x.UpdateUsers(ctx, []*common.User{batchVless("5", batchKeyX)}); err != nil {
				t.Fatal(err)
			}
			core.takeOperations()
			logs := captureNodeLog(t)

			if err := x.UpdateUsers(ctx, []*common.User{batchVless("3", batchKeyX), batchVless("5", batchKeyY)}); err != nil {
				t.Fatalf("the batch failed: %v", err)
			}
			operations := core.takeOperations()
			requireNoRejection(t, logs)
			if n := removalsOf(operations, "3"); n != 0 {
				t.Fatalf("user 3 was removed from the core %d times: %v", n, operations)
			}
			if got := core.holder("vless", vlessKey(batchKeyX)); got != "3" {
				t.Fatalf("key X is held by %q, want 3; operations %v", got, operations)
			}
			if got := core.holder("vless", vlessKey(batchKeyY)); got != "5" {
				t.Fatalf("key Y is held by %q, want 5; operations %v", got, operations)
			}
			requireNodeHolds(t, x, "vless", "3", "5")
			requireCoreHoldsExactlyTheNodeState(t, x, core)
		})
	}
}

func TestABatchThatSwapsTwoKeysRejectsNobody(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			core := newCredentialCore(stock, "vless", vlessKey(batchKeyX))
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			if err := x.UpdateUsers(ctx, []*common.User{batchVless("3", batchKeyX), batchVless("5", batchKeyY)}); err != nil {
				t.Fatal(err)
			}
			core.takeOperations()
			logs := captureNodeLog(t)

			if err := x.UpdateUsers(ctx, []*common.User{batchVless("3", batchKeyY), batchVless("5", batchKeyX)}); err != nil {
				t.Fatalf("the batch failed: %v", err)
			}
			operations := core.takeOperations()
			requireNoRejection(t, logs)
			for _, email := range []string{"3", "5"} {
				if n := removalsOf(operations, email); n > 1 {
					t.Fatalf("user %s was removed from the core %d times, more than its own rotation: %v", email, n, operations)
				}
			}
			if got := core.holder("vless", vlessKey(batchKeyX)); got != "5" {
				t.Fatalf("key X is held by %q, want 5; operations %v", got, operations)
			}
			if got := core.holder("vless", vlessKey(batchKeyY)); got != "3" {
				t.Fatalf("key Y is held by %q, want 3; operations %v", got, operations)
			}
			requireNodeHolds(t, x, "vless", "3", "5")
			requireCoreHoldsExactlyTheNodeState(t, x, core)
		})
	}
}

func TestTwoClaimantsOfOneReleasedKeyInOneBatchGiveItToTheOldest(t *testing.T) {
	core := newCredentialCore(false, "vless", vlessKey(batchKeyX))
	x := newCredentialXray(t, core)
	ctx := syncTestContext(t)
	if err := x.UpdateUsers(ctx, []*common.User{batchVless("5", batchKeyX)}); err != nil {
		t.Fatal(err)
	}
	logs := captureNodeLog(t)

	if err := x.UpdateUsers(ctx, []*common.User{batchVless("8", batchKeyX), batchVless("5", batchKeyY), batchVless("4", batchKeyX)}); err != nil {
		t.Fatalf("the batch failed: %v", err)
	}
	if got := core.holder("vless", vlessKey(batchKeyX)); got != "4" {
		t.Fatalf("key X is held by %q, want the oldest claimant 4", got)
	}
	requireRefusalLogged(t, logs, "8", "4")
	requireNodeHolds(t, x, "vless", "4", "5")
	requireCoreHoldsExactlyTheNodeState(t, x, core)
}

func requireOneHolderPerKey(t *testing.T, core *credentialCore, removed map[string]bool) {
	t.Helper()
	core.mu.Lock()
	defer core.mu.Unlock()
	seen := make(map[string]string)
	for email, key := range core.users["vless"] {
		if removed[email] {
			t.Fatalf("removed user %s still holds a key in the core", email)
		}
		if other, taken := seen[key]; taken {
			t.Fatalf("users %s and %s both hold key %s in the core", other, email, key)
		}
		seen[key] = email
		if holder := core.keys["vless"][key]; holder != email {
			t.Fatalf("user %s is in the core with key %s but the key routes to %q", email, key, holder)
		}
	}
}

func TestConcurrentBatchesOfRotationsSwapsAndRemovalsKeepOneHolderPerKey(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			core := newCredentialCore(stock, "vless", vlessKey(batchKeyX))
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			keys := make([]string, 6)
			for index := range keys {
				keys[index] = uuid.NewSHA1(interleaveNamespace, []byte("batch/"+strconv.Itoa(index))).String()
			}
			emails := []string{"1", "2", "3", "4", "5", "6"}

			batchOf := func(rng *rand.Rand) []*common.User {
				perm := rng.Perm(len(keys))
				drop := rng.IntN(len(emails) + 1)
				users := make([]*common.User, 0, len(emails))
				for index, email := range emails {
					if index == drop {
						users = append(users, &common.User{Email: email})
						continue
					}
					users = append(users, batchVless(email, keys[perm[index]]))
				}
				return users
			}

			for round := range 30 {
				rng := rand.New(rand.NewPCG(uint64(round), 7))
				var wg sync.WaitGroup
				for worker := range 4 {
					users := batchOf(rng)
					full := worker == 3 && round%3 == 0
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := applyBatchSync(ctx, x, users, full); err != nil {
							t.Errorf("round %d: %v", round, err)
						}
					}()
				}
				wg.Wait()
				requireOneHolderPerKey(t, core, nil)
				requireCoreHoldsExactlyTheNodeState(t, x, core)

				final := batchOf(rng)
				removed := make(map[string]bool)
				var want []string
				for _, user := range final {
					if user.GetProxies() == nil {
						removed[user.GetEmail()] = true
						continue
					}
					want = append(want, user.GetEmail())
				}
				logs := captureNodeLog(t)
				if err := x.UpdateUsers(ctx, final); err != nil {
					t.Fatalf("round %d: settling batch failed: %v", round, err)
				}
				requireNoRejection(t, logs)
				requireNodeHolds(t, x, "vless", want...)
				requireOneHolderPerKey(t, core, removed)
				requireCoreHoldsExactlyTheNodeState(t, x, core)
			}
		})
	}
}

func applyBatchSync(ctx context.Context, x *Xray, users []*common.User, full bool) error {
	if full {
		var active []*common.User
		for _, user := range users {
			if user.GetProxies() != nil {
				active = append(active, user)
			}
		}
		return x.SyncUsers(ctx, active)
	}
	return x.UpdateUsers(ctx, users)
}
