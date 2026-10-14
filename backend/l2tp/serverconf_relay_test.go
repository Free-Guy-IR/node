package l2tp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var swanctlGoldenConfigs = []struct {
	golden string
	raw    string
}{
	{"swanctl-default.golden", `{"inbound_tag":"l2tp-de","server_addr":"203.0.113.10","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`},
	{"swanctl-legacy.golden", `{"inbound_tag":"l2tp-legacy","pool":"10.32.0.0/24","psk":"correct-horse-battery","legacy_clients":true}`},
	{"swanctl-custom.golden", `{"inbound_tag":"l2tp-custom","pool":"10.33.0.0/24","psk":"correct-horse-battery","ike_proposals":["AES256-SHA256-MODP2048"," aes128-sha1-modp1024"],"esp_proposals":["aes256-sha256"]}`},
}

func swanctlTestInstance(t *testing.T, raw string) *L2TP {
	t.Helper()
	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig(%s): %v", raw, err)
	}
	return &L2TP{config: cfg}
}

func withRelayIdentities(raw, list string) string {
	return strings.TrimSuffix(raw, "}") + `,"relay_identities":` + list + "}"
}

func readSwanctlGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwanctlWithoutRelayIdentitiesIsByteIdenticalToThePreChangeOutput(t *testing.T) {
	for _, c := range swanctlGoldenConfigs {
		want := readSwanctlGolden(t, c.golden)
		for _, raw := range []string{c.raw, withRelayIdentities(c.raw, "[]"), withRelayIdentities(c.raw, "null")} {
			if got := swanctlTestInstance(t, raw).swanctlConfig(); got != want {
				t.Fatalf("%s from %s: generated swanctl differs from the pre-change output\n--- got ---\n%s\n--- want ---\n%s", c.golden, raw, got, want)
			}
		}
	}
}
