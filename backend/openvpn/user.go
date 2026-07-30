package openvpn

import (
	"context"
	"sync"

	"github.com/pasarguard/node/common"
)

// User sync for OpenVPN is fundamentally different from every other backend
// in this repo: xray hot-upserts via its HandlerService gRPC API, sing-box
// has no hot-upsert API at all and must rewrite-config-and-restart for every
// sync (see backend/singbox/user.go), and WireGuard reconfigures its kernel
// interface in place. OpenVPN sits apart from all three: users are
// authenticated per-connection over each instance's management-interface
// Unix socket (see management.go) against an in-memory username->credential
// map this file maintains - there is no config file entry to rewrite and
// therefore no restart required for ANY of SyncUser/SyncUsers/UpdateUsers/
// UpdateUsersAndRestart. This is this backend's headline advantage: user
// churn is instant and disruption-free for every other already-connected
// user, unlike sing-box where every single-user sync currently causes a
// brief connection drop for the whole node.

// authEntry is what an instance's authStore keeps per OpenVPN username: the
// password to validate against, and the user's canonical email. The latter
// is what every stats/online-tracking method in this package (and the
// Backend interface itself, e.g. GetUserOnlineStats(ctx, email)) keys by, so
// ManagementClient translates from the OpenVPN-specific "username" it sees
// on the wire back to "email" at the moment of successful auth (see
// authStore.authenticate, used as ManagementClient.authFn).
type authEntry struct {
	password string
	email    string
}

// authStore is the in-memory, per-instance username -> credential map that
// backs live (restart-free) user sync - see the doc comment above. It is the
// single source of truth ManagementClient.authFn consults for every
// CLIENT:CONNECT/REAUTH decision.
type authStore struct {
	mu    sync.RWMutex
	users map[string]authEntry
}

func newAuthStore() *authStore {
	return &authStore{users: make(map[string]authEntry)}
}

func (s *authStore) replace(users map[string]authEntry) {
	if users == nil {
		users = make(map[string]authEntry)
	}
	s.mu.Lock()
	s.users = users
	s.mu.Unlock()
}

func (s *authStore) upsert(username string, entry authEntry) {
	s.mu.Lock()
	s.users[username] = entry
	s.mu.Unlock()
}

func (s *authStore) remove(username string) {
	s.mu.Lock()
	delete(s.users, username)
	s.mu.Unlock()
}

func (s *authStore) contains(username string) bool {
	s.mu.RLock()
	_, ok := s.users[username]
	s.mu.RUnlock()
	return ok
}

// authenticate validates a username/password pair presented over the
// management socket, returning the user's email on success.
func (s *authStore) authenticate(username, password string) (string, bool) {
	s.mu.RLock()
	entry, ok := s.users[username]
	s.mu.RUnlock()

	if !ok || password == "" || entry.password != password {
		return "", false
	}
	return entry.email, true
}

// openVPNCredential extracts the (username, password) pair an OpenVpnUser
// proxy carries. A user with no OpenVpnUser proxy configured (ok == false)
// can never be added to or removed from an instance's authStore by
// username, since there is no username to key by - the panel is expected to
// keep sending the OpenVpnUser credential block even when revoking access,
// only shrinking Inbounds to signal the revocation (mirrors the same
// requirement singbox's SyncUser/updateUsers place on Hysteria2 users - see
// backend/singbox/user.go and its tests).
func openVPNCredential(user *common.User) (username, password string, ok bool) {
	proxy := user.GetProxies().GetOpenVpn()
	if proxy == nil {
		return "", "", false
	}
	username = proxy.GetUsername()
	if username == "" {
		return "", "", false
	}
	return username, proxy.GetPassword(), true
}

// syncAllUsers fully replaces the authorized-user set on every instance, and
// disconnects any currently-connected client whose username is no longer
// authorized on its instance. No subprocess restart, ever - see the doc
// comment above.
func (b *Backend) syncAllUsers(users []*common.User) {
	perInstance := make(map[string]map[string]authEntry, len(b.instances))
	for tag := range b.instances {
		perInstance[tag] = make(map[string]authEntry)
	}

	for _, user := range users {
		username, password, ok := openVPNCredential(user)
		if !ok {
			continue
		}
		email := user.GetEmail()
		for _, tag := range user.GetInbounds() {
			if m, exists := perInstance[tag]; exists {
				m[username] = authEntry{password: password, email: email}
			}
		}
	}

	for tag, instance := range b.instances {
		instance.auth.replace(perInstance[tag])
		instance.enforceAuthorized()
	}
}

// updateUsers incrementally upserts/removes the given users against every
// instance's authStore: a user is authorized on an instance's tag if it
// carries an OpenVPN credential and lists that tag in Inbounds, otherwise it
// is removed from that instance if present. Mirrors singbox's
// Config.updateUsers incremental-merge semantics (see
// backend/singbox/user.go).
func (b *Backend) updateUsers(users []*common.User) {
	touched := make(map[string]struct{}, len(b.instances))

	for _, user := range users {
		username, password, hasCredential := openVPNCredential(user)
		if !hasCredential {
			continue
		}

		activeTags := make(map[string]struct{}, len(user.GetInbounds()))
		for _, tag := range user.GetInbounds() {
			activeTags[tag] = struct{}{}
		}

		email := user.GetEmail()
		for tag, instance := range b.instances {
			if _, active := activeTags[tag]; active {
				instance.auth.upsert(username, authEntry{password: password, email: email})
			} else {
				instance.auth.remove(username)
			}
			touched[tag] = struct{}{}
		}
	}

	for tag := range touched {
		b.instances[tag].enforceAuthorized()
	}
}

// SyncUser applies a single user's OpenVPN membership across all instances,
// live - see the doc comment above for why no restart is needed here, unlike
// every other backend's SyncUser.
func (b *Backend) SyncUser(_ context.Context, user *common.User) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.updateUsers([]*common.User{user})
	return nil
}

// SyncUsers fully replaces the authorized-user set on every instance, live.
func (b *Backend) SyncUsers(_ context.Context, users []*common.User) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.syncAllUsers(users)
	return nil
}

// UpdateUsers incrementally merges the given users, live.
func (b *Backend) UpdateUsers(_ context.Context, users []*common.User) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.updateUsers(users)
	return nil
}

// UpdateUsersAndRestart behaves identically to UpdateUsers on this backend:
// there is no config-rewrite-plus-restart path to take at all here (see the
// doc comment above) - kept as a distinct method rather than an alias to
// preserve the Backend interface's call sites, mirroring sing-box's identical
// rationale for its own UpdateUsersAndRestart (backend/singbox/user.go).
func (b *Backend) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	return b.UpdateUsers(ctx, users)
}
