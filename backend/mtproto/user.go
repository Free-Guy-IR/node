package mtproto

import (
	"context"
	"encoding/hex"

	"github.com/9seconds/mtg/v2/mtglib"

	"github.com/pasarguard/node/common"
)

// secretEntry is what Backend.secretsByID keeps per MTProto username: the
// raw 16-byte key (parsed once here, reused across every instance) and the
// email every stats/online-tracking method keys by.
type secretEntry struct {
	email    string
	keyBytes [mtglib.SecretKeyLength]byte
	inbounds map[string]struct{}
}

// mtprotoCredential extracts the (username, keyBytes) pair an MtprotoUser
// proxy carries. A user with no MtprotoUser proxy configured, or an
// unparsable secret, is skipped - mirrors backend/openvpn/user.go's
// openVPNCredential.
func mtprotoCredential(user *common.User) (username string, keyBytes [mtglib.SecretKeyLength]byte, ok bool) {
	proxy := user.GetProxies().GetMtproto()
	if proxy == nil {
		return "", keyBytes, false
	}

	username = proxy.GetUsername()
	if username == "" {
		return "", keyBytes, false
	}

	raw, err := hex.DecodeString(proxy.GetSecret())
	if err != nil || len(raw) != mtglib.SecretKeyLength {
		return "", keyBytes, false
	}

	copy(keyBytes[:], raw)

	return username, keyBytes, true
}

func (b *Backend) revokeIdentity(user *common.User) []string {
	var revoked []string
	if username := user.GetProxies().GetMtproto().GetUsername(); username != "" {
		if _, ok := b.secretsByID[username]; ok {
			revoked = append(revoked, username)
		}
		delete(b.secretsByID, username)
	}
	email := user.GetEmail()
	if email == "" {
		return revoked
	}
	for username, entry := range b.secretsByID {
		if entry.email == email {
			revoked = append(revoked, username)
			delete(b.secretsByID, username)
		}
	}
	return revoked
}

func revokedBetween(before, after map[string]secretEntry) []string {
	var revoked []string
	for username, entry := range before {
		if next, ok := after[username]; !ok || next.keyBytes != entry.keyBytes {
			revoked = append(revoked, username)
		}
	}
	return revoked
}

func (b *Backend) instanceTags(user *common.User) map[string]struct{} {
	tags := make(map[string]struct{})
	for _, tag := range user.GetInbounds() {
		if _, ok := b.instances[tag]; ok {
			tags[tag] = struct{}{}
		}
	}
	return tags
}

func (b *Backend) wantsUser(user *common.User) bool {
	return len(b.instanceTags(user)) > 0
}

// applyInitialUsers seeds Backend.secretsByID before any mtglib.Proxy is
// constructed, so New's first ProxyOpts.Secrets build already reflects every
// user provided at startup.
func (b *Backend) applyInitialUsers(users []*common.User) {
	entries := make(map[string]secretEntry, len(users))

	for _, user := range users {
		username, keyBytes, ok := mtprotoCredential(user)
		if !ok {
			continue
		}
		tags := b.instanceTags(user)
		if len(tags) == 0 {
			continue
		}
		entries[username] = secretEntry{email: user.GetEmail(), keyBytes: keyBytes, inbounds: tags}
	}

	b.mu.Lock()
	b.secretsByID = entries
	b.mu.Unlock()
}

// secretsForInstance builds the mtglib.Secret map for one instance: same key
// bytes as every other instance, but this instance's own fake-TLS domain as
// Host (Secret.Host is what a Telegram client validates SNI against, and it
// travels inline inside the secret string itself - see mtglib/secret.go).
func (b *Backend) secretsForInstance(tag, domain string) map[string]mtglib.Secret {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make(map[string]mtglib.Secret, len(b.secretsByID))
	for username, entry := range b.secretsByID {
		if _, on := entry.inbounds[tag]; !on {
			continue
		}
		out[username] = mtglib.Secret{Key: entry.keyBytes, Host: domain}
	}

	return out
}

// pushSecretsToInstances pushes the current Backend.secretsByID into every
// running mtglib.Proxy via UpdateSecrets - live, no restart, no dropped
// connections (see mtglib.Proxy.UpdateSecrets's doc comment in the
// Free-Guy-IR/mtg fork).
//
// Middle-proxy (ad-tag) instances are skipped here entirely: their Secrets
// field (see newMiddleProxyInstance) is a closure that reads b.secretsByID
// live on every connection attempt, so they never need - and never have -
// a *mtglib.Proxy to push into (inst.proxy is nil for them by construction,
// see the proxyInstance doc comment; calling UpdateSecrets on it panics
// with a nil pointer dereference, which crashed the node backend on every
// SyncUser call once any instance had an ad_tag configured - caught via a
// concurrent-load test hitting SyncUser repeatedly against a real ad-tag
// instance).
func (b *Backend) pushSecretsToInstances() {
	b.mu.RLock()
	instances := make([]*proxyInstance, 0, len(b.instances))
	for _, inst := range b.instances {
		instances = append(instances, inst)
	}
	b.mu.RUnlock()

	for _, inst := range instances {
		if inst.proxy == nil {
			continue
		}
		inst.proxy.UpdateSecrets(b.secretsForInstance(inst.tag, inst.domain))
	}
}

// syncAllUsers fully replaces the authorized-user set across every instance.
func (b *Backend) syncAllUsers(users []*common.User) {
	b.userSyncMu.Lock()
	defer b.userSyncMu.Unlock()

	b.mu.RLock()
	before := b.secretsByID
	b.mu.RUnlock()

	b.applyInitialUsers(users)
	b.pushSecretsToInstances()

	b.mu.RLock()
	revoked := revokedBetween(before, b.secretsByID)
	b.mu.RUnlock()
	liveSessionRegistry.revoke(revoked)
}

// updateUsers incrementally upserts/removes the given users. A user with no
// MtprotoUser credential is removed if present; this mirrors OpenVPN's
// updateUsers requirement that the panel keep sending the credential block
// even when revoking access (shrinking Inbounds is what actually signals
// revocation for protocols scoped by inbound tag - MTProto has no such
// per-instance tag scoping since every instance shares one user set, so here
// presence/absence of a parseable MtprotoUser block is the only signal).
func (b *Backend) updateUsers(users []*common.User) {
	b.userSyncMu.Lock()
	defer b.userSyncMu.Unlock()

	var revoked []string
	b.mu.Lock()
	if b.secretsByID == nil {
		b.secretsByID = make(map[string]secretEntry)
	}
	for _, user := range users {
		username, keyBytes, ok := mtprotoCredential(user)
		if !ok {
			revoked = append(revoked, b.revokeIdentity(user)...)
			continue
		}
		previous, existed := b.secretsByID[username]
		tags := b.instanceTags(user)
		if len(tags) == 0 {
			if existed {
				revoked = append(revoked, username)
			}
			delete(b.secretsByID, username)
			continue
		}
		if existed && previous.keyBytes != keyBytes {
			revoked = append(revoked, username)
		}
		b.secretsByID[username] = secretEntry{email: user.GetEmail(), keyBytes: keyBytes, inbounds: tags}
	}
	b.mu.Unlock()

	b.pushSecretsToInstances()
	liveSessionRegistry.revoke(revoked)
}

// removeUsers removes the given users' secrets, live.
func (b *Backend) removeUsers(users []*common.User) {
	b.userSyncMu.Lock()
	defer b.userSyncMu.Unlock()

	var revoked []string
	b.mu.Lock()
	for _, user := range users {
		username, _, ok := mtprotoCredential(user)
		if !ok {
			revoked = append(revoked, b.revokeIdentity(user)...)
			continue
		}
		revoked = append(revoked, username)
		delete(b.secretsByID, username)
	}
	b.mu.Unlock()

	b.pushSecretsToInstances()
	liveSessionRegistry.revoke(revoked)
}

// SyncUser applies a single user's MTProto membership across all instances,
// live.
func (b *Backend) SyncUser(_ context.Context, user *common.User) error {
	if _, _, ok := mtprotoCredential(user); !ok || !b.wantsUser(user) {
		b.removeUsers([]*common.User{user})
		return nil
	}
	b.updateUsers([]*common.User{user})
	return nil
}

// SyncUsers fully replaces the authorized-user set across every instance,
// live.
func (b *Backend) SyncUsers(_ context.Context, users []*common.User) error {
	b.syncAllUsers(users)
	return nil
}

// UpdateUsers incrementally merges the given users, live.
func (b *Backend) UpdateUsers(_ context.Context, users []*common.User) error {
	b.updateUsers(users)
	return nil
}

// UpdateUsersAndRestart behaves identically to UpdateUsers on this backend -
// UpdateSecrets never restarts anything, so there is no separate
// rewrite-and-restart path to take (mirrors backend/openvpn/user.go's
// identical rationale).
func (b *Backend) UpdateUsersAndRestart(ctx context.Context, users []*common.User) error {
	return b.UpdateUsers(ctx, users)
}
