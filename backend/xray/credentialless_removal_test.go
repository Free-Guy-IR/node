package xray

import (
	"context"
	"fmt"
	"testing"

	"github.com/pasarguard/node/common"
)

func credentialLessRemovals(email string) map[string]*common.User {
	return map[string]*common.User{
		"no proxies, no inbounds":         {Email: email},
		"empty proxies, no inbounds":      {Email: email, Proxies: &common.Proxy{}},
		"no proxies, inbounds still sent": {Email: email, Inbounds: syncTestTags},
	}
}

func TestACredentialLessRemovalRevokesTheUserFromEveryInbound(t *testing.T) {
	removals := map[string]func(context.Context, *Xray, *common.User) error{
		"SyncUser": func(ctx context.Context, x *Xray, user *common.User) error {
			return x.SyncUser(ctx, user)
		},
		"UpdateUsers": func(ctx context.Context, x *Xray, user *common.User) error {
			return x.UpdateUsers(ctx, []*common.User{user})
		},
	}
	for path, remove := range removals {
		for payload, removal := range credentialLessRemovals("1.alice") {
			t.Run(fmt.Sprintf("%s/%s", path, payload), func(t *testing.T) {
				core := newModelCore(false)
				x := newModelXray(t, core, syncTestInbounds)
				ctx := syncTestContext(t)
				if err := x.UpdateUsers(ctx, []*common.User{syncTestUser("1.alice", syncTestTags...), syncTestUser("2.bob", syncTestTags...)}); err != nil {
					t.Fatal(err)
				}
				for _, tag := range syncTestTags {
					if _, held := core.state()[tag]["1.alice"]; !held {
						t.Fatalf("setup: the core does not hold 1.alice on %s", tag)
					}
				}

				if err := remove(ctx, x, removal); err != nil {
					t.Fatal(err)
				}
				state := core.state()
				for _, tag := range syncTestTags {
					if _, held := state[tag]["1.alice"]; held {
						t.Fatalf("a credential-less removal left 1.alice on %s", tag)
					}
					if _, held := state[tag]["2.bob"]; !held {
						t.Fatalf("the removal of 1.alice took 2.bob off %s", tag)
					}
				}
				requireCoreMatchesLocal(t, x, core)
			})
		}
	}
}

func TestACredentialLessRemovalLeavesTheUserOutOfARebuiltConfig(t *testing.T) {
	builds := map[string]func(*Config, []*common.User){
		"updateUsers": func(c *Config, users []*common.User) { c.updateUsers(users) },
		"syncUsers":   func(c *Config, users []*common.User) { c.syncUsers(users) },
	}
	for name, build := range builds {
		for payload, removal := range credentialLessRemovals("1.alice") {
			t.Run(fmt.Sprintf("%s/%s", name, payload), func(t *testing.T) {
				cfg, err := NewConfig(syncTestInbounds, nil)
				if err != nil {
					t.Fatal(err)
				}
				x := &Xray{config: cfg}
				cfg.updateUsers([]*common.User{syncTestUser("1.alice", syncTestTags...)})

				build(cfg, []*common.User{removal})
				for _, tag := range syncTestTags {
					if emails := clientEmails(x, tag); len(emails) != 0 {
						t.Fatalf("a credential-less removal left %v on %s", emails, tag)
					}
				}
			})
		}
	}
}
