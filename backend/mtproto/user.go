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
		entries[username] = secretEntry{email: user.GetEmail(), keyBytes: keyBytes}
	}

	b.mu.Lock()
	b.secretsByID = entries
	b.mu.Unlock()
}

// secretsForInstance builds the mtglib.Secret map for one instance: same key
// bytes as every other instance, but this instance's own fake-TLS domain as
// Host (Secret.Host is what a Telegram client validates SNI against, and it
// travels inline inside the secret string itself - see mtglib/secret.go).
func (b *Backend) secretsForInstance(domain string) map[string]mtglib.Secret {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make(map[string]mtglib.Secret, len(b.secretsByID))
	for username, entry := range b.secretsByID {
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
		inst.proxy.UpdateSecrets(b.secretsForInstance(inst.domain))
	}
}

// syncAllUsers fully replaces the authorized-user set across every instance.
func (b *Backend) syncAllUsers(users []*common.User) {
	b.applyInitialUsers(users)
	b.pushSecretsToInstances()
}

// updateUsers incrementally upserts/removes the given users. A user with no
// MtprotoUser credential is removed if present; this mirrors OpenVPN's
// updateUsers requirement that the panel keep sending the credential block
// even when revoking access (shrinking Inbounds is what actually signals
// revocation for protocols scoped by inbound tag - MTProto has no such
// per-instance tag scoping since every instance shares one user set, so here
// presence/absence of a parseable MtprotoUser block is the only signal).
func (b *Backend) updateUsers(users []*common.User) {
	b.mu.Lock()
	if b.secretsByID == nil {
		b.secretsByID = make(map[string]secretEntry)
	}
	for _, user := range users {
		username, keyBytes, ok := mtprotoCredential(user)
		if !ok {
			continue
		}
		b.secretsByID[username] = secretEntry{email: user.GetEmail(), keyBytes: keyBytes}
	}
	b.mu.Unlock()

	b.pushSecretsToInstances()
}

// removeUsers removes the given users' secrets, live.
func (b *Backend) removeUsers(users []*common.User) {
	b.mu.Lock()
	for _, user := range users {
		username, _, ok := mtprotoCredential(user)
		if !ok {
			continue
		}
		delete(b.secretsByID, username)
	}
	b.mu.Unlock()

	b.pushSecretsToInstances()
}

// SyncUser applies a single user's MTProto membership across all instances,
// live.
func (b *Backend) SyncUser(_ context.Context, user *common.User) error {
	if _, _, ok := mtprotoCredential(user); !ok {
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
