package xray

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

// TestRoutingMethodsErrorWhenNotStarted verifies every RoutingBackend method on
// *Xray goes through the routingHandler() guard and errors (rather than panics)
// when the core is not started / the handler is nil.
func TestRoutingMethodsErrorWhenNotStarted(t *testing.T) {
	x := &Xray{} // core nil, handler nil => not started
	ctx := context.Background()

	if _, err := x.ListRoutingRules(ctx); err == nil {
		t.Fatal("ListRoutingRules: expected error when not started")
	}
	if _, err := x.GetBalancerInfo(ctx, "b"); err == nil {
		t.Fatal("GetBalancerInfo: expected error when not started")
	}
	if _, err := x.TestRoute(ctx, &common.TestRouteRequest{}); err == nil {
		t.Fatal("TestRoute: expected error when not started")
	}
	if err := x.AddRoutingRule(ctx, "{}", false); err == nil {
		t.Fatal("AddRoutingRule: expected error when not started")
	}
	if err := x.RemoveRoutingRule(ctx, "r"); err == nil {
		t.Fatal("RemoveRoutingRule: expected error when not started")
	}
	if err := x.OverrideBalancerTarget(ctx, "b", "t"); err == nil {
		t.Fatal("OverrideBalancerTarget: expected error when not started")
	}
}

func startRoutingBackend(t *testing.T) *Xray {
	t.Helper()

	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}

	raw := fmt.Sprintf(`{
		"inbounds": [{
			"tag": "VLESS TCP ROUTING",
			"listen": "127.0.0.1",
			"port": %d,
			"protocol": "vless",
			"settings": {"clients": [], "decryption": "none"}
		}],
		"outbounds": [
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"}
		]
	}`, netutil.FindFreePort())

	xrayConfig, err := NewConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if xrayConfig.API != nil {
		t.Fatalf("test config already carries an api section: %#v", xrayConfig.API)
	}

	back, err := New(context.Background(), xrayConfig, nil, netutil.FindFreePort(), netutil.FindFreePort(), &config.Config{
		XrayExecutablePath:  executablePath,
		XrayAssetsPath:      assetsPath,
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(back.Shutdown)
	return back
}

func TestRoutingServiceServesWithoutAPanelOptIn(t *testing.T) {
	back := startRoutingBackend(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rules, err := back.ListRoutingRules(ctx)
	if err != nil {
		t.Fatalf("ListRoutingRules on a panel config that never lists routingservice: %v", err)
	}

	tags := make([]string, 0, len(rules.GetRules()))
	for _, rule := range rules.GetRules() {
		tags = append(tags, rule.GetRuleTag())
	}
	if !slices.Contains(tags, apiGuardIPRuleTag) {
		t.Fatalf("routing rule tags = %v, want %s among them", tags, apiGuardIPRuleTag)
	}
}

func TestAddAndRemoveRoutingRuleWithoutAPanelOptIn(t *testing.T) {
	back := startRoutingBackend(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ruleTag = "pg-routing-optin"
	rule := fmt.Sprintf(`{"type": "field", "ruleTag": %q, "domain": ["example.com"], "outboundTag": "block"}`, ruleTag)
	if err := back.AddRoutingRule(ctx, rule, true); err != nil {
		t.Fatalf("AddRoutingRule on a panel config that never lists routingservice: %v", err)
	}

	hasRule := func() bool {
		rules, err := back.ListRoutingRules(ctx)
		if err != nil {
			t.Fatalf("ListRoutingRules: %v", err)
		}
		for _, r := range rules.GetRules() {
			if r.GetRuleTag() == ruleTag {
				return true
			}
		}
		return false
	}

	if !hasRule() {
		t.Fatal("the rule added over the routing API is not in the running core")
	}
	if err := back.RemoveRoutingRule(ctx, ruleTag); err != nil {
		t.Fatalf("RemoveRoutingRule: %v", err)
	}
	if hasRule() {
		t.Fatal("the rule removed over the routing API is still in the running core")
	}
}

func TestRestartDropsRulesAddedOverTheRoutingAPI(t *testing.T) {
	back := startRoutingBackend(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const ruleTag = "pg-routing-rpc-only"
	rule := fmt.Sprintf(`{"type": "field", "ruleTag": %q, "domain": ["example.com"], "outboundTag": "block"}`, ruleTag)
	if err := back.AddRoutingRule(ctx, rule, true); err != nil {
		t.Fatal(err)
	}

	hasRule := func() bool {
		rules, err := back.ListRoutingRules(ctx)
		if err != nil {
			t.Fatalf("ListRoutingRules: %v", err)
		}
		for _, r := range rules.GetRules() {
			if r.GetRuleTag() == ruleTag {
				return true
			}
		}
		return false
	}

	if !hasRule() {
		t.Fatal("the rule added over the routing API is not in the running core")
	}
	if err := back.Restart(); err != nil {
		t.Fatal(err)
	}
	if hasRule() {
		t.Fatalf("a restart kept %s: the active config now tracks routing-API edits, so any live-apply port must preserve them too", ruleTag)
	}
}

func TestRoutingAPIEditsInterleaveWithRestartsAndUserSyncs(t *testing.T) {
	back := startRoutingBackend(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	const inboundTag = "VLESS TCP ROUTING"
	alice := sessionUser("1.alice", uuid.New(), inboundTag)
	bob := sessionUser("2.bob", uuid.New(), inboundTag)

	var wg sync.WaitGroup
	var routingCalls, routingServed atomic.Int32
	editorDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-editorDone:
				return
			case <-ctx.Done():
				return
			default:
			}
			tag := fmt.Sprintf("pg-interleave-%d", i)
			rule := fmt.Sprintf(`{"type": "field", "ruleTag": %q, "domain": ["example.com"], "outboundTag": "block"}`, tag)
			routingCalls.Add(1)
			if err := back.AddRoutingRule(ctx, rule, true); err == nil {
				routingServed.Add(1)
				_ = back.RemoveRoutingRule(ctx, tag)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	syncsDone := make(chan struct{})
	go func() {
		defer close(syncsDone)
		for _, users := range [][]*common.User{{alice}, {alice, bob}, {bob}} {
			if ctx.Err() != nil {
				return
			}
			if err := back.SyncUsers(ctx, users); err != nil {
				t.Errorf("SyncUsers(%d users): %v", len(users), err)
			}
		}
	}()

	for i := 0; i < 2; i++ {
		if err := back.Restart(); err != nil {
			t.Errorf("restart %d: %v", i, err)
		}
	}
	<-syncsDone
	close(editorDone)
	wg.Wait()

	if t.Failed() {
		t.FailNow()
	}
	if routingServed.Load() == 0 {
		t.Fatalf("none of the %d routing calls was served: the routing API never came back during the interleave", routingCalls.Load())
	}

	rules, err := back.ListRoutingRules(ctx)
	if err != nil {
		t.Fatalf("ListRoutingRules after the interleave: %v", err)
	}
	tags := make([]string, 0, len(rules.GetRules()))
	for _, rule := range rules.GetRules() {
		tags = append(tags, rule.GetRuleTag())
	}
	if !slices.Contains(tags, apiGuardIPRuleTag) {
		t.Fatalf("routing rule tags after the interleave = %v, want %s among them", tags, apiGuardIPRuleTag)
	}

	if err := back.SyncUsers(ctx, []*common.User{alice, bob}); err != nil {
		t.Fatalf("SyncUsers after the interleave: %v", err)
	}
	active := back.activeConfig()
	for _, inbound := range active.InboundConfigs {
		if inbound.Tag != inboundTag {
			continue
		}
		inbound.mu.RLock()
		emails := make([]string, 0, len(inbound.clients))
		for email := range inbound.clients {
			emails = append(emails, email)
		}
		inbound.mu.RUnlock()
		slices.Sort(emails)
		if !slices.Equal(emails, []string{"1.alice", "2.bob"}) {
			t.Fatalf("clients on %s = %v, want the last sync's two users", inboundTag, emails)
		}
		return
	}
	t.Fatalf("inbound %q is not in the active config", inboundTag)
}
