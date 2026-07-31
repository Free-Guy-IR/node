// Package mtproto implements backend.Backend on top of a forked mtg
// (github.com/Free-Guy-IR/mtg, see mtglib.ProxyOpts.Secrets) MTProto proxy
// library. Unlike every other backend in this repo, MTProto does not spawn
// one OS process per admin-configured instance (OpenVPN) or one process per
// individual user - the official/upstream MTProto proxy implementations
// (both the C reference implementation and upstream mtg) report only
// whole-process aggregate byte counters, with no per-secret breakdown at
// all, which is incompatible with this panel's per-user usage/quota model.
//
// Instead, each configured instance runs ONE mtglib.Proxy in-process
// (goroutines, not subprocesses) accepting many users multiplexed by their
// individual secret - exactly like Xray/sing-box's "one process, N users"
// shape, not OpenVPN's "one process per instance, N users per instance
// sharing that instance's socket" shape. Per-user attribution comes from the
// forked mtglib's EventAuthenticated/EventTraffic events (see stats.go),
// which the upstream project does not emit.
package mtproto

import (
	"encoding/json"
	"fmt"
)

// InstanceConfig is one admin-configured MTProto listener: a port plus the
// fake-TLS domain fronting host every user connecting to it is validated
// against. There is no per-instance PKI (MTProto secrets are symmetric, not
// certificate-based) and no protocol/network choice (mtg is TCP-only).
type InstanceConfig struct {
	Tag           string `json:"tag"`
	Port          int    `json:"port"`
	FakeTLSDomain string `json:"fake_tls_domain"`
}

type Config struct {
	Instances []*InstanceConfig `json:"instances"`
}

func NewConfig(raw string) (*Config, error) {
	cfg := &Config{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("mtproto: cannot parse config: %w", err)
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validate(cfg *Config) error {
	if len(cfg.Instances) == 0 {
		return fmt.Errorf("mtproto config: at least one instance is required")
	}

	tags := make(map[string]struct{}, len(cfg.Instances))
	ports := make(map[int]struct{}, len(cfg.Instances))

	for _, inst := range cfg.Instances {
		if inst.Tag == "" {
			return fmt.Errorf("mtproto config: instance tag must not be empty")
		}
		if _, dup := tags[inst.Tag]; dup {
			return fmt.Errorf("mtproto config: duplicate instance tag %q", inst.Tag)
		}
		tags[inst.Tag] = struct{}{}

		if inst.Port <= 0 || inst.Port > 65535 {
			return fmt.Errorf("mtproto config: instance %q has invalid port %d", inst.Tag, inst.Port)
		}
		if _, dup := ports[inst.Port]; dup {
			return fmt.Errorf("mtproto config: duplicate port %d", inst.Port)
		}
		ports[inst.Port] = struct{}{}

		if inst.FakeTLSDomain == "" {
			return fmt.Errorf("mtproto config: instance %q must set fake_tls_domain", inst.Tag)
		}
	}

	return nil
}
