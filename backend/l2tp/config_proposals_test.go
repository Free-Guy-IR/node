package l2tp

import (
	"regexp"
	"slices"
	"testing"
)

var espDHGroupRe = regexp.MustCompile(`-(modp[0-9]+|ecp[0-9]+|curve25519|curve448|x25519|x448)$`)

const (
	defaultProposalsConfig = `{"inbound_tag":"l2tp-de","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`
	legacyProposalsConfig  = `{"inbound_tag":"l2tp-legacy","pool":"10.32.0.0/24","psk":"correct-horse-battery","legacy_clients":true}`
)

func espProposalsOf(t *testing.T, raw string) []string {
	t.Helper()
	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig(%s): %v", raw, err)
	}
	return cfg.ESPProposals
}

func TestDefaultESPProposalsAcceptPFSClientsAndKeepNonPFSFirst(t *testing.T) {
	esp := espProposalsOf(t, defaultProposalsConfig)
	firstPFS := slices.IndexFunc(esp, espDHGroupRe.MatchString)
	if firstPFS < 0 {
		t.Fatalf("no default ESP proposal carries a DH group, so Quick Mode from a pfs=yes client gets NO_PROPOSAL_CHOSEN: %v", esp)
	}
	if firstPFS == 0 {
		t.Fatalf("the default ESP proposals must start with the non-PFS ones: %v", esp)
	}
	for _, p := range esp[firstPFS:] {
		if !espDHGroupRe.MatchString(p) {
			t.Fatalf("non-PFS proposal %q comes after a PFS one: %v", p, esp)
		}
	}
	want := []string{
		"aes256-sha256", "aes256-sha1", "aes128-sha1",
		"aes256-sha256-modp2048", "aes256-sha1-modp2048", "aes128-sha1-modp2048",
		"aes256-sha1-modp1536", "aes128-sha1-modp1536",
		"aes256-sha1-modp1024", "aes128-sha1-modp1024",
	}
	if !slices.Equal(esp, want) {
		t.Fatalf("default ESP proposals = %v, want the list proven in production %v", esp, want)
	}
}

func TestLegacyESPProposalsAppendTheThreeDESVariantsLast(t *testing.T) {
	want := append(espProposalsOf(t, defaultProposalsConfig), "3des-sha1", "3des-sha1-modp1024")
	if got := espProposalsOf(t, legacyProposalsConfig); !slices.Equal(got, want) {
		t.Fatalf("legacy ESP proposals = %v, want %v", got, want)
	}
}

func TestExplicitESPProposalsAreKeptAsGiven(t *testing.T) {
	got := espProposalsOf(t, `{"inbound_tag":"l2tp-custom","pool":"10.33.0.0/24","psk":"correct-horse-battery","esp_proposals":["aes256-sha256"]}`)
	if !slices.Equal(got, []string{"aes256-sha256"}) {
		t.Fatalf("explicit esp_proposals = %v, want [aes256-sha256]", got)
	}
}

func TestLegacyClientsLeaveAnExplicitProposalListAsGiven(t *testing.T) {
	defaulted, err := NewConfig(legacyProposalsConfig)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = `{"inbound_tag":"l2tp-custom","pool":"10.33.0.0/24","psk":"correct-horse-battery","legacy_clients":true`
	cases := []struct {
		raw      string
		ike, esp []string
	}{
		{prefix + `,"esp_proposals":["aes256-sha256"]}`, defaulted.IKEProposals, []string{"aes256-sha256"}},
		{prefix + `,"ike_proposals":["aes256-sha256-modp2048"]}`, []string{"aes256-sha256-modp2048"}, defaulted.ESPProposals},
		{prefix + `,"ike_proposals":["aes256-sha256-modp2048"],"esp_proposals":["aes256-sha256"]}`, []string{"aes256-sha256-modp2048"}, []string{"aes256-sha256"}},
	}
	for _, c := range cases {
		cfg, err := NewConfig(c.raw)
		if err != nil {
			t.Fatalf("NewConfig(%s): %v", c.raw, err)
		}
		if !slices.Equal(cfg.IKEProposals, c.ike) || !slices.Equal(cfg.ESPProposals, c.esp) {
			t.Fatalf("%s: ike_proposals = %v, esp_proposals = %v; want %v and %v, since legacy_clients adds 3DES only to a list left to the defaults", c.raw, cfg.IKEProposals, cfg.ESPProposals, c.ike, c.esp)
		}
	}
}
