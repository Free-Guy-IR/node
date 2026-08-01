package middleproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"time"
)

const obfsHandshakeTimeout = 10 * time.Second

// SecretEntry is one candidate user secret an incoming connection's
// obfuscated2 handshake is tried against, plus the identity to attribute
// matched traffic to.
type SecretEntry struct {
	Username string
	Email    string
	Key      []byte // 16 raw bytes
}

// Options configures one Instance: the port it listens on and the ad tag
// every relayed packet on it carries. AdTag is the only reason this
// package exists - without one, the mtglib-based (v2) path already covers
// everything this does, with far less connection overhead.
type Options struct {
	Port  int
	AdTag []byte

	// Secrets returns the current set of user secrets authorized on this
	// instance. Called once per incoming connection (not cached across
	// connections), matching this backend's existing live-update-via-
	// UpdateSecrets model for the mtglib path - a secret added via
	// SyncUser/UpdateUsers takes effect on the very next connection
	// attempt, no restart needed.
	Secrets func() []SecretEntry

	OnAuth    func(streamID, username, email string)
	OnTraffic func(streamID string, n uint, isRead bool)
	OnFinish  func(streamID string)
	OnLog     func(msg string)
}

// Instance is one admin-configured, ad-tag-carrying MTProto listener. It
// mirrors mtglib.Proxy's calling convention on purpose (New then
// Serve(listener) then Shutdown) so backend/mtproto/mtproto.go's New/
// Restart/Shutdown can drive either kind of instance through the same
// listener-management code.
type Instance struct {
	opts Options
	done chan struct{}
}

// New builds an Instance; call Serve to start accepting on a listener.
func New(opts Options) *Instance {
	return &Instance{opts: opts, done: make(chan struct{})}
}

// Serve accepts connections on listener until it is closed or Shutdown is
// called. Blocking, like mtglib.Proxy.Serve - callers run it in its own
// goroutine.
func (i *Instance) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-i.done:
				return nil
			default:
				return err //nolint:wrapcheck
			}
		}
		go i.handle(conn)
	}
}

// Shutdown signals Serve's accept-error branch that a subsequent Accept
// error is an intentional close, not a failure worth returning. The
// listener itself is owned and closed by the caller (mtproto.go), matching
// mtglib.Proxy's Shutdown/listener-ownership split.
func (i *Instance) Shutdown() {
	select {
	case <-i.done:
	default:
		close(i.done)
	}
}

func (i *Instance) log(format string, args ...any) {
	if i.opts.OnLog == nil {
		return
	}
	i.opts.OnLog(fmt.Sprintf(format, args...))
}

func (i *Instance) handle(conn net.Conn) {
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(obfsHandshakeTimeout)) //nolint:errcheck

	var fm obfsFrame
	if _, err := io.ReadFull(conn, fm.data[:]); err != nil {
		return
	}

	entries := i.opts.Secrets()

	var (
		hs       obfsHandshakeResult
		matched  bool
		username string
		email    string
	)

	for _, entry := range entries {
		res, ok := tryObfsSecret(&fm, entry.Key)
		if !ok {
			continue
		}
		hs = res
		matched = true
		username = entry.Username
		email = entry.Email
		break
	}

	if !matched {
		return
	}

	conn.SetReadDeadline(time.Time{}) //nolint:errcheck

	streamID := newStreamID()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := Relay(ctx, conn, hs, relayOptions{
		AdTag:     i.opts.AdTag,
		StreamID:  streamID,
		Username:  username,
		Email:     email,
		OnAuth:    i.opts.OnAuth,
		OnTraffic: i.opts.OnTraffic,
		OnFinish:  i.opts.OnFinish,
	}); err != nil {
		i.log("mtproto middleproxy: connection for %q ended: %v", username, err)
	}
}

func newStreamID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck
	return hex.EncodeToString(b[:])
}
