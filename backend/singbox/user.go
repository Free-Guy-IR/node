package singbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/pasarguard/node/common"
)

// hysteria2Entry extracts the (name, password) pair sing-box expects for a
// hysteria2 inbound's "users" array from a common.User, if that user actually
// has a Hysteria2 proxy configured.
func hysteria2Entry(user *common.User) (hy2UserEntry, bool) {
	proxy := user.GetProxies().GetHysteria2()
	if proxy == nil {
		return hy2UserEntry{}, false
	}
	email := user.GetEmail()
	if email == "" {
		return hy2UserEntry{}, false
	}
	return hy2UserEntry{Name: email, Password: proxy.GetPassword()}, true
}

// syncUsers replaces the full user list of every indexed hysteria2 inbound:
// only users that (a) carry a Hysteria2 proxy and (b) list the inbound's tag in
// their Inbounds are kept. Mirrors xray's Config.syncUsers (full resync).
func (c *Config) syncUsers(users []*common.User) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, inbound := range c.hy2 {
		inbound.users = make(map[string]hy2UserEntry)
	}

	for _, user := range users {
		entry, ok := hysteria2Entry(user)
		if !ok {
			continue
		}
		for _, tag := range user.GetInbounds() {
			if inbound, ok := c.hy2[tag]; ok {
				inbound.users[entry.Name] = entry
			}
		}
	}
}

// updateUsers upserts/removes only the given users against every indexed
// hysteria2 inbound, leaving all other already-synced users untouched. A user is
// added/updated on an inbound's tag if it is active there (has a Hysteria2 proxy
// and lists that tag), otherwise it is removed from that inbound if present.
// Mirrors xray's Config.updateUsers/buildInboundUpdates incremental-merge
// semantics.
func (c *Config) updateUsers(users []*common.User) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, user := range users {
		email := user.GetEmail()
		if email == "" {
			continue
		}

		entry, active := hysteria2Entry(user)
		activeTags := make(map[string]struct{})
		if active {
			for _, tag := range user.GetInbounds() {
				activeTags[tag] = struct{}{}
			}
		}

		for tag, inbound := range c.hy2 {
			if _, isActive := activeTags[tag]; isActive {
				inbound.users[email] = entry
			} else {
				delete(inbound.users, email)
			}
		}
	}
}

// upsertUser is the single-user form of updateUsers, used by SyncUser.
func (c *Config) upsertUser(user *common.User) {
	c.updateUsers([]*common.User{user})
}

// SyncUser applies a single user's Hysteria2 membership across all hysteria2
// inbounds and restarts sing-box with the resulting config.
//
// Deviation from xray worth double-checking: xray's SyncUser/UpdateUsers add or
// remove a user live via the xray HandlerService gRPC API with no core restart.
// sing-box's experimental.v2ray_api only registers a StatsService (confirmed by
// reading experimental/v2rayapi/server.go on the dev box - only
// RegisterStatsServiceServer is called, there is no handler/proxyman-command
// equivalent). There is therefore no way to hot-upsert a Hysteria2 user into a
// running sing-box process: every one of SyncUser/SyncUsers/UpdateUsers/
// UpdateUsersAndRestart must rewrite the config file and restart the process.
// This means single-user syncs are far more disruptive here than on xray (brief
// connection drop for all users on this node, not just the one being synced).
// A follow-up could coalesce/debounce rapid successive single-user syncs to
// reduce restart frequency; out of scope for this v1 pass.
func (s *SingBox) SyncUser(ctx context.Context, user *common.User) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	candidate, err := s.config.Clone()
	if err != nil {
		return err
	}

	candidate.upsertUser(user)
	return s.applyConfigWithRestart(ctx, candidate)
}

// SyncUsers fully replaces the user list on every hysteria2 inbound and
// restarts sing-box.
func (s *SingBox) SyncUsers(ctx context.Context, users []*common.User) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	candidate, err := s.config.Clone()
	if err != nil {
		return err
	}

	candidate.syncUsers(users)
	return s.applyConfigWithRestart(ctx, candidate)
}

// UpdateUsers incrementally merges the given users and restarts sing-box.
//
// Note: on xray this is a live, restart-free operation. On sing-box it still
// requires a full restart (see SyncUser's doc comment for why) - functionally
// this makes UpdateUsers identical to UpdateUsersAndRestart on this backend.
// Kept as a distinct method (rather than aliasing) to preserve the Backend
// interface's semantics/call sites and in case a future sing-box version adds a
// hot-update API.
func (s *SingBox) UpdateUsers(ctx context.Context, users []*common.User) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	candidate, err := s.config.Clone()
	if err != nil {
		return err
	}

	candidate.updateUsers(users)
	return s.applyConfigWithRestart(ctx, candidate)
}

// UpdateUsersAndRestart behaves identically to UpdateUsers on this backend -
// see UpdateUsers's doc comment.
func (s *SingBox) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	return s.UpdateUsers(ctx, users)
}

// applyConfigWithRestart restarts the core with candidate, waits for the API to
// become healthy, and rolls back to the previous config on any failure. Mirrors
// xray's Xray.applyConfigWithRestart/restorePreviousConfig.
func (s *SingBox) applyConfigWithRestart(ctx context.Context, candidate *Config) error {
	previous := s.config

	if err := s.restartCoreWithConfig(candidate); err != nil {
		if restoreErr := s.restorePreviousConfig(previous); restoreErr != nil {
			return fmt.Errorf("%w; failed to restore previous sing-box config: %v", err, restoreErr)
		}
		return err
	}

	if err := s.checkStatus(ctx); err != nil {
		if restoreErr := s.restorePreviousConfig(previous); restoreErr != nil {
			return fmt.Errorf("%w; failed to restore previous sing-box config: %v", err, restoreErr)
		}
		return err
	}

	s.setConfig(candidate)
	return nil
}

func (s *SingBox) restorePreviousConfig(previous *Config) error {
	if previous == nil {
		return errors.New("previous sing-box config is nil")
	}

	log.Println("restoring previous sing-box config after failed restart")
	if err := s.restartCoreWithConfig(previous); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.checkStatus(ctx); err != nil {
		return err
	}

	s.setConfig(previous)
	return nil
}
