package xray

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
)

const interleaveIterations = 150

var interleaveNamespace = uuid.MustParse("5f0c8a2e-1d4b-4e7a-9c3f-2b6d8e0a4c17")

func versionedUser(email, version string, inbounds ...string) *common.User {
	seed := email + "/" + version
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Vmess:       &common.Vmess{Id: uuid.NewSHA1(interleaveNamespace, []byte("vmess/"+seed)).String()},
			Vless:       &common.Vless{Id: uuid.NewSHA1(interleaveNamespace, []byte("vless/"+seed)).String()},
			Trojan:      &common.Trojan{Password: "trojan/" + seed},
			Shadowsocks: &common.Shadowsocks{Password: "ss/" + seed, Method: "aes-128-gcm"},
			Hysteria:    &common.Hysteria{Auth: "hysteria/" + seed},
		},
	}
}

type syncCall struct {
	id    string
	bulk  bool
	users []*common.User
}

func (c syncCall) run(ctx context.Context, x *Xray) error {
	ctx = withCall(ctx, c.id)
	if c.bulk {
		return x.UpdateUsers(ctx, c.users)
	}
	return x.SyncUser(ctx, c.users[0])
}

type interleaveUsers struct {
	bystanders []*common.User
	target     string
	revoked    string
	other      string
	single     string
}

func newInterleaveUsers() interleaveUsers {
	users := interleaveUsers{target: "10.target", revoked: "11.revoked", other: "12.other", single: "13.single"}
	for i := range 4 {
		users.bystanders = append(users.bystanders, versionedUser(fmt.Sprintf("%d.bystander", i+1), "stable", syncTestTags...))
	}
	return users
}

func (u interleaveUsers) setup() syncCall {
	users := slices.Clone(u.bystanders)
	users = append(users,
		versionedUser(u.target, "setup", "vless", "trojan"),
		versionedUser(u.revoked, "setup", "vless", "ss"),
		versionedUser(u.other, "setup", "trojan"),
		versionedUser(u.single, "setup", "hysteria"),
	)
	return syncCall{id: "setup", bulk: true, users: users}
}

func (u interleaveUsers) round(i int) []syncCall {
	bulk := slices.Clone(u.bystanders)
	bulk = append(bulk,
		versionedUser(u.target, fmt.Sprintf("bulk-%d", i), "vless", "trojan"),
		versionedUser(u.revoked, fmt.Sprintf("bulk-%d", i), "vless", "ss"),
		versionedUser(u.other, fmt.Sprintf("bulk-%d", i), "trojan"),
	)

	calls := []syncCall{
		{id: fmt.Sprintf("%d/bulk", i), bulk: true, users: bulk},
		{id: fmt.Sprintf("%d/sync-target", i), users: []*common.User{versionedUser(u.target, fmt.Sprintf("sync-%d", i), "vless", "trojan", "vmess")}},
		{id: fmt.Sprintf("%d/resend-target", i), users: []*common.User{versionedUser(u.target, fmt.Sprintf("bulk-%d", i), "vless", "trojan")}},
		{id: fmt.Sprintf("%d/revoke-other", i), users: []*common.User{versionedUser(u.revoked, "disabled")}},
		{id: fmt.Sprintf("%d/sync-single", i), users: []*common.User{versionedUser(u.single, fmt.Sprintf("single-%d", i), "hysteria", "ss2022")}},
		{id: fmt.Sprintf("%d/resend-bystander", i), users: []*common.User{u.bystanders[i%len(u.bystanders)]}},
		{id: fmt.Sprintf("%d/bulk-bystanders", i), bulk: true, users: slices.Clone(u.bystanders)},
	}
	if i%2 == 0 {
		calls = append(calls, syncCall{id: fmt.Sprintf("%d/revoke-target", i), users: []*common.User{versionedUser(u.target, "disabled")}})
	}
	return calls
}

func expectedFingerprint(x *Xray, user *common.User, tag string) (string, bool) {
	settings, _ := setupUserAccount(user)
	for _, inbound := range x.config.InboundConfigs {
		if inbound.Tag != tag {
			continue
		}
		account, active := isActiveInbound(inbound, user.GetInbounds(), settings)
		if !active {
			return "", false
		}
		return localFingerprint(inbound, account), true
	}
	return "", false
}

type userKey struct {
	tag   string
	email string
}

type interleaveModel struct {
	x       *Xray
	stock   bool
	state   map[userKey]string
	touched map[string]bool
}

func (m *interleaveModel) apply(t *testing.T, call syncCall, entries []coreEntry) {
	t.Helper()

	byKey := make(map[userKey][]coreEntry)
	for _, entry := range entries {
		key := userKey{tag: entry.tag, email: entry.email}
		byKey[key] = append(byKey[key], entry)
	}

	last := make(map[string]*common.User)
	order := make([]string, 0, len(call.users))
	for _, user := range call.users {
		if _, seen := last[user.GetEmail()]; !seen {
			order = append(order, user.GetEmail())
		}
		last[user.GetEmail()] = user
	}

	for _, email := range order {
		user := last[email]
		for _, tag := range syncTestTags {
			key := userKey{tag: tag, email: email}
			got := byKey[key]
			delete(byKey, key)
			current, held := m.state[key]
			want, active := expectedFingerprint(m.x, user, tag)

			switch {
			case active && held && current == want:
				if len(got) != 0 {
					t.Fatalf("call %s touched unchanged user %s on %s: %v", call.id, email, tag, got)
				}
			case active:
				if len(got) != 2 || got[0].op != removeOperation || got[1].op != addOperation || got[1].failed || got[1].account != want {
					t.Fatalf("call %s did not replace changed user %s on %s with its own account: %v", call.id, email, tag, got)
				}
				m.state[key] = want
			case m.stock:
				if len(got) == 0 || len(got) > 2 || got[len(got)-1].op != removeOperation || (len(got) == 2 && got[0].op != revokeOperation) {
					t.Fatalf("call %s did not remove user %s from %s on a stock core: %v", call.id, email, tag, got)
				}
				delete(m.state, key)
			default:
				if len(got) != 1 || got[0].op != revokeOperation || got[0].failed {
					t.Fatalf("call %s did not revoke user %s on %s: %v", call.id, email, tag, got)
				}
				delete(m.state, key)
			}
		}
	}

	if len(byKey) != 0 {
		t.Fatalf("call %s sent operations for users it did not carry: %v", call.id, byKey)
	}
}

func serializedCalls(t *testing.T, entries []coreEntry) ([]string, map[string][]coreEntry) {
	t.Helper()

	order := make([]string, 0)
	byCall := make(map[string][]coreEntry)
	for i, entry := range entries {
		if entry.call == "" {
			t.Fatalf("operation %v carried no call id", entry)
		}
		if _, seen := byCall[entry.call]; !seen {
			order = append(order, entry.call)
		} else if entries[i-1].call != entry.call {
			t.Fatalf("operations of call %s interleaved with call %s", entry.call, entries[i-1].call)
		}
		byCall[entry.call] = append(byCall[entry.call], entry)
	}
	return order, byCall
}

func requireCoreMatchesModel(t *testing.T, core *modelCore, model *interleaveModel, round int) {
	t.Helper()

	remote := make(map[userKey]string)
	for tag, users := range core.state() {
		for email, account := range users {
			remote[userKey{tag: tag, email: email}] = account
		}
	}
	if !maps.Equal(remote, model.state) {
		t.Fatalf("round %d: the core does not hold the newest state:\n core  %v\n model %v", round, remote, model.state)
	}
}

func TestSyncCallsRacingABulkUpdateKeepTheNewestStateAndLeaveUnchangedUsersAlone(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			raceSyncCalls(t, stock)
		})
	}
}

func raceSyncCalls(t *testing.T, stock bool) {
	core := newModelCore(stock)
	x := newModelXray(t, core, syncTestInbounds)
	users := newInterleaveUsers()
	model := &interleaveModel{x: x, stock: stock, state: make(map[userKey]string)}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	setup := users.setup()
	if err := setup.run(ctx, x); err != nil {
		t.Fatal(err)
	}
	for _, user := range setup.users {
		for _, tag := range syncTestTags {
			if want, active := expectedFingerprint(x, user, tag); active {
				model.state[userKey{tag: tag, email: user.GetEmail()}] = want
			}
		}
	}
	requireCoreMatchesModel(t, core, model, -1)
	requireCoreMatchesLocal(t, x, core)
	seen := len(core.allEntries())

	bystanders := make(map[string]bool)
	for _, user := range users.bystanders {
		bystanders[user.GetEmail()] = true
	}

	for round := range interleaveIterations {
		calls := users.round(round)
		callByID := make(map[string]syncCall, len(calls))
		for _, call := range calls {
			callByID[call.id] = call
		}

		start := make(chan struct{})
		errs := make([]error, len(calls))
		var wg sync.WaitGroup
		for i, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = call.run(ctx, x)
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: call %s failed: %v", round, calls[i].id, err)
			}
		}

		entries := core.allEntries()[seen:]
		seen += len(entries)
		for _, entry := range entries {
			if bystanders[entry.email] {
				t.Fatalf("round %d: call %s sent %s for unchanged bystander %s on %s", round, entry.call, entry.op, entry.email, entry.tag)
			}
		}

		order, byCall := serializedCalls(t, entries)
		for _, id := range order {
			call, ok := callByID[id]
			if !ok || !strings.HasPrefix(id, fmt.Sprintf("%d/", round)) {
				t.Fatalf("round %d: operations from unknown or stale call %s", round, id)
			}
			model.apply(t, call, byCall[id])
		}
		for _, call := range calls {
			if _, ran := byCall[call.id]; ran {
				continue
			}
			model.apply(t, call, nil)
		}

		requireCoreMatchesModel(t, core, model, round)
		requireCoreMatchesLocal(t, x, core)
	}
}
