package xray

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	statscommand "github.com/xtls/xray-core/app/stats/command"
	"log"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const credentialInbounds = `{"log":{"loglevel":"warning"},"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"trojan","protocol":"trojan","settings":{"clients":[]}},
	{"tag":"vmess","protocol":"vmess","settings":{"clients":[]}}
]}`

const (
	heldVlessID    = "6a3e0f5c-2b1d-4c8e-9f7a-1d2e3f4a5b6c"
	variantVlessID = "6a3e0f5c-2b1d-ffff-9f7a-1d2e3f4a5b6c"
	sharedSecret   = "shared-credential-secret"
)

type credentialKind struct {
	name     string
	tag      string
	key      string
	holder   func(email string, inbounds ...string) *common.User
	claimant func(email string, inbounds ...string) *common.User
}

func ownCredentialUser(email string, inbounds ...string) *common.User {
	id := uuid.NewSHA1(interleaveNamespace, []byte("credential/"+email)).String()
	return &common.User{
		Email:    email,
		Inbounds: inbounds,
		Proxies: &common.Proxy{
			Vless:  &common.Vless{Id: id},
			Vmess:  &common.Vmess{Id: id},
			Trojan: &common.Trojan{Password: "own/" + email},
		},
	}
}

func vlessCredentialUser(id string) func(string, ...string) *common.User {
	return func(email string, inbounds ...string) *common.User {
		return &common.User{Email: email, Inbounds: inbounds, Proxies: &common.Proxy{Vless: &common.Vless{Id: id}}}
	}
}

func trojanCredentialUser(email string, inbounds ...string) *common.User {
	return &common.User{Email: email, Inbounds: inbounds, Proxies: &common.Proxy{Trojan: &common.Trojan{Password: sharedSecret}}}
}

func vmessCredentialUser(email string, inbounds ...string) *common.User {
	return &common.User{Email: email, Inbounds: inbounds, Proxies: &common.Proxy{Vmess: &common.Vmess{Id: heldVlessID}}}
}

func credentialKinds() []credentialKind {
	return []credentialKind{
		{name: "vless", tag: "vless", key: vlessKey(heldVlessID), holder: vlessCredentialUser(heldVlessID), claimant: vlessCredentialUser(variantVlessID)},
		{name: "trojan", tag: "trojan", key: "trojan:" + sharedSecret, holder: trojanCredentialUser, claimant: trojanCredentialUser},
		{name: "vmess", tag: "vmess", key: vmessKey(heldVlessID), holder: vmessCredentialUser, claimant: vmessCredentialUser},
	}
}

func vlessKey(id string) string {
	parsed := uuid.MustParse(id)
	parsed[6], parsed[7] = 0, 0
	return "vless:" + hex.EncodeToString(parsed[:])
}

func vmessKey(id string) string {
	return "vmess:" + uuid.MustParse(id).String()
}

func coreKey(protocol string, id, password string) (string, error) {
	switch protocol {
	case Vless:
		parsed, err := uuid.Parse(id)
		if err != nil {
			return "", err
		}
		return vlessKey(parsed.String()), nil
	case Vmess:
		parsed, err := uuid.Parse(id)
		if err != nil {
			return "", err
		}
		return vmessKey(parsed.String()), nil
	case Trojan:
		return "trojan:" + password, nil
	}
	return "", nil
}

func typedAccountKey(account *serial.TypedMessage) (string, error) {
	switch {
	case strings.HasSuffix(account.GetType(), "vless.Account"):
		var message vless.Account
		if err := proto.Unmarshal(account.GetValue(), &message); err != nil {
			return "", err
		}
		return coreKey(Vless, message.GetId(), "")
	case strings.HasSuffix(account.GetType(), "vmess.Account"):
		var message vmess.Account
		if err := proto.Unmarshal(account.GetValue(), &message); err != nil {
			return "", err
		}
		return coreKey(Vmess, message.GetId(), "")
	case strings.HasSuffix(account.GetType(), "trojan.Account"):
		var message trojan.Account
		if err := proto.Unmarshal(account.GetValue(), &message); err != nil {
			return "", err
		}
		return coreKey(Trojan, "", message.GetPassword())
	}
	return "", nil
}

type credentialEvent struct {
	op      string
	tag     string
	email   string
	failed  bool
	holder  string
	present []string
}

type credentialCore struct {
	command.UnimplementedHandlerServiceServer
	stock      bool
	watchTag   string
	watchKey   string
	mu         sync.Mutex
	down       bool
	users      map[string]map[string]string
	keys       map[string]map[string]string
	events     []credentialEvent
	adds       []string
	operations []string
}

func newCredentialCore(stock bool, watchTag, watchKey string) *credentialCore {
	return &credentialCore{
		stock:    stock,
		watchTag: watchTag,
		watchKey: watchKey,
		users:    make(map[string]map[string]string),
		keys:     make(map[string]map[string]string),
	}
}

func (c *credentialCore) tables(tag string) (map[string]string, map[string]string) {
	if c.users[tag] == nil {
		c.users[tag] = make(map[string]string)
		c.keys[tag] = make(map[string]string)
	}
	return c.users[tag], c.keys[tag]
}

func (c *credentialCore) recordLocked(op, tag, email string, failed bool) {
	present := make([]string, 0, len(c.users[c.watchTag]))
	for held := range c.users[c.watchTag] {
		present = append(present, held)
	}
	slices.Sort(present)
	c.events = append(c.events, credentialEvent{op: op, tag: tag, email: email, failed: failed, holder: c.keys[c.watchTag][c.watchKey], present: present})
}

func (c *credentialCore) addLocked(tag, email, key string) error {
	users, keys := c.tables(tag)
	if _, found := users[email]; found {
		return fmt.Errorf("proxy/%s: User %s already exists.", tag, email)
	}
	if key != "" {
		if holder, held := keys[key]; held && !strings.EqualFold(holder, email) && !c.stock {
			return fmt.Errorf("proxy/%s: User %s: the credential is already held by user %s.", tag, email, holder)
		}
		keys[key] = email
	}
	users[email] = key
	return nil
}

func (c *credentialCore) removeLocked(tag, email string) error {
	users, keys := c.tables(tag)
	key, found := users[email]
	if !found {
		return fmt.Errorf("proxy/%s: User %s not found.", tag, email)
	}
	delete(users, email)
	if key != "" && (c.stock || keys[key] == email) {
		delete(keys, key)
	}
	return nil
}

func (c *credentialCore) AlterInbound(_ context.Context, request *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tag := request.GetTag()
	op := operationName(request.GetOperation().GetType())
	if c.down {
		c.recordLocked(op, tag, "", true)
		return nil, status.Error(codes.Unavailable, "connection refused")
	}

	var email string
	var err error
	switch op {
	case addOperation:
		var add command.AddUserOperation
		if err = proto.Unmarshal(request.GetOperation().GetValue(), &add); err != nil {
			return nil, err
		}
		email = add.GetUser().GetEmail()
		var key string
		if key, err = typedAccountKey(add.GetUser().GetAccount()); err == nil {
			err = c.addLocked(tag, email, key)
		}
		if err == nil {
			c.adds = append(c.adds, tag+":"+email)
		}
	case removeOperation, revokeOperation:
		var remove command.RemoveUserOperation
		if err = proto.Unmarshal(request.GetOperation().GetValue(), &remove); err != nil {
			return nil, err
		}
		email = remove.GetEmail()
		err = c.removeLocked(tag, email)
	default:
		err = fmt.Errorf("unexpected operation %s", op)
	}
	c.operations = append(c.operations, tag+":"+op+":"+email)
	c.recordLocked(op, tag, email, err != nil)
	if err != nil {
		return nil, status.Error(codes.Unknown, err.Error())
	}
	return &command.AlterInboundResponse{}, nil
}

func (c *credentialCore) restart(config *Config) error {
	c.mu.Lock()
	c.down = true
	c.recordLocked("stop", "", "", false)
	c.mu.Unlock()

	raw, err := config.ToBytes()
	if err != nil {
		return err
	}
	time.Sleep(time.Duration(rand.IntN(400)) * time.Microsecond)

	var parsed struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []struct {
					Email    string `json:"email"`
					ID       string `json:"id"`
					Password string `json:"password"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.users = make(map[string]map[string]string)
	c.keys = make(map[string]map[string]string)
	for _, inbound := range parsed.Inbounds {
		for _, client := range inbound.Settings.Clients {
			key, err := coreKey(inbound.Protocol, client.ID, client.Password)
			if err == nil {
				err = c.addLocked(inbound.Tag, client.Email, key)
			}
			if err != nil {
				c.recordLocked("load-failed", inbound.Tag, client.Email, true)
				return fmt.Errorf("failed to initiate user: %w", err)
			}
		}
	}
	c.down = false
	c.recordLocked("load", "", "", false)
	return nil
}

func (c *credentialCore) holder(tag, key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys[tag][key]
}

func (c *credentialCore) holds(tag, email string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, found := c.users[tag][email]
	return found
}

func (c *credentialCore) takeOperations() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	operations := c.operations
	c.operations = nil
	return operations
}

func (c *credentialCore) allEvents() []credentialEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.events)
}

func (c *credentialCore) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *credentialCore) heldState() map[string]map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := make(map[string]map[string]bool)
	for tag, users := range c.users {
		for email := range users {
			if state[tag] == nil {
				state[tag] = make(map[string]bool)
			}
			state[tag][email] = true
		}
	}
	return state
}

func newCredentialXray(t *testing.T, core *credentialCore) *Xray {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, core)
	statscommand.RegisterStatsServiceServer(server, &statsModel{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	handler, err := api.NewXrayAPI(listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.Close)

	cfg, err := NewConfig(credentialInbounds, nil)
	if err != nil {
		t.Fatal(err)
	}

	x := &Xray{config: cfg, handler: handler, core: &Core{
		logsChan:       make(chan string, 64),
		logPhase:       logPhaseRuntime,
		startupLogSize: 200,
		runtimeLogs:    newStartupLogRing(64),
	}}
	x.restartCore = core.restart
	x.readyPoll = 5 * time.Millisecond
	return x
}

func captureNodeLog(t *testing.T) *syncBuffer {
	t.Helper()
	buffer := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buffer
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func requireRefusalLogged(t *testing.T, logs *syncBuffer, email, holder string) {
	t.Helper()
	want := fmt.Sprintf("xray: user %s rejected: ", email)
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, want) && strings.Contains(line, "already held by user "+holder) {
			return
		}
	}
	t.Fatalf("no log line names %s's refusal for a credential %s holds; log:\n%s", email, holder, logs.String())
}

func requireNodeHolds(t *testing.T, x *Xray, tag string, want ...string) {
	t.Helper()
	got := clientEmails(x, tag)
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the node holds %v on %s, want %v", got, tag, want)
	}
}

func requireCoreHoldsExactlyTheNodeState(t *testing.T, x *Xray, core *credentialCore) {
	t.Helper()
	remote := core.heldState()
	for _, inbound := range x.config.InboundConfigs {
		for _, email := range clientEmails(x, inbound.Tag) {
			if !remote[inbound.Tag][email] {
				t.Fatalf("the node holds %s on %s but the core does not", email, inbound.Tag)
			}
			delete(remote[inbound.Tag], email)
		}
	}
	for tag, users := range remote {
		for email := range users {
			t.Fatalf("the core holds %s on %s but the node does not", email, tag)
		}
	}
}

func TestAStartupConfigGivesASharedCredentialToTheOldestUser(t *testing.T) {
	for _, kind := range credentialKinds() {
		t.Run(kind.name, func(t *testing.T) {
			cfg, err := NewConfig(credentialInbounds, nil)
			if err != nil {
				t.Fatal(err)
			}
			rejections := cfg.syncUsers([]*common.User{
				kind.claimant("12", kind.tag),
				kind.holder("9", kind.tag),
				kind.claimant("10", kind.tag),
				ownCredentialUser("3", kind.tag),
			})

			raw, err := cfg.ToBytes()
			if err != nil {
				t.Fatal(err)
			}
			got := emittedClients(t, raw)[kind.tag]
			if !slices.Equal(got, []string{"3", "9"}) {
				t.Fatalf("the config emits %v on %s, want the oldest holder 9 and the unrelated 3", got, kind.tag)
			}
			want := fmt.Sprintf("its %s on inbound %s is already held by user 9", map[string]string{"vless": "vless id", "trojan": "trojan password", "vmess": "vmess id"}[kind.name], kind.tag)
			rejected := rejections.Users()
			if len(rejected) != 2 || rejected[0].Email != "10" || rejected[1].Email != "12" || rejected[0].Reason != want || rejected[1].Reason != want {
				t.Fatalf("the config build names %+v, want 10 and 12 refused with %q", rejected, want)
			}
		})
	}
}

func TestARestartingFullSyncKeepsASharedCredentialWithItsIncumbent(t *testing.T) {
	for _, kind := range credentialKinds() {
		for _, stock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stock=%v", kind.name, stock), func(t *testing.T) {
				core := newCredentialCore(stock, kind.tag, kind.key)
				x := newCredentialXray(t, core)
				ctx := syncTestContext(t)
				incumbent := kind.claimant("12", kind.tag)
				if err := x.UpdateUsers(ctx, []*common.User{incumbent}); err != nil {
					t.Fatal(err)
				}

				candidate, err := x.config.Clone()
				if err != nil {
					t.Fatal(err)
				}
				candidate.syncUsers([]*common.User{incumbent, kind.holder("7", kind.tag)})
				if err := x.restartCore(candidate); err != nil {
					t.Fatalf("the core refused the full sync's config: %v", err)
				}
				if got := core.holder(kind.tag, kind.key); got != "12" {
					t.Fatalf("after the full sync the credential is held by %q, want the incumbent 12", got)
				}
			})
		}
	}
}

func TestSyncUserRefusesAUserWhoseCredentialAnotherUserHolds(t *testing.T) {
	for _, kind := range credentialKinds() {
		for _, stock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stock=%v", kind.name, stock), func(t *testing.T) {
				core := newCredentialCore(stock, kind.tag, kind.key)
				x := newCredentialXray(t, core)
				ctx := syncTestContext(t)
				if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag)}); err != nil {
					t.Fatal(err)
				}
				core.takeOperations()
				logs := captureNodeLog(t)

				if err := x.SyncUser(ctx, kind.claimant("12", kind.tag)); err != nil {
					t.Fatalf("a per-user refusal must not fail the sync: %v", err)
				}
				if ops := core.takeOperations(); len(ops) != 0 {
					t.Fatalf("the refused user reached the core: %v", ops)
				}
				if got := core.holder(kind.tag, kind.key); got != "7" {
					t.Fatalf("the credential is held by %q, want its holder 7", got)
				}
				requireRefusalLogged(t, logs, "12", "7")
				requireNodeHolds(t, x, kind.tag, "7")
				requireCoreHoldsExactlyTheNodeState(t, x, core)
			})
		}
	}
}

func TestUpdateUsersRefusesOnlyTheUsersWhoseCredentialIsTaken(t *testing.T) {
	for _, kind := range credentialKinds() {
		for _, stock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stock=%v", kind.name, stock), func(t *testing.T) {
				core := newCredentialCore(stock, kind.tag, kind.key)
				x := newCredentialXray(t, core)
				ctx := syncTestContext(t)
				if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag)}); err != nil {
					t.Fatal(err)
				}
				logs := captureNodeLog(t)

				if err := x.UpdateUsers(ctx, []*common.User{kind.claimant("12", kind.tag), ownCredentialUser("3", kind.tag)}); err != nil {
					t.Fatalf("a per-user refusal must not fail the sync: %v", err)
				}
				requireRefusalLogged(t, logs, "12", "7")
				requireNodeHolds(t, x, kind.tag, "3", "7")
				requireCoreHoldsExactlyTheNodeState(t, x, core)
				if got := core.holder(kind.tag, kind.key); got != "7" {
					t.Fatalf("the credential is held by %q, want its holder 7", got)
				}
			})
		}
	}
}

func TestTwoNewcomersOnOneCredentialInOneBatchGiveItToTheOldest(t *testing.T) {
	for _, kind := range credentialKinds() {
		t.Run(kind.name, func(t *testing.T) {
			core := newCredentialCore(false, kind.tag, kind.key)
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			logs := captureNodeLog(t)

			if err := x.UpdateUsers(ctx, []*common.User{kind.holder("15", kind.tag), kind.claimant("9", kind.tag)}); err != nil {
				t.Fatalf("a per-user refusal must not fail the sync: %v", err)
			}
			if got := core.holder(kind.tag, kind.key); got != "9" {
				t.Fatalf("the credential is held by %q, want the oldest claimant 9", got)
			}
			requireRefusalLogged(t, logs, "15", "9")
			requireCoreHoldsExactlyTheNodeState(t, x, core)
		})
	}
}

func TestAWaitingUserGetsTheCredentialOnceItsHolderLeaves(t *testing.T) {
	leave := map[string]func(context.Context, *Xray, *common.User) error{
		"UpdateUsers": func(ctx context.Context, x *Xray, user *common.User) error {
			return x.UpdateUsers(ctx, []*common.User{user})
		},
		"SyncUser": func(ctx context.Context, x *Xray, user *common.User) error {
			return x.SyncUser(ctx, user)
		},
	}
	for _, kind := range credentialKinds() {
		for path, apply := range leave {
			for _, stock := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stock=%v", kind.name, path, stock), func(t *testing.T) {
					core := newCredentialCore(stock, kind.tag, kind.key)
					x := newCredentialXray(t, core)
					ctx := syncTestContext(t)
					if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag)}); err != nil {
						t.Fatal(err)
					}
					if err := x.SyncUser(ctx, kind.claimant("12", kind.tag)); err != nil {
						t.Fatalf("a per-user refusal must not fail the sync: %v", err)
					}
					core.takeOperations()

					if err := apply(ctx, x, kind.holder("7")); err != nil {
						t.Fatal(err)
					}
					if got := core.holder(kind.tag, kind.key); got != "12" {
						t.Fatalf("after its holder left the credential is held by %q, want the waiting 12", got)
					}
					ops := core.takeOperations()
					revoked := slices.IndexFunc(ops, func(op string) bool { return strings.HasSuffix(op, ":7") && !strings.Contains(op, ":add:") })
					added := slices.Index(ops, kind.tag+":add:12")
					if revoked < 0 || added < 0 || added < revoked {
						t.Fatalf("the waiting user must be added after the holder was revoked: %v", ops)
					}
					requireNodeHolds(t, x, kind.tag, "12")
					requireCoreHoldsExactlyTheNodeState(t, x, core)
				})
			}
		}
	}
}

func TestAWaitingUserWhoLeavesIsNeverInstalled(t *testing.T) {
	for _, kind := range credentialKinds() {
		t.Run(kind.name, func(t *testing.T) {
			core := newCredentialCore(false, kind.tag, kind.key)
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag)}); err != nil {
				t.Fatal(err)
			}
			if err := x.SyncUser(ctx, kind.claimant("12", kind.tag)); err != nil {
				t.Fatalf("a per-user refusal must not fail the sync: %v", err)
			}
			if err := x.SyncUser(ctx, kind.claimant("12")); err != nil {
				t.Fatal(err)
			}
			if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7")}); err != nil {
				t.Fatal(err)
			}
			if err := x.Restart(); err != nil {
				t.Fatal(err)
			}
			if got := core.holder(kind.tag, kind.key); got != "" {
				t.Fatalf("the credential is held by %q after both users left", got)
			}
			if core.holds(kind.tag, "12") {
				t.Fatal("a user who left while waiting was installed")
			}
		})
	}
}

func raceRemovalClaimAndRestart(t *testing.T, kind credentialKind, stock bool, round int) {
	core := newCredentialCore(stock, kind.tag, kind.key)
	x := newCredentialXray(t, core)
	ctx := syncTestContext(t)
	bystanders := []*common.User{ownCredentialUser("1", kind.tag), ownCredentialUser("2", kind.tag)}
	if err := x.UpdateUsers(ctx, append(slices.Clone(bystanders), kind.holder("7", kind.tag))); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var removeErr, claimErr, restartErr error
	removedAt := -1
	var removedMu sync.Mutex
	jitter := func() { time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond) }
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		jitter()
		removeErr = x.UpdateUsers(ctx, []*common.User{kind.holder("7")})
		removedMu.Lock()
		removedAt = core.eventCount()
		removedMu.Unlock()
	}()
	go func() {
		defer wg.Done()
		<-start
		jitter()
		claimErr = x.SyncUser(ctx, kind.claimant("12", kind.tag))
	}()
	go func() {
		defer wg.Done()
		<-start
		jitter()
		restartErr = x.Restart()
	}()
	close(start)
	wg.Wait()

	if err := errors.Join(removeErr, claimErr, restartErr); err != nil {
		t.Fatalf("round %d: a call failed: %v", round, err)
	}
	events := core.allEvents()
	for index, event := range events[removedAt:] {
		if slices.Contains(event.present, "7") {
			t.Fatalf("round %d: the removed holder 7 was back on the core at event %d (%s %s) after its removal returned", round, removedAt+index, event.op, event.email)
		}
	}
	installed := false
	for index, event := range events {
		switch {
		case event.holder == "12":
			installed = true
		case installed && event.op != "stop":
			t.Fatalf("round %d: 12 held the credential and lost it at event %d (%s %s on %s, holder %q)", round, index, event.op, event.email, event.tag, event.holder)
		}
	}
	if got := core.holder(kind.tag, kind.key); got != "12" {
		t.Fatalf("round %d: the credential is held by %q at quiescence, want 12", round, got)
	}
	requireNodeHolds(t, x, kind.tag, "1", "12", "2")
	requireCoreHoldsExactlyTheNodeState(t, x, core)
}

func TestRemovingAHolderWhileAnotherUserClaimsItsCredentialAcrossARestart(t *testing.T) {
	for _, kind := range credentialKinds() {
		for _, stock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stock=%v", kind.name, stock), func(t *testing.T) {
				for round := range 40 {
					raceRemovalClaimAndRestart(t, kind, stock, round)
				}
			})
		}
	}
}

func TestUsersSharingAVlessIDOnARealCore(t *testing.T) {
	forEachXrayBinary(t, func(t *testing.T, binary string) {
		holder := sessionUser("7", uuid.MustParse(heldVlessID), sessionTestInbound)
		claimant := sessionUser("12", uuid.MustParse(variantVlessID), sessionTestInbound)
		back, port := startSessionBackend(t, binary, holder, claimant)
		target := startByteSource(t)

		conn := openVlessStream(t, port, uuid.MustParse(heldVlessID), target)
		requireFlowing(t, conn, "with both users on the core")
		_ = conn.Close()

		ctx := syncTestContext(t)
		if err := back.UpdateUsers(ctx, []*common.User{withoutInbounds(holder)}); err != nil {
			t.Fatal(err)
		}
		conn = openVlessStream(t, port, uuid.MustParse(variantVlessID), target)
		requireFlowing(t, conn, "after the holder left")
	})
}

func TestASyncNamesEveryRefusedUserForTheController(t *testing.T) {
	kind := credentialKinds()[0]
	core := newCredentialCore(false, kind.tag, kind.key)
	x := newCredentialXray(t, core)
	ctx := syncTestContext(t)
	if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag)}); err != nil {
		t.Fatal(err)
	}

	calls := map[string]struct {
		email string
		apply func() error
	}{
		"syncUserLocked":    {email: "12", apply: func() error { return x.syncUserLocked(ctx, kind.claimant("12", kind.tag)) }},
		"updateUsersLocked": {email: "13", apply: func() error { return x.updateUsersLocked(ctx, []*common.User{kind.claimant("13", kind.tag)}) }},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call.apply()
			var report *backend.RejectedUsersError
			if !errors.As(err, &report) {
				t.Fatalf("the refusal is not a named rejection: %v", err)
			}
			rejected, systemic := backend.SplitRejected(err)
			if systemic != nil {
				t.Fatalf("a per-user refusal carried a systemic error: %v", systemic)
			}
			if len(rejected) != 1 || rejected[0].Email != call.email || !strings.Contains(rejected[0].Reason, "vless id on inbound vless is already held by user 7") {
				t.Fatalf("the rejection does not name %s and its holder: %+v", call.email, rejected)
			}
		})
	}
}

func TestAUserMovingOntoAHeldCredentialLosesItsOldOne(t *testing.T) {
	for _, kind := range credentialKinds() {
		for _, stock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stock=%v", kind.name, stock), func(t *testing.T) {
				core := newCredentialCore(stock, kind.tag, kind.key)
				x := newCredentialXray(t, core)
				ctx := syncTestContext(t)
				if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag), ownCredentialUser("12", kind.tag)}); err != nil {
					t.Fatal(err)
				}
				logs := captureNodeLog(t)

				if err := x.SyncUser(ctx, kind.claimant("12", kind.tag)); err != nil {
					t.Fatalf("a per-user refusal must not fail the sync: %v", err)
				}
				requireRefusalLogged(t, logs, "12", "7")
				if core.holds(kind.tag, "12") {
					t.Fatal("12 keeps its old credential after moving onto one that 7 holds")
				}
				if got := core.holder(kind.tag, kind.key); got != "7" {
					t.Fatalf("the credential is held by %q, want its holder 7", got)
				}
				requireCoreHoldsExactlyTheNodeState(t, x, core)
			})
		}
	}
}

func TestARestartingUpdateThatRemovesTheHolderInstallsTheWaitingUser(t *testing.T) {
	for _, kind := range credentialKinds() {
		t.Run(kind.name, func(t *testing.T) {
			core := newCredentialCore(false, kind.tag, kind.key)
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag)}); err != nil {
				t.Fatal(err)
			}
			if err := x.SyncUser(ctx, kind.claimant("12", kind.tag)); err != nil {
				t.Fatalf("a per-user refusal must not fail the sync: %v", err)
			}

			candidate, err := x.config.Clone()
			if err != nil {
				t.Fatal(err)
			}
			candidate.updateUsers([]*common.User{kind.holder("7")})
			if err := x.restartCore(candidate); err != nil {
				t.Fatalf("the core refused the restarting update's config: %v", err)
			}
			if got := core.holder(kind.tag, kind.key); got != "12" {
				t.Fatalf("after a restarting update removed the holder the credential is held by %q, want the waiting 12", got)
			}
		})
	}
}

func TestAFullSyncThatLeavesOutAWaitingUserNeverInstallsIt(t *testing.T) {
	for _, kind := range credentialKinds() {
		t.Run(kind.name, func(t *testing.T) {
			core := newCredentialCore(false, kind.tag, kind.key)
			x := newCredentialXray(t, core)
			ctx := syncTestContext(t)
			if err := x.UpdateUsers(ctx, []*common.User{kind.holder("7", kind.tag), ownCredentialUser("3", kind.tag)}); err != nil {
				t.Fatal(err)
			}
			if err := x.SyncUser(ctx, kind.claimant("12", kind.tag)); err != nil {
				t.Fatalf("a per-user refusal must not fail the sync: %v", err)
			}

			candidate, err := x.config.Clone()
			if err != nil {
				t.Fatal(err)
			}
			candidate.syncUsers([]*common.User{ownCredentialUser("3", kind.tag)})
			if err := x.restartCore(candidate); err != nil {
				t.Fatalf("the core refused the full sync's config: %v", err)
			}
			if core.holds(kind.tag, "12") || core.holder(kind.tag, kind.key) != "" {
				t.Fatalf("a full sync without 12 installed it; the credential is held by %q", core.holder(kind.tag, kind.key))
			}
		})
	}
}
