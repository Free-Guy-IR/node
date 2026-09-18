package xray

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const extAssetStartupFailure = "failed to start xray: Failed to start: main: failed to load config files: [stdin:] > " +
	"infra/conf/serial: failed to parse json config > infra/conf: failed to build routing configuration > " +
	"infra/conf: invalid field rule > infra/conf: failed to open file: pgfilter.dat > " +
	"open pgfilter.dat: no such file or directory"

func testConfig(t *testing.T, rules ...string) *Config {
	t.Helper()

	raw := fmt.Sprintf(`{"log":{},"routing":{"rules":[%s]},"inbounds":[],"outbounds":[{"tag":"DIRECT","protocol":"freedom"}]}`,
		strings.Join(rules, ","))

	cfg, err := NewConfig(raw, nil)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	return cfg
}

func ruleTags(t *testing.T, c *Config) []string {
	t.Helper()

	tags := make([]string, 0, len(c.RouterConfig.RuleList))
	for _, raw := range c.RouterConfig.RuleList {
		tags = append(tags, routingRuleTag(raw))
	}

	return tags
}

func TestIsContentFilterRuleTag(t *testing.T) {
	owned := []string{
		"pgcf-9-block",
		"pgcf-12-allow-0123456789-abcdef01",
		"pgcf-70-cat-aaaaaaaaaa-bbbbbbbb",
		"pgcf-3-strict",
	}
	for _, tag := range owned {
		if !isContentFilterRuleTag(tag) {
			t.Errorf("isContentFilterRuleTag(%q) = false, want true", tag)
		}
	}

	foreign := []string{
		"",
		"pgcf",
		"pgcf-legacy-manual",
		"pgcf-9",
		"pgcf-9-unknown",
		"pgcf-x-block",
		"operator-block-ads",
		"PG_NODE_MALFORMED_DOMAIN_GUARD",
		"pgcf-9-block-short",
	}
	for _, tag := range foreign {
		if isContentFilterRuleTag(tag) {
			t.Errorf("isContentFilterRuleTag(%q) = true, want false", tag)
		}
	}
}

func TestStripContentFilterRulesKeepsOperatorRules(t *testing.T) {
	cfg := testConfig(t,
		`{"ruleTag":"operator-direct","outboundTag":"DIRECT","type":"field"}`,
		`{"ruleTag":"pgcf-9-block-aaaaaaaaaa-bbbbbbbb","outboundTag":"BLOCK","type":"field","domain":["ext:pgfilter.dat:pglist-1"]}`,
		`{"ruleTag":"pgcf-9-allow","outboundTag":"DIRECT","type":"field"}`,
		`{"ruleTag":"pgcf-legacy-manual","outboundTag":"DIRECT","type":"field"}`,
		`{"outboundTag":"DIRECT","type":"field"}`,
	)

	removed := stripContentFilterRules(cfg)
	want := []string{"pgcf-9-block-aaaaaaaaaa-bbbbbbbb", "pgcf-9-allow"}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("stripContentFilterRules() = %v, want %v", removed, want)
	}

	gotTags := ruleTags(t, cfg)
	wantTags := []string{"operator-direct", "pgcf-legacy-manual", ""}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Fatalf("remaining rules = %v, want %v", gotTags, wantTags)
	}
}

func TestStripContentFilterRulesWithoutOwnedRules(t *testing.T) {
	cfg := testConfig(t, `{"ruleTag":"operator-direct","outboundTag":"DIRECT","type":"field"}`)

	if removed := stripContentFilterRules(cfg); removed != nil {
		t.Fatalf("stripContentFilterRules() = %v, want nil", removed)
	}
	if len(cfg.RouterConfig.RuleList) != 1 {
		t.Fatalf("rule list length = %d, want 1", len(cfg.RouterConfig.RuleList))
	}
}

func TestStartupFailureIsRuleRelated(t *testing.T) {
	ruleRelated := []string{
		extAssetStartupFailure,
		"failed to start xray: Failed to start: main: failed to load config files: [stdin:] > infra/conf/serial: " +
			"failed to parse json config > infra/conf: failed to build routing configuration > infra/conf: " +
			"invalid field rule > infra/conf: failed to load geosite: category-ads-all",
	}
	for _, failure := range ruleRelated {
		if !startupFailureIsRuleRelated(failure) {
			t.Errorf("startupFailureIsRuleRelated(%q) = false, want true", failure)
		}
	}

	unrelated := []string{
		"",
		"failed to start xray: Failed to start: main: failed to load config files: [stdin:] > infra/conf/serial: " +
			"failed to parse json config > infra/conf: failed to build inbound handler config",
		"failed to start xray: [Fatal] app/proxyman/inbound: failed to listen TCP on 443 > address already in use",
		"failed to start xray: infra/conf: failed to build DNS configuration > invalid nameserver",
		"xray version probe did not finish: context deadline exceeded",
		"xray process stopped before API became ready; no fatal xray startup log was detected. Recent xray logs:\n" +
			"xray process exited unexpectedly: signal: killed (process was killed externally; check container/system OOM and memory limits)",
		"fatal error: runtime: out of memory",
	}
	for _, failure := range unrelated {
		if startupFailureIsRuleRelated(failure) {
			t.Errorf("startupFailureIsRuleRelated(%q) = true, want false", failure)
		}
	}
}

type recordingStarter struct {
	attempts   []*Config
	stops      int
	failWhen   func(*Config) error
	afterFirst error
}

func (r *recordingStarter) start(_ context.Context, c *Config) error {
	r.attempts = append(r.attempts, c)
	return r.failWhen(c)
}

func (r *recordingStarter) stop() {
	r.stops++
}

func failWhileFiltered(failure error) func(*Config) error {
	return func(c *Config) error {
		if len(contentFilterRuleTags(c)) > 0 {
			return failure
		}
		return nil
	}
}

func filteredConfig(t *testing.T) *Config {
	t.Helper()

	return testConfig(t,
		`{"ruleTag":"operator-direct","outboundTag":"DIRECT","type":"field"}`,
		`{"ruleTag":"pgcf-9-block-aaaaaaaaaa-bbbbbbbb","outboundTag":"BLOCK","type":"field","domain":["ext:pgfilter.dat:pglist-1"]}`,
	)
}

func TestStartCoreWithFilterFallbackDegradesOnRuleRelatedFailure(t *testing.T) {
	cfg := filteredConfig(t)
	starter := &recordingStarter{failWhen: failWhileFiltered(errors.New(extAssetStartupFailure))}

	x := &Xray{}
	if err := x.startCoreWithFilterFallback(context.Background(), cfg, starter.start, starter.stop); err != nil {
		t.Fatalf("startCoreWithFilterFallback() = %v, want nil", err)
	}

	if len(starter.attempts) != 2 {
		t.Fatalf("start attempts = %d, want 2", len(starter.attempts))
	}
	if starter.stops != 1 {
		t.Fatalf("core stops before retry = %d, want 1", starter.stops)
	}

	degradation := x.StartupDegradation()
	if !degradation.FilterRulesStripped {
		t.Fatal("FilterRulesStripped = false, want true")
	}
	if degradation.Reason != extAssetStartupFailure {
		t.Fatalf("Reason = %q, want %q", degradation.Reason, extAssetStartupFailure)
	}
	want := []string{"pgcf-9-block-aaaaaaaaaa-bbbbbbbb"}
	if !reflect.DeepEqual(degradation.StrippedRuleTags, want) {
		t.Fatalf("StrippedRuleTags = %v, want %v", degradation.StrippedRuleTags, want)
	}

	active := x.activeConfig()
	if tags := contentFilterRuleTags(active); len(tags) != 0 {
		t.Fatalf("active config still carries content-filter rules: %v", tags)
	}
	if got := ruleTags(t, active); !reflect.DeepEqual(got, []string{"operator-direct"}) {
		t.Fatalf("active config rules = %v, want [operator-direct]", got)
	}
	if tags := contentFilterRuleTags(cfg); len(tags) != 1 {
		t.Fatalf("original config was mutated: %v", tags)
	}
}

func TestStartCoreWithFilterFallbackKeepsUnrelatedFailure(t *testing.T) {
	cfg := filteredConfig(t)
	failure := errors.New("failed to start xray: [Fatal] app/proxyman/inbound: failed to listen TCP on 443 > address already in use")
	starter := &recordingStarter{failWhen: func(*Config) error { return failure }}

	x := &Xray{}
	err := x.startCoreWithFilterFallback(context.Background(), cfg, starter.start, starter.stop)
	if !errors.Is(err, failure) {
		t.Fatalf("startCoreWithFilterFallback() = %v, want %v", err, failure)
	}

	if len(starter.attempts) != 1 {
		t.Fatalf("start attempts = %d, want 1", len(starter.attempts))
	}
	if starter.stops != 0 {
		t.Fatalf("core stops = %d, want 0", starter.stops)
	}
	if x.StartupDegradation().FilterRulesStripped {
		t.Fatal("FilterRulesStripped = true, want false")
	}
}

func TestStartCoreWithFilterFallbackFailsWhenStrippedRetryAlsoFails(t *testing.T) {
	cfg := filteredConfig(t)
	original := errors.New(extAssetStartupFailure)
	retry := errors.New("failed to start xray: [Fatal] app/proxyman/inbound: failed to listen TCP on 443 > address already in use")
	starter := &recordingStarter{failWhen: func(c *Config) error {
		if len(contentFilterRuleTags(c)) > 0 {
			return original
		}
		return retry
	}}

	x := &Xray{}
	err := x.startCoreWithFilterFallback(context.Background(), cfg, starter.start, starter.stop)
	if !errors.Is(err, original) {
		t.Fatalf("startCoreWithFilterFallback() = %v, want %v", err, original)
	}

	if len(starter.attempts) != 2 {
		t.Fatalf("start attempts = %d, want 2", len(starter.attempts))
	}
	if x.StartupDegradation().FilterRulesStripped {
		t.Fatal("FilterRulesStripped = true, want false")
	}
}

func TestStartCoreWithFilterFallbackWithoutContentFilterRules(t *testing.T) {
	cfg := testConfig(t, `{"ruleTag":"operator-direct","outboundTag":"DIRECT","type":"field"}`)
	failure := errors.New(extAssetStartupFailure)
	starter := &recordingStarter{failWhen: func(*Config) error { return failure }}

	x := &Xray{}
	err := x.startCoreWithFilterFallback(context.Background(), cfg, starter.start, starter.stop)
	if !errors.Is(err, failure) {
		t.Fatalf("startCoreWithFilterFallback() = %v, want %v", err, failure)
	}
	if len(starter.attempts) != 1 {
		t.Fatalf("start attempts = %d, want 1", len(starter.attempts))
	}
	if x.StartupDegradation().FilterRulesStripped {
		t.Fatal("FilterRulesStripped = true, want false")
	}
}

func TestStartCoreWithFilterFallbackCleanStartReportsNoDegradation(t *testing.T) {
	cfg := filteredConfig(t)
	starter := &recordingStarter{failWhen: func(*Config) error { return nil }}

	x := &Xray{}
	if err := x.startCoreWithFilterFallback(context.Background(), cfg, starter.start, starter.stop); err != nil {
		t.Fatalf("startCoreWithFilterFallback() = %v, want nil", err)
	}
	if len(starter.attempts) != 1 {
		t.Fatalf("start attempts = %d, want 1", len(starter.attempts))
	}

	degradation := x.StartupDegradation()
	if degradation.FilterRulesStripped || degradation.Reason != "" || len(degradation.StrippedRuleTags) != 0 {
		t.Fatalf("StartupDegradation() = %+v, want zero value", degradation)
	}
	if tags := contentFilterRuleTags(x.activeConfig()); len(tags) != 1 {
		t.Fatalf("active config content-filter rules = %v, want the rules it was given", tags)
	}
}

func TestStartupFailureIsResourceExhaustion(t *testing.T) {
	exhausted := []string{
		"failed to start xray: signal: killed",
		"Failed to start: runtime: out of memory",
		"fork/exec: cannot allocate memory",
	}
	for _, failure := range exhausted {
		if !startupFailureIsResourceExhaustion(failure) {
			t.Fatalf("expected resource exhaustion for %q", failure)
		}
	}

	if startupFailureIsResourceExhaustion(extAssetStartupFailure) {
		t.Fatal("an asset failure is not resource exhaustion")
	}
}

func TestAssetFailureNeedsRoutingContext(t *testing.T) {
	withoutRoutingContext := []string{
		"failed to start xray: failed to open file: geoip.dat",
		"failed to start xray: infra/conf: failed to build DNS configuration > failed to load geosite: cn",
		"failed to start xray: infra/conf: failed to build DNS configuration > failed to load geoip: private",
		"failed to start xray: infra/conf: failed to build DNS configuration > failed to parse domain rule",
	}
	for _, failure := range withoutRoutingContext {
		if startupFailureIsRuleRelated(failure) {
			t.Errorf("a shared asset failure without routing context must not be blamed on filter rules: %q", failure)
		}
	}

	withRoutingContext := []string{
		extAssetStartupFailure,
		"failed to start xray: infra/conf: failed to build routing configuration > failed to load geosite: category-ads-all",
	}
	for _, failure := range withRoutingContext {
		if !startupFailureIsRuleRelated(failure) {
			t.Errorf("a routing-context failure must be recognised: %q", failure)
		}
	}
}

func TestTransientResourceExhaustionKeepsFilterRules(t *testing.T) {
	config := testConfig(t, `{"ruleTag":"pgcf-9-block","outboundTag":"BLOCK","domain":["example.com"]}`)

	attempts := 0
	start := func(_ context.Context, c *Config) error {
		attempts++
		if attempts == 1 {
			return errors.New("failed to start xray: signal: killed")
		}
		if len(contentFilterRuleTags(c)) == 0 {
			t.Fatal("the retry must keep the filter rules")
		}
		return nil
	}

	x := &Xray{}
	if err := x.startCoreWithFilterFallback(context.Background(), config, start, nil); err != nil {
		t.Fatalf("expected the unchanged retry to succeed: %v", err)
	}

	if attempts != 2 {
		t.Fatalf("expected exactly one unchanged retry, got %d attempts", attempts)
	}

	if x.StartupDegradation().FilterRulesStripped {
		t.Fatal("a transient resource failure must not report stripped filter rules")
	}
}

func TestPersistentResourceExhaustionStillStripsFilterRules(t *testing.T) {
	config := testConfig(t, `{"ruleTag":"pgcf-9-block","outboundTag":"BLOCK","domain":["example.com"]}`)

	start := func(_ context.Context, c *Config) error {
		if len(contentFilterRuleTags(c)) > 0 {
			return errors.New("failed to start xray: signal: killed")
		}
		return nil
	}

	x := &Xray{}
	if err := x.startCoreWithFilterFallback(context.Background(), config, start, nil); err != nil {
		t.Fatalf("expected the stripped retry to succeed: %v", err)
	}

	degradation := x.StartupDegradation()
	if !degradation.FilterRulesStripped {
		t.Fatal("a filter that keeps starving the core must be stripped and reported")
	}
	if !reflect.DeepEqual(degradation.StrippedRuleTags, []string{"pgcf-9-block"}) {
		t.Fatalf("unexpected stripped tags: %v", degradation.StrippedRuleTags)
	}
}

func TestUnchangedRetryFailingForAnUnrelatedReasonKeepsFilterRules(t *testing.T) {
	config := testConfig(t, `{"ruleTag":"pgcf-9-block","outboundTag":"BLOCK","domain":["example.com"]}`)

	attempts := 0
	start := func(_ context.Context, c *Config) error {
		attempts++
		if attempts == 1 {
			return errors.New("failed to start xray: signal: killed")
		}
		return errors.New("failed to start xray: [Fatal] app/proxyman/inbound: failed to listen TCP on 443 > address already in use")
	}

	x := &Xray{}
	err := x.startCoreWithFilterFallback(context.Background(), config, start, nil)
	if err == nil {
		t.Fatal("an unrelated retry failure must not be swallowed")
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("the reported failure must be the one that actually happened, got: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the rules must not be stripped, so exactly two attempts were expected, got %d", attempts)
	}
	if x.StartupDegradation().FilterRulesStripped {
		t.Fatal("no filter rule may be blamed for a port collision")
	}
}

func TestDomainParsingMessagesNeedRoutingContext(t *testing.T) {
	shared := []string{
		"failed to start xray: infra/conf: failed to build DNS configuration > empty country name in rule",
		"failed to start xray: infra/conf: failed to build DNS configuration > empty filename or empty country in rule",
		"failed to start xray: infra/conf: failed to build DNS configuration > substr in dotless rule should not contain a dot",
	}
	for _, failure := range shared {
		if startupFailureIsRuleRelated(failure) {
			t.Errorf("a domain-parsing message from DNS must not be blamed on filter rules: %q", failure)
		}
	}

	if !startupFailureIsRuleRelated("failed to start xray: infra/conf: failed to build routing configuration > empty country name in rule") {
		t.Fatal("the same message with routing context must be recognised")
	}
}
