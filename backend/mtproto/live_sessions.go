package mtproto

import "sync"

type liveSessions struct {
	mu         sync.Mutex
	epoch      uint64
	startEpoch map[string]uint64
	revokedAt  map[string]uint64
	userOf     map[string]string
	byUser     map[string]map[string]func()
}

var liveSessionRegistry = &liveSessions{}

func (l *liveSessions) initLocked() {
	if l.startEpoch == nil {
		l.startEpoch = make(map[string]uint64)
		l.revokedAt = make(map[string]uint64)
		l.userOf = make(map[string]string)
		l.byUser = make(map[string]map[string]func())
	}
}

func (l *liveSessions) Begin(streamID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	l.startEpoch[streamID] = l.epoch
}

func (l *liveSessions) Authenticated(streamID, username string, closer func()) {
	if l == nil || closer == nil {
		return
	}
	l.mu.Lock()
	l.initLocked()
	start, begun := l.startEpoch[streamID]
	if !begun || l.revokedAt[username] > start {
		l.mu.Unlock()
		closer()
		return
	}
	l.userOf[streamID] = username
	sessions := l.byUser[username]
	if sessions == nil {
		sessions = make(map[string]func())
		l.byUser[username] = sessions
	}
	sessions[streamID] = closer
	l.mu.Unlock()
}

func (l *liveSessions) End(streamID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	delete(l.startEpoch, streamID)
	username, ok := l.userOf[streamID]
	if !ok {
		return
	}
	delete(l.userOf, streamID)
	if sessions := l.byUser[username]; sessions != nil {
		delete(sessions, streamID)
		if len(sessions) == 0 {
			delete(l.byUser, username)
		}
	}
}

func (l *liveSessions) revoke(usernames []string) {
	if l == nil || len(usernames) == 0 {
		return
	}
	l.mu.Lock()
	l.initLocked()
	l.epoch++
	oldest := l.epoch
	for _, start := range l.startEpoch {
		if start < oldest {
			oldest = start
		}
	}
	for username, at := range l.revokedAt {
		if at <= oldest {
			delete(l.revokedAt, username)
		}
	}
	var closers []func()
	for _, username := range usernames {
		l.revokedAt[username] = l.epoch
		for streamID, closer := range l.byUser[username] {
			closers = append(closers, closer)
			delete(l.userOf, streamID)
		}
		delete(l.byUser, username)
	}
	l.mu.Unlock()

	for _, closer := range closers {
		closer()
	}
}
