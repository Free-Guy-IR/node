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

// singBoxSyncDebounceWindow is how long enqueueSync waits, after the most
// recent call to join a batch, before flushing it - it slides forward on
// every new call that joins an in-flight batch, so a trickle of syncs a
// couple seconds apart keeps extending the wait instead of each one
// triggering its own restart.
//
// singBoxSyncDebounceMaxWait caps that sliding window so sustained load
// can't delay a batch indefinitely: once this much time has passed since
// the batch's first call, it flushes regardless of how recently the last
// one joined.
//
// Both values come from live production log analysis: restart gaps
// (each one a separate SyncUser call, each disconnecting every Hysteria2
// user on the node) ranged from ~1s to ~25s apart. A single fixed window
// can only ever catch bursts tighter than itself - short windows miss
// most of that spread, long ones needlessly delay genuinely isolated
// syncs. The sliding window keeps genuinely close-together syncs merged
// however long the burst runs, while the max-wait cap keeps that bound
// well clear of the widely-spaced (13s, 25s) gaps that should just apply
// on their own.
const (
	singBoxSyncDebounceWindow  = 2 * time.Second
	singBoxSyncDebounceMaxWait = 8 * time.Second
)

// singBoxPendingBatch accumulates config mutators from every SyncUser/
// SyncUsers/UpdateUsers call that arrives during one debounce window, so
// they can all be applied with a single restart. done is closed (not sent
// on) once the batch's restart has completed, waking every caller waiting
// on it at once; err is only safe to read after that close happens-before
// relationship. mutators/lastActivity/deadline are only ever read or
// written while holding the owning SingBox's batchMu.
type singBoxPendingBatch struct {
	mutators     []func(*Config)
	done         chan struct{}
	err          error
	lastActivity time.Time
	deadline     time.Time
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
	b := s.enqueue(mutate)
	<-b.done
	return b.err
}

// QueueUser adds user's Hysteria2 membership to the current (or a new)
// pending batch and returns immediately, without waiting for that batch to
// flush.
//
// Why this exists alongside enqueueSync/SyncUser: the panel's node bridge
// sends a burst of individually-queued users to this node over ONE
// client-streaming SyncUser RPC - the controller/rpc layer's loop calls
// stream.Recv() for the next message only after backend.SyncUser() returns
// for the current one. If SyncUser blocks until its batch flushes (as
// enqueueSync does), that receive loop stalls for the same duration, so the
// next already-in-flight message on the wire is never read in time to join
// the same batch - every message ends up starting (and waiting out) its own
// batch, and coalescing never actually happens despite the debounce logic
// being "correct" in isolation. This was caught by burst-testing against a
// real panel: 8 users synced back-to-back on one stream still produced 7
// restarts.
//
// QueueUser breaks that stall: the controller/rpc layer calls this instead
// of SyncUser when the backend supports it (see the nonBlockingUserSyncer
// interface check in controller/rpc/user.go), so it can immediately go back
// to stream.Recv() for the next message. Every message already sent by the
// client lands in the same batch, and the whole stream's worth of users
// restarts sing-box exactly once.
//
// Trade-off: the caller no longer learns whether the eventual restart
// succeeds (the stream ack now just means "queued", not "applied"). A failed
// flush is logged from flushBatch instead of returned to a waiter. This is
// judged acceptable because a failing restart is an infrastructure problem
// (bad generated config, sing-box binary/env issue) rather than something
// retryable per-user, and it will surface again on the very next sync for
// any of the same users.
func (s *SingBox) QueueUser(ctx context.Context, user *common.User) error {
	s.enqueue(func(c *Config) { c.upsertUser(user) })
	return nil
}

// enqueue adds mutate to the current pending batch (starting a new one and
// scheduling its flush check if none is pending) and returns that batch
// without waiting on it - enqueueSync blocks on the returned batch's done
// channel itself, QueueUser does not.
func (s *SingBox) enqueue(mutate func(*Config)) *singBoxPendingBatch {
	s.batchMu.Lock()
	b := s.batch
	isNew := b == nil
	now := time.Now()
	if isNew {
		b = &singBoxPendingBatch{
			done:     make(chan struct{}),
			deadline: now.Add(singBoxSyncDebounceMaxWait),
		}
		s.batch = b
	}
	b.mutators = append(b.mutators, mutate)
	b.lastActivity = now
	s.batchMu.Unlock()

	if isNew {
		s.scheduleFlushCheck(b)
	}

	return b
}

// scheduleFlushCheck flushes b once singBoxSyncDebounceWindow has passed
// with no further calls joining it, or once singBoxSyncDebounceMaxWait has
// elapsed since it was created, whichever comes first. It re-schedules
// itself with a fresh timer rather than resetting an existing one, which
// sidesteps the well-known race between time.Timer.Reset and a timer that
// may have already fired (see the time.Timer docs) at the cost of a few
// harmless extra timer firings under sustained load. Because this chain
// only ever calls flushBatch(b) once and then stops (it returns instead of
// rescheduling right after), b.done can never be closed twice.
func (s *SingBox) scheduleFlushCheck(b *singBoxPendingBatch) {
	time.AfterFunc(singBoxSyncDebounceWindow, func() {
		s.batchMu.Lock()
		if s.batch != b {
			// Already flushed.
			s.batchMu.Unlock()
			return
		}
		quiet := time.Since(b.lastActivity)
		atDeadline := !time.Now().Before(b.deadline)
		s.batchMu.Unlock()

		if quiet >= singBoxSyncDebounceWindow || atDeadline {
			s.flushBatch(b)
			return
		}
		s.scheduleFlushCheck(b)
	})
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
		if s.config.sameHysteria2Users(candidate) {
			// The batch's net effect left every hysteria2 inbound's user set
			// unchanged - the common case, since most syncs are traffic/online
			// updates or touch users in no hysteria2 inbound. A restart here
			// would drop every Hysteria2 session for nothing, so adopt the
			// candidate (cheap, keeps other fields current) without one.
			s.setConfig(candidate)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = s.applyConfigWithRestart(ctx, candidate)
			cancel()
		}
	}

	if err != nil {
		log.Printf("sing-box: batched user sync failed (%d user(s) in batch): %v", len(mutators), err)
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
