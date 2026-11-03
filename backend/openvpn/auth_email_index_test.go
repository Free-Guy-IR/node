package openvpn

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"

	"github.com/pasarguard/node/common"
)

func ixSweepReference(users map[string]authEntry, email, username string, entry *authEntry) {
	if email != "" {
		for name, current := range users {
			if current.email == email && name != username {
				delete(users, name)
			}
		}
	}
	if username == "" {
		return
	}
	if entry == nil {
		delete(users, username)
		return
	}
	users[username] = *entry
}

func ixSnapshot(s *authStore) map[string]authEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.users)
}

func ixIndexMismatch(s *authStore) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	want := make(map[string][]string)
	for name, entry := range s.users {
		if entry.email != "" {
			want[entry.email] = append(want[entry.email], name)
		}
	}
	if len(want) != len(s.byEmail) {
		return fmt.Sprintf("index has %d emails, entries have %d: index %v entries %v", len(s.byEmail), len(want), s.byEmail, s.users)
	}
	for email, names := range s.byEmail {
		if len(names) == 0 {
			return fmt.Sprintf("index keeps an empty list for %q", email)
		}
		got := slices.Clone(names)
		slices.Sort(got)
		expected := want[email]
		slices.Sort(expected)
		if !slices.Equal(got, expected) {
			return fmt.Sprintf("index for %q is %v, entries say %v", email, got, expected)
		}
	}
	return ""
}

func ixRequireConsistent(t *testing.T, s *authStore, what string) {
	t.Helper()
	if mismatch := ixIndexMismatch(s); mismatch != "" {
		t.Fatalf("%s: %s", what, mismatch)
	}
}

func ixRandomEntry(r *rand.Rand, emails []string) authEntry {
	return authEntry{password: fmt.Sprintf("pw%d", r.IntN(3)), email: emails[r.IntN(len(emails))], maxConcurrent: uint32(r.IntN(2))}
}

func TestAuthStoreEmailIndexMatchesTheFullSweepOverRandomHistories(t *testing.T) {
	emails := []string{"", "a", "b", "c", "a2"}
	usernames := []string{"", "u1", "u2", "u3", "u4", "a", "b"}
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*7919))
		store := newAuthStore()
		reference := make(map[string]authEntry)
		for step := 0; step < 300; step++ {
			if r.IntN(25) == 0 {
				full := make(map[string]authEntry)
				for range r.IntN(6) {
					full[usernames[1+r.IntN(len(usernames)-1)]] = ixRandomEntry(r, emails)
				}
				store.replace(maps.Clone(full))
				reference = full
			} else {
				email := emails[r.IntN(len(emails))]
				username := usernames[r.IntN(len(usernames))]
				var entry *authEntry
				if r.IntN(3) != 0 {
					value := ixRandomEntry(r, emails)
					if r.IntN(4) != 0 {
						value.email = email
					}
					entry = &value
				}
				store.applyForEmail(email, username, entry)
				ixSweepReference(reference, email, username, entry)
			}
			if got := ixSnapshot(store); !maps.Equal(got, reference) {
				t.Fatalf("seed %d step %d: store %v, full sweep %v", seed, step, got, reference)
			}
			ixRequireConsistent(t, store, fmt.Sprintf("seed %d step %d", seed, step))
		}
	}
}

func TestAuthStoreAReplaceWithANilMapStartsAnEmptyIndex(t *testing.T) {
	store := newAuthStore()
	store.applyForEmail("a", "u1", &authEntry{password: "pw", email: "a"})
	store.replace(nil)
	ixRequireConsistent(t, store, "after a nil replace")
	store.applyForEmail("a", "u2", &authEntry{password: "pw", email: "a"})
	if got := ixSnapshot(store); len(got) != 1 {
		t.Fatalf("after a nil replace and one add the store holds %v", got)
	}
	ixRequireConsistent(t, store, "after an add following a nil replace")
}

func ixBackendEntries(b *Backend, tags []string) map[string]map[string]authEntry {
	out := make(map[string]map[string]authEntry, len(tags))
	for _, tag := range tags {
		out[tag] = ixSnapshot(b.instances[tag].auth)
	}
	return out
}

func ixReferenceUpdate(state map[string]map[string]authEntry, users []*common.User) {
	for _, user := range users {
		email := user.GetEmail()
		username, pw, maxConcurrent, hasCredential := openVPNCredential(user)
		if !hasCredential {
			if email == "" {
				continue
			}
			username = ""
		}
		for tag, entries := range state {
			var entry *authEntry
			if hasCredential && slices.Contains(user.GetInbounds(), tag) {
				entry = &authEntry{password: pw, email: email, maxConcurrent: maxConcurrent}
			}
			ixSweepReference(entries, email, username, entry)
		}
	}
}

func TestASharedEmailRemovalRevokesExactlyWhatTheFullSweepRevoked(t *testing.T) {
	tags := []string{"ovpn-a", "ovpn-b"}
	initial := []*common.User{
		ovpnUser("shared", "phone", "pw-phone", "ovpn-a", "ovpn-b"),
		ovpnUser("shared", "laptop", "pw-laptop", "ovpn-a"),
		ovpnUser("other", "other", "pw-other", "ovpn-a", "ovpn-b"),
		ovpnUser("", "anonymous", "pw-anon", "ovpn-b"),
	}
	updates := map[string][]*common.User{
		"email only removal":           {{Email: "shared"}},
		"empty proxies removal":        {{Email: "shared", Proxies: &common.Proxy{}}},
		"one of the two kept":          {ovpnUser("shared", "laptop", "pw-laptop", "ovpn-b")},
		"one of the two revoked":       {ovpnUser("shared", "phone", "pw-phone")},
		"renamed to a third username":  {ovpnUser("shared", "tablet", "pw-tablet", "ovpn-a")},
		"username moved to other":      {ovpnUser("other", "phone", "pw-moved", "ovpn-b")},
		"removal then re-add in batch": {{Email: "shared"}, ovpnUser("shared", "phone", "pw-new", "ovpn-a")},
		"re-add then removal in batch": {ovpnUser("shared", "phone", "pw-new", "ovpn-a"), {Email: "shared"}},
	}
	for name, update := range updates {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			b := nvRemovalBackend(t, tags...)
			if err := b.SyncUsers(ctx, initial); err != nil {
				t.Fatal(err)
			}
			reference := ixBackendEntries(b, tags)
			if err := b.UpdateUsers(ctx, update); err != nil {
				t.Fatal(err)
			}
			ixReferenceUpdate(reference, update)
			got := ixBackendEntries(b, tags)
			for _, tag := range tags {
				if !maps.Equal(got[tag], reference[tag]) {
					t.Fatalf("%s holds %v, the full sweep holds %v", tag, got[tag], reference[tag])
				}
				ixRequireConsistent(t, b.instances[tag].auth, tag)
			}
		})
	}
}

func ixEmail(i int) string {
	return fmt.Sprintf("mail-%02d", i)
}

func ixOutcome(user *common.User, tag string) map[string]authEntry {
	out := make(map[string]authEntry)
	username, pw, maxConcurrent, hasCredential := openVPNCredential(user)
	if hasCredential && slices.Contains(user.GetInbounds(), tag) {
		out[username] = authEntry{password: pw, email: user.GetEmail(), maxConcurrent: maxConcurrent}
	}
	return out
}

func ixOwnedBy(entries map[string]authEntry, email string) map[string]authEntry {
	out := make(map[string]authEntry)
	for username, entry := range entries {
		if entry.email == email {
			out[username] = entry
		}
	}
	return out
}

func ixRacingUser(r *rand.Rand, writer string, email string, round int, tags []string) *common.User {
	switch r.IntN(5) {
	case 0:
		return &common.User{Email: email}
	case 1:
		return &common.User{Email: email, Proxies: &common.Proxy{}}
	}
	var inbounds []string
	for _, tag := range tags {
		if r.IntN(2) == 0 {
			inbounds = append(inbounds, tag)
		}
	}
	username := fmt.Sprintf("%s-%s-%d", email, writer, r.IntN(3))
	return ovpnUser(email, username, fmt.Sprintf("pw-%s-%d", writer, round), inbounds...)
}

func TestConcurrentUpdatesSyncsRemovalsAndLoginsOnSharedEmailsLeaveTheLastWriteAndAConsistentIndex(t *testing.T) {
	tags := []string{"ovpn-a", "ovpn-b"}
	const emailCount = 12
	const rounds = 150
	for attempt := 0; attempt < 8; attempt++ {
		ctx := context.Background()
		b := nvRemovalBackend(t, tags...)
		writers := []string{"single", "batch", "remove", "full"}
		last := make(map[string]map[string]*common.User, len(writers))
		var wg sync.WaitGroup
		var lastMu sync.Mutex
		run := func(writer string, seed uint64, body func(r *rand.Rand, record func(*common.User))) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(seed, uint64(attempt)))
			mine := make(map[string]*common.User)
			body(r, func(u *common.User) { mine[u.GetEmail()] = u })
			lastMu.Lock()
			last[writer] = mine
			lastMu.Unlock()
		}
		wg.Add(len(writers) + 1)
		go run("single", 1, func(r *rand.Rand, record func(*common.User)) {
			for round := 0; round < rounds; round++ {
				u := ixRacingUser(r, "single", ixEmail(r.IntN(emailCount)), round, tags)
				_ = b.SyncUser(ctx, u)
				record(u)
			}
		})
		go run("batch", 2, func(r *rand.Rand, record func(*common.User)) {
			for round := 0; round < rounds; round++ {
				batch := make([]*common.User, 0, 6)
				for range 6 {
					batch = append(batch, ixRacingUser(r, "batch", ixEmail(r.IntN(emailCount)), round, tags))
				}
				_ = b.UpdateUsers(ctx, batch)
				for _, u := range batch {
					record(u)
				}
			}
		})
		go run("remove", 3, func(r *rand.Rand, record func(*common.User)) {
			for round := 0; round < rounds; round++ {
				u := &common.User{Email: ixEmail(r.IntN(emailCount))}
				if round%2 == 0 {
					_ = b.SyncUser(ctx, u)
				} else {
					_ = b.UpdateUsersAndRestart(ctx, []*common.User{u})
				}
				record(u)
			}
		})
		go run("full", 4, func(r *rand.Rand, record func(*common.User)) {
			for round := 0; round < rounds/10; round++ {
				full := make([]*common.User, 0, emailCount)
				for i := 0; i < emailCount; i++ {
					u := ixRacingUser(r, "full", ixEmail(i), round, tags)
					full = append(full, u)
				}
				_ = b.SyncUsers(ctx, full)
				for _, u := range full {
					record(u)
				}
			}
		})
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(5, uint64(attempt)))
			for round := 0; round < rounds*4; round++ {
				tag := tags[r.IntN(len(tags))]
				email := ixEmail(r.IntN(emailCount))
				username := fmt.Sprintf("%s-%s-%d", email, writers[r.IntN(2)], r.IntN(3))
				if got, _, ok := b.instances[tag].auth.authenticate(username, fmt.Sprintf("pw-single-%d", r.IntN(rounds))); ok && got != email {
					t.Errorf("username %s authenticated as %s", username, got)
				}
				_ = b.instances[tag].auth.emails()
			}
		}()
		wg.Wait()

		final := ixBackendEntries(b, tags)
		for _, tag := range tags {
			ixRequireConsistent(t, b.instances[tag].auth, fmt.Sprintf("attempt %d %s", attempt, tag))
			for i := 0; i < emailCount; i++ {
				email := ixEmail(i)
				got := ixOwnedBy(final[tag], email)
				var candidates []map[string]authEntry
				for _, writer := range writers {
					if u, ok := last[writer][email]; ok {
						candidates = append(candidates, ixOutcome(u, tag))
					}
				}
				if len(candidates) == 0 {
					candidates = append(candidates, map[string]authEntry{})
				}
				if !slices.ContainsFunc(candidates, func(c map[string]authEntry) bool { return maps.Equal(c, got) }) {
					t.Fatalf("attempt %d: %s holds %v for %s, which no writer's last write leaves: %v", attempt, tag, got, email, candidates)
				}
			}
		}

		settle := make([]*common.User, 0, emailCount)
		for i := 0; i < emailCount; i++ {
			settle = append(settle, ixRacingUser(rand.New(rand.NewPCG(uint64(i), uint64(attempt))), "settle", ixEmail(i), 0, tags))
		}
		if err := b.UpdateUsers(ctx, settle); err != nil {
			t.Fatal(err)
		}
		settled := ixBackendEntries(b, tags)
		for _, tag := range tags {
			want := make(map[string]authEntry)
			for _, u := range settle {
				maps.Copy(want, ixOutcome(u, tag))
			}
			if !maps.Equal(settled[tag], want) {
				t.Fatalf("attempt %d: after the last write of every user %s holds %v, want %v", attempt, tag, settled[tag], want)
			}
			ixRequireConsistent(t, b.instances[tag].auth, fmt.Sprintf("attempt %d %s settled", attempt, tag))
		}

		removals := make([]*common.User, 0, emailCount)
		for i := 0; i < emailCount; i++ {
			removals = append(removals, &common.User{Email: ixEmail(i)})
		}
		if err := b.UpdateUsers(ctx, removals); err != nil {
			t.Fatal(err)
		}
		for _, tag := range tags {
			if left := ixSnapshot(b.instances[tag].auth); len(left) != 0 {
				t.Fatalf("attempt %d: removing every email left %v authorized on %s", attempt, left, tag)
			}
			ixRequireConsistent(t, b.instances[tag].auth, fmt.Sprintf("attempt %d %s emptied", attempt, tag))
		}
	}
}
