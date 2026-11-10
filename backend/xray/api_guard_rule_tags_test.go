package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pasarguard/node/pkg/fsutil"
)

func ruleTagCounts(t *testing.T, cfg *Config) map[string]int {
	t.Helper()

	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}

	var probe struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}

	counts := make(map[string]int, len(probe.Routing.Rules))
	for _, rule := range probe.Routing.Rules {
		if tag, ok := rule["ruleTag"].(string); ok && tag != "" {
			counts[tag]++
		}
	}
	return counts
}

func duplicateRuleTags(counts map[string]int) []string {
	var dups []string
	for tag, n := range counts {
		if n > 1 {
			dups = append(dups, fmt.Sprintf("%s appears %d times", tag, n))
		}
	}
	sort.Strings(dups)
	return dups
}

func TestApplyAPIGivesEveryGuardRuleItsOwnRuleTag(t *testing.T) {
	const (
		apiPort    = 48101
		metricPort = 48102
	)

	for name, routing := range map[string]panelRouting{
		"noPanelRules":        panelNoRules,
		"panelCatchAllDirect": panelCatchAllDirect,
		"panelPrivateBlock":   panelPrivateBlock,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loopbackProbeConfig(t, routing)
			for range 3 {
				if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
					t.Fatal(err)
				}
			}

			counts := ruleTagCounts(t, cfg)
			if dups := duplicateRuleTags(counts); len(dups) > 0 {
				t.Fatalf("xray refuses a config that reuses a ruleTag; generated config has duplicates: %s", strings.Join(dups, "; "))
			}

			for _, tag := range []string{
				apiGuardIPRuleTag,
				apiGuardDomainRuleTag,
				siblingGuardIPRuleTag,
				siblingGuardDomainRuleTag,
			} {
				if counts[tag] != 1 {
					t.Errorf("ruleTag %q appears %d times, want exactly 1", tag, counts[tag])
				}
			}
			if counts[apiGuardRuleTag] != 0 {
				t.Errorf("the shared legacy ruleTag %q is still emitted %d times", apiGuardRuleTag, counts[apiGuardRuleTag])
			}
			if counts[siblingGuardRuleTag] != 0 {
				t.Errorf("the shared legacy ruleTag %q is still emitted %d times", siblingGuardRuleTag, counts[siblingGuardRuleTag])
			}
		})
	}
}

func TestApplyAPIStripsAPanelSuppliedGuardRuleTag(t *testing.T) {
	const (
		apiPort    = 48103
		metricPort = 48104
	)

	cfg := loopbackProbeConfig(t, panelCatchAllDirect)
	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	for _, tag := range []string{
		apiGuardRuleTag,
		apiGuardIPRuleTag,
		apiGuardDomainRuleTag,
		siblingGuardRuleTag,
		siblingGuardIPRuleTag,
		siblingGuardDomainRuleTag,
		malformedDomainGuardRuleTag,
	} {
		planted, err := json.Marshal(map[string]any{
			"type":        "field",
			"ruleTag":     tag,
			"ip":          []string{"0.0.0.0/0"},
			"outboundTag": "DIRECT",
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg.RouterConfig.RuleList = append(cfg.RouterConfig.RuleList, json.RawMessage(planted))
	}

	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	counts := ruleTagCounts(t, cfg)
	if dups := duplicateRuleTags(counts); len(dups) > 0 {
		t.Fatalf("a panel-supplied guard ruleTag survived ApplyAPI: %s", strings.Join(dups, "; "))
	}

	for _, raw := range cfg.RouterConfig.RuleList {
		var rule struct {
			RuleTag     string `json:"ruleTag"`
			OutboundTag string `json:"outboundTag"`
		}
		if err := json.Unmarshal(raw, &rule); err != nil {
			t.Fatal(err)
		}
		if nodeGuardRuleTag(rule.RuleTag) && rule.OutboundTag == "DIRECT" {
			t.Errorf("the planted rule %q survived ApplyAPI pointing at %q", rule.RuleTag, rule.OutboundTag)
		}
	}
}

func assertRealXrayAccepts(t *testing.T, cfg *Config) {
	t.Helper()

	binary := executablePath
	if env := os.Getenv("XRAY_EXECUTABLE_PATH"); env != "" {
		binary = env
	}
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("no xray binary at %s", binary)
	}

	generated, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, generated, 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := exec.Command(binary, "run", "-c", path, "-test").CombinedOutput()
	if err != nil {
		t.Fatalf("%s refused the generated config: %v\n%s", binary, err, output)
	}
}

func TestTheRealXrayAcceptsTheGeneratedGuardedConfig(t *testing.T) {
	cfg := loopbackProbeConfig(t, panelCatchAllDirect)
	if err := cfg.ApplyAPI(48105, 48106); err != nil {
		t.Fatal(err)
	}
	assertRealXrayAccepts(t, cfg)
}

func TestTheRealXrayAcceptsTheShippedSampleConfig(t *testing.T) {
	raw, err := fsutil.ReadFileAsString(jsonFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := NewConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyAPI(48107, 48108); err != nil {
		t.Fatal(err)
	}
	assertRealXrayAccepts(t, cfg)
}
