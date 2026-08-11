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
// Scope note: this Config understands sing-box inbounds of the proxy types
// listed in supportedInboundTypes ("hysteria2", "vless", "vmess", "trojan",
// "shadowsocks", "tuic"). For each such inbound it manages the per-user credential
// array ("users"). Any other inbound/outbound or top-level key (log, dns,
// route, outbounds, endpoints, ...) supplied in the raw config is passed
// through untouched via the generic `root` map. Unlike xray's backend, this is
// not a full generic multi-protocol resolver: it only injects the per-user
// account objects each supported inbound type needs, keyed by inbound tag.
//
// Concurrency note: a single sync.Mutex (not xray's per-inbound RWMutex) guards
// the whole tree. This config is only touched around restarts/syncs (not a
// per-request hot path), so the simpler single-lock model is sufficient and
// easier to reason about.
type Config struct {
	mu        sync.Mutex
	root      map[string]any
	inbounds  map[string]*protoInbound // tag -> inbound (any supported proxy type)
	apiPort   int                      // 0 until ApplyAPI is called
	clashPort int                      // local clash_api controller port, for live user hot-reload; 0 until ApplyAPI
}

// supportedInboundTypes is the set of sing-box inbound "type" values whose
// per-user "users" array this Config indexes and injects credentials into.
var supportedInboundTypes = map[string]bool{
	"hysteria2":   true,
	"vless":       true,
	"vmess":       true,
	"trojan":      true,
	"shadowsocks": true,
	"tuic":        true,
}

// protoInbound holds one indexed supported inbound. typ is the sing-box inbound
// "type" (one of supportedInboundTypes) and decides the JSON shape each user in
// the "users" array is serialized to (see userEntry.toUserJSON). obj is a live
// reference into root["inbounds"][i], mutated in place. users is the source of
// truth for this inbound's "users" array, keyed by email/name.
type protoInbound struct {
	tag   string
	typ   string
	obj   map[string]any
	users map[string]userEntry
}

// userEntry is the union of the per-protocol fields a sing-box user object can
// carry. Which fields are populated (and serialized) depends on the owning
// inbound's typ: password for hysteria2/trojan/shadowsocks, uuid (+optional
// flow) for vless, uuid for vmess, uuid+password for tuic. The json tags describe the flat {name,
// password} shape and are used by tests that decode a generated config back;
// production serialization goes through toUserJSON so each type emits exactly
// the fields sing-box expects.
type userEntry struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

// toUserJSON renders this entry into the exact sing-box user object shape for
// the given inbound type:
//   - vless:       {name, uuid, flow?}   (flow omitted when empty)
//   - vmess:       {name, uuid, alterId} (alterId always 0)
//   - tuic:        {name, uuid, password}
//   - trojan/ss:   {name, password}
//   - hysteria2:   {name, password}
//
// The same shapes are used both for the config file's "users" array and for the
// clash_api hot-reload request body.
func (u userEntry) toUserJSON(typ string) map[string]any {
	switch typ {
	case "vless":
		m := map[string]any{"name": u.Name, "uuid": u.UUID}
		if u.Flow != "" {
			m["flow"] = u.Flow
		}
		return m
	case "vmess":
		return map[string]any{"name": u.Name, "uuid": u.UUID, "alterId": 0}
	case "tuic":
		return map[string]any{"name": u.Name, "uuid": u.UUID, "password": u.Password}
	default: // hysteria2, trojan, shadowsocks
		return map[string]any{"name": u.Name, "password": u.Password}
	}
}

// userEntryFromJSON parses a user object already present in an admin-supplied
// inbound template back into a userEntry, reading the fields relevant to typ.
// Returns false for objects without a usable "name".
func userEntryFromJSON(typ string, um map[string]any) (userEntry, bool) {
	name, _ := um["name"].(string)
	if name == "" {
		return userEntry{}, false
	}
	e := userEntry{Name: name}
	switch typ {
	case "vless":
		e.UUID, _ = um["uuid"].(string)
		e.Flow, _ = um["flow"].(string)
	case "vmess":
		e.UUID, _ = um["uuid"].(string)
	case "tuic":
		e.UUID, _ = um["uuid"].(string)
		e.Password, _ = um["password"].(string)
	default: // hysteria2, trojan, shadowsocks
		e.Password, _ = um["password"].(string)
	}
	return e, true
}

// sameUsers reports whether every indexed inbound in c holds exactly the same
// user set as the matching inbound in other. When it does, restarting the core
// would drop every live session only to reinstall the identical set of
// credentials - disruption for no change - so the caller can skip the restart.
// Compares the credential-bearing fields per type (via full userEntry equality);
// traffic counters and other churn are intentionally ignored.
func (c *Config) sameUsers(other *Config) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	other.mu.Lock()
	defer other.mu.Unlock()

	if len(c.inbounds) != len(other.inbounds) {
		return false
	}
	for tag, in := range c.inbounds {
		oin, ok := other.inbounds[tag]
		if !ok || len(in.users) != len(oin.users) {
			return false
		}
		for name, u := range in.users {
			ou, ok := oin.users[name]
			if !ok || ou != u {
				return false
			}
		}
	}
	return true
}

// sortedUserObjects returns this inbound's users as sing-box user objects
// (shaped per this inbound's type), ordered by name for stable output.
func (in *protoInbound) sortedUserObjects() []any {
	names := make([]string, 0, len(in.users))
	for name := range in.users {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, in.users[name].toUserJSON(in.typ))
	}
	return out
}

// NewConfig parses a raw sing-box JSON config and indexes every supported
// inbound found in it by tag, so users can later be synced in.
func NewConfig(raw string) (*Config, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("parse sing-box config: %w", err)
	}

	cfg := &Config{
		root:     root,
		inbounds: make(map[string]*protoInbound),
	}

	if err := cfg.indexInbounds(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) indexInbounds() error {
	inboundsRaw, ok := c.root["inbounds"]
	if !ok {
		// No inbounds at all is unusual for a node config but not fatal at parse
		// time - surfaces naturally later (no supported inbound to sync users into).
		return nil
	}

	inboundList, ok := inboundsRaw.([]any)
	if !ok {
		return fmt.Errorf("sing-box config: \"inbounds\" must be an array")
	}

	for idx, raw := range inboundList {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		typ, _ := obj["type"].(string)
		if !supportedInboundTypes[typ] {
			continue
		}

		tag, _ := obj["tag"].(string)
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return fmt.Errorf("sing-box config: %s inbound at index %d is missing a \"tag\"", typ, idx)
		}
		if _, dup := c.inbounds[tag]; dup {
			return fmt.Errorf("sing-box config: duplicate inbound tag %q", tag)
		}

		entry := &protoInbound{
			tag:   tag,
			typ:   typ,
			obj:   obj,
			users: make(map[string]userEntry),
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
				if ue, ok := userEntryFromJSON(typ, um); ok {
					entry.users[ue.Name] = ue
				}
			}
		}

		c.inbounds[tag] = entry
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
	// clash_api rides on the adjacent port; it hosts the /inbounds/{tag}/users
	// endpoint our custom sing-box exposes, which lets us swap an inbound's user
	// set live (no restart). If the port is taken, clash_api simply fails to bind
	// and the hot-reload call falls back to the restart path - no crash.
	c.clashPort = port + 1
	return c.refreshAPILocked()
}

// refreshAPILocked (re)writes the experimental.v2ray_api block so that its
// "stats.users" list always reflects every user email currently known across all
// indexed inbounds. This matters because sing-box's v2ray_api only creates a
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
			// flows. Listing every indexed inbound tag here is what makes
			// those two RPCs (which backend/singbox/stats.go's GetStats now
			// delegates Outbound/Outbounds to, since this backend's
			// minimal direct-outbound config has no traffic-bearing
			// "outbound" of its own to track) return real, non-zero data.
			"inbounds": c.allInboundTagsLocked(),
			"users":    c.allUserEmailsLocked(),
		},
	}
	// clash_api is bound to loopback only; it carries the custom
	// /inbounds/{tag}/users endpoint used for live user hot-reload. Left
	// unauthenticated because it is reachable only from this host (node and
	// sing-box share the network namespace).
	if c.clashPort != 0 {
		experimental["clash_api"] = map[string]any{
			"external_controller": fmt.Sprintf("127.0.0.1:%d", c.clashPort),
		}
	}
	c.root["experimental"] = experimental

	return nil
}

// ClashPort is the loopback port of the clash_api controller that hosts the
// live user-update endpoint. 0 until ApplyAPI has run.
func (c *Config) ClashPort() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clashPort
}

// inboundUserSet is one indexed inbound's protocol type plus its current user
// set, already rendered into the sing-box user object shape for that type.
// Used to push a live user update to the running core instead of restarting it.
type inboundUserSet struct {
	Type  string
	Users []any
}

// UserSets returns, per indexed inbound tag, its type and current user set
// (rendered as sing-box user objects). Used to push a live user update to the
// running core via the clash_api hot-reload endpoint instead of restarting it.
func (c *Config) UserSets() map[string]inboundUserSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]inboundUserSet, len(c.inbounds))
	for tag, in := range c.inbounds {
		out[tag] = inboundUserSet{Type: in.typ, Users: in.sortedUserObjects()}
	}
	return out
}

func (c *Config) allInboundTagsLocked() []string {
	tags := make([]string, 0, len(c.inbounds))
	for tag := range c.inbounds {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func (c *Config) allUserEmailsLocked() []string {
	seen := make(map[string]struct{})
	for _, inbound := range c.inbounds {
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

// ToBytes rebuilds each indexed inbound's "users" array and the
// experimental.v2ray_api "stats.users" list from current state, then marshals
// the whole tree as pretty-printed JSON ready to write to the config file
// sing-box is started with.
func (c *Config) ToBytes() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, inbound := range c.inbounds {
		inbound.obj["users"] = inbound.sortedUserObjects()
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

	for _, inbound := range c.inbounds {
		inbound.obj["users"] = inbound.sortedUserObjects()
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
	cloned.clashPort = c.clashPort

	return cloned, nil
}

// HasInbounds reports whether the config indexed at least one supported inbound
// (hysteria2/vless/vmess/trojan/shadowsocks/tuic). Useful for fast-failing at startup
// with a clear error instead of silently starting a sing-box process that can
// never carry traffic for any user.
func (c *Config) HasInbounds() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inbounds) > 0
}
