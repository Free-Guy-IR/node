package controller

import (
	"reflect"
	"testing"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
)

type degradableBackend struct {
	backend.Backend
	degradation backend.StartupDegradation
}

func (d *degradableBackend) Started() bool   { return true }
func (d *degradableBackend) Version() string { return "25.9.11" }

func (d *degradableBackend) StartupDegradation() backend.StartupDegradation {
	return d.degradation
}

type plainBackend struct {
	backend.Backend
}

func (p *plainBackend) Started() bool   { return true }
func (p *plainBackend) Version() string { return "25.9.11" }

func TestBaseInfoResponseReportsStrippedFilterRules(t *testing.T) {
	reason := "failed to start xray: Failed to start: main: failed to load config files: [stdin:] > " +
		"infra/conf: failed to build routing configuration > infra/conf: failed to open file: pgfilter.dat"
	tags := []string{"pgcf-9-block-aaaaaaaaaa-bbbbbbbb"}

	c := &Controller{
		backend: &degradableBackend{degradation: backend.StartupDegradation{
			FilterRulesStripped: true,
			Reason:              reason,
			StrippedRuleTags:    tags,
		}},
		primaryType: common.BackendType_XRAY,
	}

	response := c.BaseInfoResponse()

	if !response.GetStarted() {
		t.Fatal("Started = false, want true")
	}
	if !response.GetFilterRulesStripped() {
		t.Fatal("FilterRulesStripped = false, want true")
	}
	if response.GetFilterStripReason() != reason {
		t.Fatalf("FilterStripReason = %q, want %q", response.GetFilterStripReason(), reason)
	}
	if !reflect.DeepEqual(response.GetStrippedRuleTags(), tags) {
		t.Fatalf("StrippedRuleTags = %v, want %v", response.GetStrippedRuleTags(), tags)
	}
	if response.GetNodeVersion() != NodeVersion {
		t.Fatalf("NodeVersion = %q, want %q", response.GetNodeVersion(), NodeVersion)
	}
}

func TestBaseInfoResponseWithoutDegradation(t *testing.T) {
	c := &Controller{backend: &plainBackend{}, primaryType: common.BackendType_XRAY}

	response := c.BaseInfoResponse()

	if response.GetFilterRulesStripped() {
		t.Fatal("FilterRulesStripped = true, want false")
	}
	if response.GetFilterStripReason() != "" {
		t.Fatalf("FilterStripReason = %q, want empty", response.GetFilterStripReason())
	}
	if len(response.GetStrippedRuleTags()) != 0 {
		t.Fatalf("StrippedRuleTags = %v, want empty", response.GetStrippedRuleTags())
	}
}
