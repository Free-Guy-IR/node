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

// singBoxSyncDebounceWindow is how long enqueueSync waits after the first
// call in a batch before flushing - long enough to catch a burst of nearly-
// simultaneous syncs (confirmed live on a production node: three separate
// SyncUser calls 1-4 seconds apart, each triggering its own full restart),
// short enough that a single isolated sync still applies promptly.
const singBoxSyncDebounceWindow = 1500 * time.Millisecond

// singBoxPendingBatch accumulates config mutators from every SyncUser/
// SyncUsers/UpdateUsers call that arrives during one debounce window, so
// they can all be applied with a single restart. done is closed (not sent
// on) once the batch's restart has completed, waking every caller waiting
// on it at once; err is only safe to read after that close happens-before
// relationship.
type singBoxPendingBatch struct {
	mutators []func(*Config)
	done     chan struct{}
	err      error
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
// enqueueSync (below) coalesces rapid successive calls into one restart to
// keep that disruption as infrequent as possible.
func (s *SingBox) SyncUser(ctx context.Context, user *common.User) error {
	return s.enqueueSync(func(c *Config) { c.upsertUser(user) })
}

// SyncUsers fully replaces the user list on every hysteria2 inbound and
// restarts sing-box.
func (s *SingBox) SyncUsers(ctx context.Context, users []*common.User) error {
	return s.enqueueSync(func(c *Config) { c.syncUsers(users) })
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
	return s.enqueueSync(func(c *Config) { c.updateUsers(users) })
}

// UpdateUsersAndRestart behaves identically to UpdateUsers on this backend -
// see UpdateUsers's doc comment.
func (s *SingBox) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	return s.UpdateUsers(ctx, users)
}

// enqueueSync merges concurrent/rapid-succession user-sync requests into a
// single restart instead of one restart per call. Every caller that lands in
// the same batch still blocks until that batch's restart is confirmed applied
// (or failed) and gets that exact result back, preserving the original
// synchronous contract of SyncUser/SyncUsers/UpdateUsers - only the number of
// underlying restarts changes, not the caller-visible semantics.
func (s *SingBox) enqueueSync(mutate func(*Config)) error {
	s.batchMu.Lock()
	b := s.batch
	isNew := b == nil
	if isNew {
		b = &singBoxPendingBatch{done: make(chan struct{})}
		s.batch = b
	}
	b.mutators = append(b.mutators, mutate)
	s.batchMu.Unlock()

	if isNew {
		time.AfterFunc(singBoxSyncDebounceWindow, func() { s.flushBatch(b) })
	}

	<-b.done
	return b.err
}

// flushBatch applies every mutator collected in b to a single cloned
// candidate config and restarts sing-box once for the whole batch. Detaching
// b from s.batch happens before the (potentially slow) restart so calls
// arriving while a restart is already in flight start a fresh batch instead
// of blocking indefinitely on batchMu.
func (s *SingBox) flushBatch(b *singBoxPendingBatch) {
	s.batchMu.Lock()
	if s.batch == b {
		s.batch = nil
	}
	mutators := b.mutators
	s.batchMu.Unlock()

	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	candidate, err := s.config.Clone()
	if err == nil {
		for _, m := range mutators {
			m(candidate)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = s.applyConfigWithRestart(ctx, candidate)
		cancel()
	}

	b.err = err
	close(b.done)
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
