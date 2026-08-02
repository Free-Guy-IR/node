package singbox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Config wraps a sing-box JSON configuration tree.
//
// Scope note (v1): unlike xray's backend/xray/config.go, which understands every
// xray inbound protocol and marshals typed client accounts, this Config only
// understands sing-box inbounds of "type": "hysteria2". Any other inbound/outbound
// or top-level key (log, dns, route, outbounds, endpoints, ...) supplied in the
// raw config is passed through untouched via the generic `root` map. This matches
// the requested v1 scope of "Hysteria2 only, not a generic multi-protocol
// resolver".
//
// Concurrency note: a single sync.Mutex (not xray's per-inbound RWMutex) guards
// the whole tree. This config is only touched around restarts/syncs (not a
// per-request hot path), so the simpler single-lock model is sufficient and
// easier to reason about.
type Config struct {
	mu      sync.Mutex
	root    map[string]any
	hy2     map[string]*hysteria2Inbound // tag -> inbound
	apiPort int                          // 0 until ApplyAPI is called
}

type hysteria2Inbound struct {
	tag   string
	obj   map[string]any          // reference into root["inbounds"][i]; mutated in place
	users map[string]hy2UserEntry // email -> entry, source of truth for this inbound's "users" array
}

type hy2UserEntry struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (h *hysteria2Inbound) sortedUsers() []hy2UserEntry {
	names := make([]string, 0, len(h.users))
	for name := range h.users {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]hy2UserEntry, 0, len(names))
	for _, name := range names {
		out = append(out, h.users[name])
	}
	return out
}

// NewConfig parses a raw sing-box JSON config and indexes every "hysteria2"
// inbound found in it by tag, so users can later be synced in.
func NewConfig(raw string) (*Config, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("parse sing-box config: %w", err)
	}

	cfg := &Config{
		root: root,
		hy2:  make(map[string]*hysteria2Inbound),
	}

	if err := cfg.indexHysteria2Inbounds(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) indexHysteria2Inbounds() error {
	inboundsRaw, ok := c.root["inbounds"]
	if !ok {
		// No inbounds at all is unusual for a node config but not fatal at parse
		// time - surfaces naturally later (no hysteria2 inbound to sync users into).
		return nil
	}

	inbounds, ok := inboundsRaw.([]any)
	if !ok {
		return fmt.Errorf("sing-box config: \"inbounds\" must be an array")
	}

	for idx, raw := range inbounds {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		typ, _ := obj["type"].(string)
		if typ != "hysteria2" {
			continue
		}

		tag, _ := obj["tag"].(string)
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return fmt.Errorf("sing-box config: hysteria2 inbound at index %d is missing a \"tag\"", idx)
		}
		if _, dup := c.hy2[tag]; dup {
			return fmt.Errorf("sing-box config: duplicate hysteria2 inbound tag %q", tag)
		}

		entry := &hysteria2Inbound{
			tag:   tag,
			obj:   obj,
			users: make(map[string]hy2UserEntry),
		}

		// Seed from any users already present in the admin-supplied template, in
		// case it wasn't empty (defensive - normally the panel sends an empty list
		// and relies on us to inject users).
		if existing, ok := obj["users"].([]any); ok {
			for _, raw := range existing {
				um, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				name, _ := um["name"].(string)
				password, _ := um["password"].(string)
				if name == "" {
					continue
				}
				entry.users[name] = hy2UserEntry{Name: name, Password: password}
			}
		}

		c.hy2[tag] = entry
	}

	return nil
}

// ApplyAPI records the local v2ray_api stats port and injects/refreshes the
// "experimental.v2ray_api" block. It mirrors xray's Config.ApplyAPI entry point:
// called once up front after parsing, before the first Start().
func (c *Config) ApplyAPI(port int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.apiPort = port
	return c.refreshAPILocked()
}

// refreshAPILocked (re)writes the experimental.v2ray_api block so that its
// "stats.users" list always reflects every user email currently known across all
// hysteria2 inbounds. This matters because sing-box's v2ray_api only creates a
// per-user counter for names present in "stats.users" *at process start* - unlike
// xray there is no live "add user" API call, so this must be current every time
// we are about to (re)start the process. Called from ApplyAPI and from ToBytes.
func (c *Config) refreshAPILocked() error {
	if c.apiPort == 0 {
		return nil
	}

	experimental, _ := c.root["experimental"].(map[string]any)
	if experimental == nil {
		experimental = make(map[string]any)
	}

	experimental["v2ray_api"] = map[string]any{
		"listen": fmt.Sprintf("127.0.0.1:%d", c.apiPort),
		"stats": map[string]any{
			"enabled": true,
			// Without this, sing-box's v2ray_api.StatsService builds its
			// inbounds map from an empty options.Inbounds list (see
			// NewStatsService in sing-box's experimental/v2rayapi/stats.go),
			// so RoutedConnection's countInbound check is always false and
			// no per-inbound counter is ever created - GetInboundsStats/
			// GetInboundStats (backend/singbox/api/stats.go) would then
			// always return empty regardless of how much real traffic
			// flows. Listing every hysteria2 tag here is what makes those
			// two RPCs (which backend/singbox/stats.go's GetStats now
			// delegates Outbound/Outbounds to, since this backend's
			// minimal direct-outbound config has no traffic-bearing
			// "outbound" of its own to track) return real, non-zero data.
			"inbounds": c.allInboundTagsLocked(),
			"users":    c.allUserEmailsLocked(),
		},
	}
	c.root["experimental"] = experimental

	return nil
}

func (c *Config) allInboundTagsLocked() []string {
	tags := make([]string, 0, len(c.hy2))
	for tag := range c.hy2 {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func (c *Config) allUserEmailsLocked() []string {
	seen := make(map[string]struct{})
	for _, inbound := range c.hy2 {
		for email := range inbound.users {
			seen[email] = struct{}{}
		}
	}

	emails := make([]string, 0, len(seen))
	for email := range seen {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	return emails
}

// ToBytes rebuilds each hysteria2 inbound's "users" array and the
// experimental.v2ray_api "stats.users" list from current state, then marshals
// the whole tree as pretty-printed JSON ready to write to the config file
// sing-box is started with.
func (c *Config) ToBytes() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, inbound := range c.hy2 {
		inbound.obj["users"] = inbound.sortedUsers()
	}
	if err := c.refreshAPILocked(); err != nil {
		return nil, err
	}

	return json.MarshalIndent(c.root, "", "    ")
}

// Clone returns an independent deep copy of the config (including the current
// in-memory user state), obtained by round-tripping through JSON and
// re-indexing - mirrors xray's Config.Clone, reused by the
// apply-candidate-then-restart-with-rollback pattern in user.go.
func (c *Config) Clone() (*Config, error) {
	if c == nil {
		return nil, fmt.Errorf("sing-box config is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, inbound := range c.hy2 {
		inbound.obj["users"] = inbound.sortedUsers()
	}
	if err := c.refreshAPILocked(); err != nil {
		return nil, err
	}

	data, err := json.Marshal(c.root)
	if err != nil {
		return nil, fmt.Errorf("marshal sing-box config clone: %w", err)
	}

	cloned, err := NewConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("unmarshal sing-box config clone: %w", err)
	}
	cloned.apiPort = c.apiPort

	return cloned, nil
}

// HasHysteria2Inbounds reports whether the config indexed at least one
// "hysteria2" inbound. Useful for fast-failing at startup with a clear error
// instead of silently starting a sing-box process that can never carry traffic
// for any user.
func (c *Config) HasHysteria2Inbounds() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.hy2) > 0
}
