package xray

import (
	"testing"

	"github.com/pasarguard/node/common"
)

func TestUpdateUsersLetsTheLastEntryForAUserWin(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)

	first := syncTestUser("1.alice", "trojan")
	second := syncTestUser("1.alice", "trojan")
	second.Proxies.Trojan.Password = "second trojan secret"

	if err := x.UpdateUsers(ctx, []*common.User{second}); err != nil {
		t.Fatal(err)
	}
	core.take()
	want := core.state()["trojan"]["1.alice"]

	if err := x.UpdateUsers(ctx, []*common.User{first, second}); err != nil {
		t.Fatal(err)
	}
	core.take()
	requireCoreMatchesLocal(t, x, core)
	if got := core.state()["trojan"]["1.alice"]; got != want {
		t.Fatalf("an earlier entry for the user won over the last one: the core holds %q, want %q", got, want)
	}

	if err := x.UpdateUsers(ctx, []*common.User{second, syncTestUser("1.alice")}); err != nil {
		t.Fatal(err)
	}
	core.take()
	requireCoreMatchesLocal(t, x, core)
	if held := core.state()["trojan"]; len(held) != 0 {
		t.Fatalf("the user whose last entry removes them is still on trojan: %v", held)
	}

	if err := x.UpdateUsers(ctx, []*common.User{syncTestUser("1.alice"), second}); err != nil {
		t.Fatal(err)
	}
	core.take()
	requireCoreMatchesLocal(t, x, core)
	if got := core.state()["trojan"]["1.alice"]; got != want {
		t.Fatalf("the user whose last entry adds them holds %q on trojan, want %q", got, want)
	}
}

func TestUpdateUsersAndSyncUsersLetTheLastEntryForAUserWinInTheConfig(t *testing.T) {
	enabled := syncTestUser("1.alice", "trojan")
	disabled := syncTestUser("1.alice")

	for name, apply := range map[string]func(*Config, []*common.User){
		"updateUsers": (*Config).updateUsers,
		"syncUsers":   (*Config).syncUsers,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := NewConfig(syncTestInbounds, nil)
			if err != nil {
				t.Fatal(err)
			}
			x := &Xray{config: cfg}

			apply(cfg, []*common.User{enabled, disabled})
			if emails := clientEmails(x, "trojan"); len(emails) != 0 {
				t.Fatalf("a user whose last entry removes them is on trojan: %v", emails)
			}

			apply(cfg, []*common.User{disabled, enabled})
			if emails := clientEmails(x, "trojan"); len(emails) != 1 {
				t.Fatalf("a user whose last entry adds them is not on trojan: %v", emails)
			}
		})
	}
}
