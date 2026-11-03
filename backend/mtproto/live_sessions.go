package mtproto

import "sync"

type sessionScope struct {
	tag      string
	username string
}

type liveSession struct {
	tag    string
	closer func()
}

type liveSessions struct {
	mu          sync.Mutex
	epoch       uint64
	startEpoch  map[string]uint64
	revokedAt   map[string]uint64
	revokedOnAt map[sessionScope]uint64
	userOf      map[string]string
	byUser      map[string]map[string]liveSession
}

var liveSessionRegistry = &liveSessions{}

type instanceSessions struct {
	tag string
}

func (s instanceSessions) Begin(streamID string) {
	liveSessionRegistry.Begin(streamID)
}

func (s instanceSessions) Authenticated(streamID, username string, closer func()) {
	liveSessionRegistry.authenticatedOn(s.tag, streamID, username, closer)
}

func (s instanceSessions) End(streamID string) {
	liveSessionRegistry.End(streamID)
}

func (l *liveSessions) initLocked() {
	if l.startEpoch == nil {
		l.startEpoch = make(map[string]uint64)
		l.revokedAt = make(map[string]uint64)
		l.revokedOnAt = make(map[sessionScope]uint64)
		l.userOf = make(map[string]string)
		l.byUser = make(map[string]map[string]liveSession)
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
	l.authenticatedOn("", streamID, username, closer)
}

func (l *liveSessions) authenticatedOn(tag, streamID, username string, closer func()) {
	if l == nil || closer == nil {
		return
	}
	l.mu.Lock()
	l.initLocked()
	start, begun := l.startEpoch[streamID]
	if !begun || l.revokedAt[username] > start || l.revokedOnAt[sessionScope{tag: tag, username: username}] > start {
		l.mu.Unlock()
		closer()
		return
	}
	l.userOf[streamID] = username
	sessions := l.byUser[username]
	if sessions == nil {
		sessions = make(map[string]liveSession)
		l.byUser[username] = sessions
	}
	sessions[streamID] = liveSession{tag: tag, closer: closer}
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
	l.forgetLocked(username, streamID)
}

func (l *liveSessions) forgetLocked(username, streamID string) {
	if sessions := l.byUser[username]; sessions != nil {
		delete(sessions, streamID)
		if len(sessions) == 0 {
			delete(l.byUser, username)
		}
	}
}

func (l *liveSessions) revoke(usernames []string) {
	l.revokeScoped(usernames, nil)
}

func (l *liveSessions) revokeScoped(usernames []string, scopes []sessionScope) {
	if l == nil || len(usernames) == 0 && len(scopes) == 0 {
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
	for scope, at := range l.revokedOnAt {
		if at <= oldest {
			delete(l.revokedOnAt, scope)
		}
	}
	var closers []func()
	for _, username := range usernames {
		l.revokedAt[username] = l.epoch
		for streamID, session := range l.byUser[username] {
			closers = append(closers, session.closer)
			delete(l.userOf, streamID)
		}
		delete(l.byUser, username)
	}
	for _, scope := range scopes {
		l.revokedOnAt[scope] = l.epoch
		for streamID, session := range l.byUser[scope.username] {
			if session.tag != scope.tag {
				continue
			}
			closers = append(closers, session.closer)
			delete(l.userOf, streamID)
			l.forgetLocked(scope.username, streamID)
		}
	}
	l.mu.Unlock()

	for _, closer := range closers {
		closer()
	}
}
