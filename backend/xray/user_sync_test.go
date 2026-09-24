package xray

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/serial"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const syncTestInbounds = `{"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"vmess","protocol":"vmess","settings":{"clients":[]}},
	{"tag":"trojan","protocol":"trojan","settings":{"clients":[]}},
	{"tag":"ss","protocol":"shadowsocks","settings":{"clients":[],"method":"aes-128-gcm","network":"tcp,udp"}},
	{"tag":"ss2022","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","network":"tcp,udp"}},
	{"tag":"hysteria","protocol":"hysteria","settings":{"clients":[]}}
]}`

var syncTestTags = []string{"vless", "vmess", "trojan", "ss", "ss2022", "hysteria"}

const syncCallKey = "sync-call"

type coreEntry struct {
	call    string
	tag     string
	op      string
	email   string
	account string
	failed  bool
}

type modelCore struct {
	command.UnimplementedHandlerServiceServer
	stock      bool
	mu         sync.Mutex
	entries    []coreEntry
	taken      int
	users      map[string]map[string]string
	rejectAdds int
	rejectAdd  func(tag, email string) bool
}

func newModelCore(stock bool) *modelCore {
	return &modelCore{stock: stock, users: make(map[string]map[string]string)}
}

func accountFingerprint(account *serial.TypedMessage) string {
	return account.GetType() + ":" + hex.EncodeToString(account.GetValue())
}

func localFingerprint(inbound *Inbound, account api.Account) string {
	message, err := accountForAPI(inbound, account).Message()
	if err != nil {
		return "unencodable:" + err.Error()
	}
	return accountFingerprint(message)
}

func callFromContext(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get(syncCallKey); len(values) > 0 {
		return values[0]
	}
	return ""
}

func withCall(ctx context.Context, call string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, syncCallKey, call)
}

func (c *modelCore) AlterInbound(ctx context.Context, request *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := coreEntry{call: callFromContext(ctx), tag: request.GetTag(), op: operationName(request.GetOperation().GetType())}
	err := c.applyLocked(&entry, request.GetOperation())
	entry.failed = err != nil
	c.entries = append(c.entries, entry)
	if err != nil {
		return nil, err
	}
	return &command.AlterInboundResponse{}, nil
}

func (c *modelCore) applyLocked(entry *coreEntry, operation *serial.TypedMessage) error {
	users := c.users[entry.tag]
	if users == nil {
		users = make(map[string]string)
		c.users[entry.tag] = users
	}

	switch entry.op {
	case addOperation:
		var add command.AddUserOperation
		if err := proto.Unmarshal(operation.GetValue(), &add); err != nil {
			return err
		}
		entry.email = add.GetUser().GetEmail()
		entry.account = accountFingerprint(add.GetUser().GetAccount())
		if c.rejectAdds > 0 {
			c.rejectAdds--
			return errors.New("add rejected")
		}
		if c.rejectAdd != nil && c.rejectAdd(entry.tag, entry.email) {
			return errors.New("add rejected")
		}
		if _, found := users[entry.email]; found {
			return fmt.Errorf("proxy/vless: User %s already exists.", entry.email)
		}
		users[entry.email] = entry.account
		return nil

	case removeOperation, revokeOperation:
		var remove command.RemoveUserOperation
		if err := proto.Unmarshal(operation.GetValue(), &remove); err != nil {
			return err
		}
		entry.email = remove.GetEmail()
		if entry.op == revokeOperation {
			if c.stock {
				return status.Error(codes.Unknown, "app/proxyman/command: unknown operation > proto: not found")
			}
			delete(users, entry.email)
			return nil
		}
		if _, found := users[entry.email]; !found {
			return fmt.Errorf("proxy/vless: User %s not found.", entry.email)
		}
		delete(users, entry.email)
		return nil
	}
	return fmt.Errorf("unexpected operation %s", operation.GetType())
}

func (c *modelCore) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var operations []string
	for _, entry := range c.entries[c.taken:] {
		operations = append(operations, entry.tag+":"+entry.op)
	}
	c.taken = len(c.entries)
	slices.Sort(operations)
	return operations
}

func (c *modelCore) allEntries() []coreEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.entries)
}

func (c *modelCore) state() map[string]map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := make(map[string]map[string]string, len(c.users))
	for tag, users := range c.users {
		if len(users) == 0 {
			continue
		}
		state[tag] = make(map[string]string, len(users))
		for email, account := range users {
			state[tag][email] = account
		}
	}
	return state
}

func (c *modelCore) setRejectNextAdds(count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rejectAdds = count
}

func (c *modelCore) loadFrom(x *Xray) {
	local := localState(x)
	c.mu.Lock()
	defer c.mu.Unlock()
	for tag, users := range local {
		c.users[tag] = make(map[string]string, len(users))
		for email, account := range users {
			c.users[tag][email] = account
		}
	}
}

func localState(x *Xray) map[string]map[string]string {
	state := make(map[string]map[string]string)
	for _, inbound := range x.config.InboundConfigs {
		if inbound.exclude {
			continue
		}
		inbound.mu.RLock()
		for email, account := range inbound.clients {
			if state[inbound.Tag] == nil {
				state[inbound.Tag] = make(map[string]string)
			}
			state[inbound.Tag][email] = localFingerprint(inbound, account)
		}
		inbound.mu.RUnlock()
	}
	return state
}

func newModelXray(t *testing.T, core *modelCore, inbounds string) *Xray {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, core)
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

	return &Xray{config: cfg, handler: handler}
}

func syncTestUser(email string, inbounds ...string) *common.User {
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Vmess:       &common.Vmess{Id: "6f1c6c5e-3f59-4a0e-9d8a-2b4f1c9d7e10"},
			Vless:       &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11", Flow: "xtls-rprx-vision"},
			Trojan:      &common.Trojan{Password: "example-trojan-key"},
			Shadowsocks: &common.Shadowsocks{Password: "example-shadowsocks-key", Method: "aes-128-gcm"},
			Hysteria:    &common.Hysteria{Auth: "hysteria secret"},
		},
	}
}

func sortedOperations(pairs ...string) []string {
	pairs = slices.Clone(pairs)
	slices.Sort(pairs)
	return pairs
}

func replacedOn(tags ...string) []string {
	var pairs []string
	for _, tag := range tags {
		pairs = append(pairs, tag+":"+removeOperation, tag+":"+addOperation)
	}
	return pairs
}

func revokedOn(tags ...string) []string {
	var pairs []string
	for _, tag := range tags {
		pairs = append(pairs, tag+":"+revokeOperation)
	}
	return pairs
}

func tagsExcept(except ...string) []string {
	var tags []string
	for _, tag := range syncTestTags {
		if !slices.Contains(except, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

func requireOperations(t *testing.T, got []string, want ...string) {
	t.Helper()

	want = sortedOperations(want...)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Fatalf("operations sent to xray:\n got  %v\n want %v", got, want)
	}
}

func requireCoreMatchesLocal(t *testing.T, x *Xray, core *modelCore) {
	t.Helper()

	local := localState(x)
	remote := core.state()
	for tag, users := range remote {
		for email, account := range users {
			if local[tag][email] != account {
				t.Fatalf("the core holds %s on %s with %q, the node's state has %q", email, tag, account, local[tag][email])
			}
		}
	}
	for tag, users := range local {
		for email, account := range users {
			if remote[tag][email] != account {
				t.Fatalf("the node's state holds %s on %s with %q, the core has %q", email, tag, account, remote[tag][email])
			}
		}
	}
}

func syncTestContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSyncUserLeavesAnUnchangedUserAlone(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", syncTestTags...)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), replacedOn(syncTestTags...)...)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take())
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserLeavesAUserLoadedAtStartupAlone(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", syncTestTags...)

	x.config.syncUsers([]*common.User{user})
	core.loadFrom(x)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take())

	if err := x.UpdateUsers(ctx, []*common.User{user}); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take())
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserReplacesOnlyTheAccountThatChanged(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", syncTestTags...)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	core.take()

	user.Proxies.Trojan.Password = "rotated trojan secret"
	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), replacedOn("trojan")...)

	user.Proxies.Shadowsocks.Password = "rotated shadowsocks secret"
	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), replacedOn("ss", "ss2022")...)
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRevokesAUserOnTheInboundsTheyLeave(t *testing.T) {
	for _, stock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock=%v", stock), func(t *testing.T) {
			core := newModelCore(stock)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)

			if err := x.SyncUser(ctx, syncTestUser("1.alice", syncTestTags...)); err != nil {
				t.Fatal(err)
			}
			core.take()

			if err := x.SyncUser(ctx, syncTestUser("1.alice", "vless", "trojan")); err != nil {
				t.Fatal(err)
			}
			left := []string{"vmess", "ss", "ss2022", "hysteria"}
			want := revokedOn(left...)
			if stock {
				want = []string{"vmess:" + revokeOperation}
				for _, tag := range left {
					want = append(want, tag+":"+removeOperation)
				}
			}
			requireOperations(t, core.take(), want...)
			requireCoreMatchesLocal(t, x, core)
			if held := core.state(); len(held["vless"]) != 1 || len(held["trojan"]) != 1 || len(held["vmess"]) != 0 {
				t.Fatalf("the core holds %v after the user left four inbounds", held)
			}

			if err := x.SyncUser(ctx, syncTestUser("1.alice")); err != nil {
				t.Fatal(err)
			}
			requireCoreMatchesLocal(t, x, core)
			if held := core.state(); len(held) != 0 {
				t.Fatalf("the core still holds %v after the user left every inbound", held)
			}
		})
	}
}

func TestSyncUserRetriesAnAddThatFailed(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", "vless")

	core.setRejectNextAdds(1)
	if err := x.SyncUser(ctx, user); err == nil {
		t.Fatal("a rejected add was not reported")
	}
	core.take()
	if emails := clientEmails(x, "vless"); len(emails) != 0 {
		t.Fatalf("the node kept %v on vless although the core rejected the add", emails)
	}
	requireCoreMatchesLocal(t, x, core)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), append(replacedOn("vless"), revokedOn(tagsExcept("vless")...)...)...)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn(tagsExcept("vless")...)...)
	requireCoreMatchesLocal(t, x, core)
}

func TestSyncUserRetriesAChangedAccountWhoseAddFailed(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	user := syncTestUser("1.alice", "trojan")

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	core.take()

	user.Proxies.Trojan.Password = "rotated trojan secret"
	core.setRejectNextAdds(1)
	if err := x.SyncUser(ctx, user); err == nil {
		t.Fatal("a rejected add was not reported")
	}
	core.take()
	requireCoreMatchesLocal(t, x, core)

	if err := x.SyncUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), append(replacedOn("trojan"), revokedOn(tagsExcept("trojan")...)...)...)
	requireCoreMatchesLocal(t, x, core)
	if held := core.state()["trojan"]; len(held) != 1 {
		t.Fatalf("the core holds %v on trojan after the retry", held)
	}
}

func TestUpdateUsersLeavesUnchangedUsersAlone(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	alice := syncTestUser("1.alice", "vless", "trojan")
	bob := syncTestUser("2.bob", "vless", "trojan")

	if err := x.UpdateUsers(ctx, []*common.User{alice, bob}); err != nil {
		t.Fatal(err)
	}
	core.take()

	bob.Proxies.Vless.Id = "9c3e8f5a-1b2d-4c6e-8f0a-3d5b7c9e1f21"
	carol := syncTestUser("3.carol")
	if err := x.UpdateUsers(ctx, []*common.User{alice, bob, carol}); err != nil {
		t.Fatal(err)
	}

	want := []string{"vless:remove", "vless:add", "vless:revoke", "trojan:revoke"}
	for _, tag := range tagsExcept("vless", "trojan") {
		want = append(want, tag+":revoke", tag+":revoke", tag+":revoke")
	}
	requireOperations(t, core.take(), want...)
	requireCoreMatchesLocal(t, x, core)

	if err := x.UpdateUsers(ctx, []*common.User{alice, bob}); err != nil {
		t.Fatal(err)
	}
	want = nil
	for _, tag := range tagsExcept("vless", "trojan") {
		want = append(want, tag+":revoke", tag+":revoke")
	}
	requireOperations(t, core.take(), want...)
}

func TestUpdateUsersRetriesAnAddThatFailed(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)
	users := []*common.User{syncTestUser("1.alice", "trojan")}

	core.setRejectNextAdds(1)
	if err := x.UpdateUsers(ctx, users); err == nil {
		t.Fatal("a rejected add was not reported")
	}
	core.take()
	if emails := clientEmails(x, "trojan"); len(emails) != 0 {
		t.Fatalf("the node kept %v on trojan although the core rejected the add", emails)
	}

	if err := x.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), append(replacedOn("trojan"), revokedOn(tagsExcept("trojan")...)...)...)

	if err := x.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	requireOperations(t, core.take(), revokedOn(tagsExcept("trojan")...)...)
	requireCoreMatchesLocal(t, x, core)
}
