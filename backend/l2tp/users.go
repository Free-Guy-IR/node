package l2tp

import (
	"slices"
	"sort"
	"sync"

	"github.com/pasarguard/node/common"
)

type userEntry struct {
	username string
	password string
}

type userStore struct {
	inboundTag string
	mu         sync.RWMutex
	users      map[string]userEntry
}

func newUserStore(inboundTag string) *userStore {
	return &userStore{inboundTag: inboundTag, users: make(map[string]userEntry)}
}

func safeChapField(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func credsFor(u *common.User) (username, password string, ok bool) {
	cred := u.GetProxies().GetL2Tp()
	if cred == nil {
		return "", "", false
	}
	username, password = cred.GetUsername(), cred.GetPassword()
	if !safeChapField(username) || !safeChapField(password) {
		return "", "", false
	}
	return username, password, true
}

func usernameOf(u *common.User) string {
	if username, _, ok := credsFor(u); ok {
		return username
	}
	return u.GetEmail()
}

func (s *userStore) wantsInterface(u *common.User) bool {
	if _, _, ok := credsFor(u); !ok {
		return false
	}
	return slices.Contains(u.GetInbounds(), s.inboundTag)
}

func (s *userStore) replaceAll(users []*common.User) (removed []string) {
	next := make(map[string]userEntry)
	for _, u := range users {
		if !s.wantsInterface(u) {
			continue
		}
		username, password, _ := credsFor(u)
		next[username] = userEntry{username: username, password: password}
	}

	s.mu.Lock()
	for username := range s.users {
		if _, ok := next[username]; !ok {
			removed = append(removed, username)
		}
	}
	s.users = next
	s.mu.Unlock()
	return removed
}

func (s *userStore) applyUser(u *common.User) (username string, changed bool, removed bool) {
	username = usernameOf(u)
	if username == "" {
		return "", false, false
	}
	if !s.wantsInterface(u) {
		s.mu.Lock()
		_, existed := s.users[username]
		delete(s.users, username)
		s.mu.Unlock()
		return username, false, existed
	}
	_, password, _ := credsFor(u)
	entry := userEntry{username: username, password: password}
	s.mu.Lock()
	prev, existed := s.users[username]
	s.users[username] = entry
	s.mu.Unlock()
	return username, existed && prev != entry, false
}

func (s *userStore) snapshot() []userEntry {
	s.mu.RLock()
	out := make([]userEntry, 0, len(s.users))
	for _, e := range s.users {
		out = append(out, e)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].username < out[j].username })
	return out
}

func (s *userStore) has(username string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.users[username]
	return ok
}
