package mtproto

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/9seconds/mtg/v2/antireplay"
	"github.com/9seconds/mtg/v2/events"
	"github.com/9seconds/mtg/v2/ipblocklist"
	"github.com/9seconds/mtg/v2/mtglib"

	"github.com/pasarguard/node/common"
)

type nv8PushGate struct {
	once    sync.Once
	armed   chan struct{}
	entered chan struct{}
	release chan struct{}
}

func (g *nv8PushGate) hold() {
	select {
	case <-g.armed:
	default:
		return
	}
	first := false
	g.once.Do(func() { first = true })
	if !first {
		return
	}
	close(g.entered)
	<-g.release
}

type nv8GateLogger struct {
	mtglib.Logger
	gate *nv8PushGate
}

func (l *nv8GateLogger) Named(name string) mtglib.Logger {
	return &nv8GateLogger{Logger: l.Logger.Named(name), gate: l.gate}
}

func (l *nv8GateLogger) BindInt(name string, value int) mtglib.Logger {
	return &nv8GateLogger{Logger: l.Logger.BindInt(name, value), gate: l.gate}
}

func (l *nv8GateLogger) BindStr(name, value string) mtglib.Logger {
	return &nv8GateLogger{Logger: l.Logger.BindStr(name, value), gate: l.gate}
}

func (l *nv8GateLogger) BindJSON(name, value string) mtglib.Logger {
	return &nv8GateLogger{Logger: l.Logger.BindJSON(name, value), gate: l.gate}
}

func (l *nv8GateLogger) Warning(msg string) {
	if strings.Contains(msg, "shares its key") {
		l.gate.hold()
	}
	l.Logger.Warning(msg)
}

func nv8LiveSecretIDs(proxy *mtglib.Proxy) []string {
	v := reflect.ValueOf(proxy).Elem()
	mu := (*sync.RWMutex)(unsafe.Pointer(v.FieldByName("secretsMu").UnsafeAddr()))
	mu.RLock()
	defer mu.RUnlock()
	live := *(*map[string]mtglib.Secret)(unsafe.Pointer(v.FieldByName("secrets").UnsafeAddr()))
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func TestNodeVerifyAnOlderSecretsSnapshotNeverLandsAfterANewerRemoval(t *testing.T) {
	const tag = "mtproto-nv8"
	const sharedSecret = "00112233445566778899aabbccddeeff"
	const bobSecret = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

	gate := &nv8PushGate{armed: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{})}
	b := scopedBackend(tag)
	b.applyInitialUsers([]*common.User{
		credentialedUser("alice", sharedSecret, tag),
		credentialedUser("bob", bobSecret, tag),
	})

	ntw, err := buildNetwork()
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	proxy, err := mtglib.NewProxy(mtglib.ProxyOpts{
		Logger:          &nv8GateLogger{Logger: newMtgLogger("nv8", func(string) {}), gate: gate},
		Network:         ntw,
		AntiReplayCache: antireplay.NewNoop(),
		IPBlocklist:     ipblocklist.NewNoop(),
		IPAllowlist:     allowEveryone{},
		EventStream:     events.NewNoopStream(),
		Secrets:         b.secretsForInstance("example.com"),
		PlainMode:       true,
	})
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	t.Cleanup(proxy.Shutdown)
	b.instances[tag] = &proxyInstance{tag: tag, domain: "example.com", proxy: proxy}

	if got := nv8LiveSecretIDs(proxy); strings.Join(got, ",") != "alice,bob" {
		t.Fatalf("live secrets before the race = %v, want [alice bob]", got)
	}

	close(gate.armed)
	ctx := context.Background()
	olderPush := make(chan struct{})
	go func() {
		defer close(olderPush)
		_ = b.UpdateUsers(ctx, []*common.User{credentialedUser("carol", sharedSecret, tag)})
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the push carrying the older snapshot never reached UpdateSecrets")
	}

	newerRemoval := make(chan struct{})
	go func() {
		defer close(newerRemoval)
		_ = b.UpdateUsers(ctx, []*common.User{{Email: "alice"}, {Email: "carol"}})
	}()
	select {
	case <-newerRemoval:
	case <-time.After(300 * time.Millisecond):
	}

	close(gate.release)
	for _, done := range []chan struct{}{olderPush, newerRemoval} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a user sync never returned")
		}
	}

	if _, kept := authorisedEntries(b)["alice"]; kept {
		t.Fatal("alice is still in secretsByID after her removal returned")
	}
	if got := nv8LiveSecretIDs(proxy); strings.Join(got, ",") != "bob" {
		t.Fatalf("the live proxy accepts %v after alice's and carol's removal returned last; an older snapshot was applied after it (want [bob])", got)
	}
}
