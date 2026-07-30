package openvpn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ManagementClient talks OpenVPN's line-based management-interface protocol
// over one instance's Unix domain socket (see render.go's "management
// <path> unix" / "management-client-auth" directives). It is the only piece
// of this package with no precedent elsewhere in this codebase - every other
// backend's equivalent (xray's HandlerService, sing-box's v2ray_api) is a
// typed gRPC API; OpenVPN's management interface is a bare line-oriented text
// protocol multiplexing three kinds of traffic on one connection:
//
//  1. synchronous command/response ("status 2" -> multi-line reply ending in
//     a line that is just "END", or a single "SUCCESS: ..."/"ERROR: ..."
//     line);
//  2. asynchronous, unprompted "real-time" notifications, always prefixed
//     with ">" (">CLIENT:CONNECT,...", ">BYTECOUNT_CLI:...", etc.) that can
//     arrive interleaved with a command's response at any time; and
//  3. commands we issue in reaction to a real-time notification
//     ("client-auth"/"client-deny" in response to ">CLIENT:CONNECT") that we
//     must NOT wait for a reply to, since the only goroutine that could ever
//     read that reply is the one currently blocked sending the command - see
//     writeLine vs sendCommand below.
//
// A single background goroutine (readLoop/consume) is the sole reader of the
// connection for its whole lifetime; it reconnects (with backoff, up to a
// bounded number of attempts) if the connection drops before Close is
// called, so a transient management-socket hiccup doesn't require the whole
// openvpn process to be restarted (process.go handles the process actually
// dying separately, by tearing this client down and building a fresh one on
// the next Start - see instanceProcess.Restart).
type ManagementClient struct {
	tag        string
	socketPath string

	// authFn validates a username/password pair from a CLIENT:CONNECT/REAUTH
	// env block, returning the user's email and per-user concurrent-session
	// cap (0 = unlimited) on success. Bound to an instance's
	// authStore.authenticate (see user.go).
	authFn func(username, password string) (email string, maxConcurrent uint32, ok bool)

	stopCh  chan struct{}
	doneCh  chan struct{}
	stopped atomic.Bool

	connMu sync.Mutex
	conn   net.Conn

	writeMu sync.Mutex

	cmdMu   sync.Mutex
	stateMu sync.Mutex
	pending *pendingCmd

	cidIdentity  map[string]clientIdentity
	online       map[string]clientIdentity
	onlineIPs    map[string]map[string]int64
	sessionStats map[string]ClientStats
	closedTotals map[string]ClientStats
}

type clientIdentity struct {
	username string
	email    string
}

// ClientStats is a cumulative uplink/downlink byte pair. "Uplink"/"downlink"
// follow this codebase's established convention (see pkg/stats' Stat
// construction, used identically by WireGuard's interface/peer counters):
// relative to the node's own network link, not the end user's - uplink is
// what this server transmitted (OpenVPN's BYTES_OUT / env "bytes_sent"),
// downlink is what it received (BYTES_IN / env "bytes_received").
type ClientStats struct {
	Uplink   uint64
	Downlink uint64
}

// clientEvent accumulates one >CLIENT:CONNECT|REAUTH|ESTABLISHED|DISCONNECT
// notification and the >CLIENT:ENV,k=v block that follows it, up to the
// terminating >CLIENT:ENV,END.
type clientEvent struct {
	kind string // CONNECT, REAUTH, ESTABLISHED, DISCONNECT
	cid  string
	kid  string
	env  map[string]string
}

// pendingCmd is the in-flight state for one sendCommand call: consume()
// appends reply lines to it as they arrive and closes done once a terminator
// line is seen.
type pendingCmd struct {
	lines []string
	err   error
	done  chan struct{}
}

func newManagementClient(tag, socketPath string, authFn func(username, password string) (string, uint32, bool)) *ManagementClient {
	return &ManagementClient{
		tag:          tag,
		socketPath:   socketPath,
		authFn:       authFn,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		cidIdentity:  make(map[string]clientIdentity),
		online:       make(map[string]clientIdentity),
		onlineIPs:    make(map[string]map[string]int64),
		sessionStats: make(map[string]ClientStats),
		closedTotals: make(map[string]ClientStats),
	}
}

// Connect blocks (bounded by ctx) until the management socket is dialable
// and the initial "state on"/"bytecount 5" subscription succeeds, then hands
// the connection off to a background goroutine that keeps reading events and
// silently reconnects (bounded attempts) if the connection drops before
// Close is called.
func (m *ManagementClient) Connect(ctx context.Context) error {
	conn, err := m.dialWithRetry(ctx)
	if err != nil {
		return err
	}
	return m.connectWithConn(conn)
}

// connectWithConn performs the subscribe handshake over an already-connected
// conn and starts the read loop. Split out from Connect so tests can drive
// the protocol over an in-process net.Pipe() without a real Unix socket.
func (m *ManagementClient) connectWithConn(conn net.Conn) error {
	r := bufio.NewReader(conn)
	if err := m.subscribe(conn, r); err != nil {
		conn.Close()
		return err
	}
	m.setConn(conn)
	go m.readLoop(conn, r)
	return nil
}

func (m *ManagementClient) dialAndSubscribe(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	conn, err := m.dialWithRetry(ctx)
	if err != nil {
		return nil, nil, err
	}
	r := bufio.NewReader(conn)
	if err := m.subscribe(conn, r); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, r, nil
}

func (m *ManagementClient) dialWithRetry(ctx context.Context) (net.Conn, error) {
	backoff := 150 * time.Millisecond
	for {
		conn, err := net.Dial("unix", m.socketPath)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial management socket %s: %w", m.socketPath, ctx.Err())
		case <-m.stopCh:
			return nil, errors.New("management client closed")
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

// subscribe issues the initial "state on"/"bytecount 5" commands. Called
// exactly once per physical connection (initial connect or each reconnect),
// always from the only goroutine that is or is about to become the
// connection's sole reader, so a plain synchronous write+read (sharing r,
// the same bufio.Reader consume() keeps using afterwards) is safe here - see
// the package doc comment's point 3 for why the general pending-command
// machinery (sendCommand) must NOT be used for anything called from that
// goroutine.
func (m *ManagementClient) subscribe(conn net.Conn, r *bufio.Reader) error {
	// Bound the handshake with a read deadline: without this, a management
	// socket that accepts the connection but never answers (a pathological
	// case, but one a reconnect attempt against a wedged openvpn process could
	// hit) would block this goroutine - and therefore Close(), which waits on
	// it - indefinitely. Cleared once the handshake completes; consume()'s
	// steady-state reads are deliberately unbounded (a real management socket
	// can sit idle for a long time between events).
	// Best-effort: not all net.Conn implementations support deadlines.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	for _, cmd := range []string{"state on", "bytecount 5"} {
		if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
			return fmt.Errorf("openvpn[%s]: write %q: %w", m.tag, cmd, err)
		}
		line, err := readReplyLine(r)
		if err != nil {
			return fmt.Errorf("openvpn[%s]: read reply to %q: %w", m.tag, cmd, err)
		}
		if strings.HasPrefix(line, "ERROR:") {
			return fmt.Errorf("openvpn[%s]: command %q failed: %s", m.tag, cmd, line)
		}
	}
	return nil
}

// readReplyLine reads until a non-empty line that isn't a ">"-prefixed
// real-time notification: every fresh connection immediately gets an
// unprompted ">INFO:OpenVPN Management Interface Version ..." banner line
// before any reply to a command we send, and in principle other real-time
// events could interleave too even this early.
func readReplyLine(r *bufio.Reader) (string, error) {
	for {
		raw, err := r.ReadString('\n')
		line := strings.TrimRight(raw, "\r\n")
		if line != "" && !strings.HasPrefix(line, ">") {
			return line, nil
		}
		if err != nil {
			return "", err
		}
	}
}

func (m *ManagementClient) readLoop(conn net.Conn, r *bufio.Reader) {
	defer close(m.doneCh)

	for {
		m.consume(conn, r)
		conn.Close()
		m.clearConn()
		m.resetVolatileState()

		if m.stopped.Load() {
			return
		}

		log.Printf("openvpn[%s]: management connection lost, attempting to reconnect", m.tag)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		newConn, newR, err := m.dialAndSubscribe(ctx)
		cancel()
		if err != nil {
			log.Printf("openvpn[%s]: giving up on management reconnection: %v", m.tag, err)
			return
		}

		m.setConn(newConn)
		conn, r = newConn, newR
	}
}

// consume reads and dispatches lines until the connection errors out (or
// Close() closes it out from under us), failing any command still waiting
// for a reply on the way out.
func (m *ManagementClient) consume(conn net.Conn, r *bufio.Reader) {
	var pending *clientEvent
	for {
		raw, err := r.ReadString('\n')
		line := strings.TrimRight(raw, "\r\n")
		if line != "" {
			pending = m.handleLine(line, pending)
		}
		if err != nil {
			m.failPending(fmt.Errorf("openvpn[%s]: management read error: %w", m.tag, err))
			return
		}
	}
}

func (m *ManagementClient) handleLine(line string, pending *clientEvent) *clientEvent {
	switch {
	case strings.HasPrefix(line, ">CLIENT:"):
		return m.handleClientLine(strings.TrimPrefix(line, ">CLIENT:"), pending)
	case strings.HasPrefix(line, ">BYTECOUNT_CLI:"):
		m.handleByteCount(strings.TrimPrefix(line, ">BYTECOUNT_CLI:"))
		return pending
	case strings.HasPrefix(line, ">"):
		// Other real-time notifications (>INFO:, >LOG:, >HOLD:, >STATE:, ...)
		// are outside this v1 integration's scope.
		return pending
	default:
		m.handleReplyLine(line)
		return pending
	}
}

// isFireAndForgetAck reports whether line is real openvpn's acknowledgement
// of a client-auth/client-deny command sent via writeLine (observed live
// against openvpn 2.5.11: "SUCCESS: client-auth command succeeded" - not
// documented in management-notes.txt, discovered empirically running the
// live smoke test in this package against a real binary). These must be
// filtered out here, unconditionally, before ever looking at m.pending:
// client-auth/client-deny are fire-and-forget (see writeLine's doc comment
// for why they can never be sent via the sendCommand/pending mechanism), so
// without this, an ack arriving while an unrelated sendCommand (e.g. Kill,
// Status, called from another goroutine) happens to have a command in
// flight would be misattributed to that command's response instead of
// being silently discarded, corrupting it.
func isFireAndForgetAck(line string) bool {
	if !strings.HasPrefix(line, "SUCCESS:") && !strings.HasPrefix(line, "ERROR:") {
		return false
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "client-auth") || strings.Contains(lower, "client-deny")
}

func (m *ManagementClient) handleReplyLine(line string) {
	if isFireAndForgetAck(line) {
		if strings.HasPrefix(line, "ERROR:") {
			log.Printf("openvpn[%s]: client-auth/client-deny command failed: %s", m.tag, line)
		}
		return
	}

	m.stateMu.Lock()
	pc := m.pending
	m.stateMu.Unlock()

	if pc == nil {
		log.Printf("openvpn[%s]: unexpected management reply with no command in flight: %s", m.tag, line)
		return
	}

	pc.lines = append(pc.lines, line)
	if strings.HasPrefix(line, "SUCCESS:") || strings.HasPrefix(line, "ERROR:") || line == "END" {
		if strings.HasPrefix(line, "ERROR:") {
			pc.err = errors.New(line)
		}
		m.stateMu.Lock()
		if m.pending == pc {
			m.pending = nil
		}
		m.stateMu.Unlock()
		close(pc.done)
	}
}

func (m *ManagementClient) handleClientLine(rest string, pending *clientEvent) *clientEvent {
	verb, payload, _ := strings.Cut(rest, ",")
	switch verb {
	case "CONNECT", "REAUTH":
		fields := strings.SplitN(payload, ",", 2)
		cid := fields[0]
		kid := ""
		if len(fields) > 1 {
			kid = fields[1]
		}
		return &clientEvent{kind: verb, cid: cid, kid: kid, env: make(map[string]string)}
	case "ESTABLISHED", "DISCONNECT":
		cid, _, _ := strings.Cut(payload, ",")
		return &clientEvent{kind: verb, cid: cid, env: make(map[string]string)}
	case "ENV":
		if pending == nil {
			return nil
		}
		if payload == "END" {
			m.dispatchClientEvent(pending)
			return nil
		}
		key, value, ok := strings.Cut(payload, "=")
		if ok {
			pending.env[key] = value
		}
		return pending
	default:
		// ADDRESS, CR_RESPONSE, etc.: not used by this v1 integration.
		return pending
	}
}

func (m *ManagementClient) dispatchClientEvent(ev *clientEvent) {
	switch ev.kind {
	case "CONNECT", "REAUTH":
		m.handleAuthRequest(ev)
	case "ESTABLISHED":
		m.handleEstablished(ev)
	case "DISCONNECT":
		m.handleDisconnect(ev)
	}
}

// handleAuthRequest answers a CLIENT:CONNECT or CLIENT:REAUTH notification.
// REAUTH (fired on TLS renegotiation) is validated against the exact same
// live authFn as a fresh CONNECT, so a user disabled/removed mid-session gets
// denied on its next renegotiation without needing an explicit kill.
func (m *ManagementClient) handleAuthRequest(ev *clientEvent) {
	username := ev.env["username"]
	password := ev.env["password"]

	var email string
	var maxConcurrent uint32
	var ok bool
	if m.authFn != nil {
		email, maxConcurrent, ok = m.authFn(username, password)
	}

	if !ok {
		log.Printf("openvpn[%s]: denying cid=%s username=%q (unknown user or bad password)", m.tag, ev.cid, username)
		if err := m.writeLine(fmt.Sprintf("client-deny %s %s %q", ev.cid, ev.kid, "invalid credentials")); err != nil {
			log.Printf("openvpn[%s]: failed to send client-deny for cid=%s: %v", m.tag, ev.cid, err)
		}
		return
	}

	// Concurrent-session cap only applies to a brand-new CONNECT, never to a
	// REAUTH: the reauthenticating cid is already present in m.online (added
	// at its own CLIENT:ESTABLISHED) and does not represent an additional
	// session, so counting it here would let a single already-connected user
	// at exactly their own cap get denied on their next TLS renegotiation.
	if ev.kind == "CONNECT" && maxConcurrent > 0 {
		if current := m.OnlineUserCount(email); current >= int(maxConcurrent) {
			log.Printf("openvpn[%s]: denying cid=%s username=%q (concurrent-session cap reached: %d/%d)", m.tag, ev.cid, username, current, maxConcurrent)
			if err := m.writeLine(fmt.Sprintf("client-deny %s %s %q", ev.cid, ev.kid, "too many concurrent connections")); err != nil {
				log.Printf("openvpn[%s]: failed to send client-deny for cid=%s: %v", m.tag, ev.cid, err)
			}
			return
		}
	}

	m.stateMu.Lock()
	m.cidIdentity[ev.cid] = clientIdentity{username: username, email: email}
	m.stateMu.Unlock()

	// client-auth's payload is a client-connect-style option block (the
	// options a --client-connect script would normally write); we have none
	// to push, so the block is empty - just the "END" terminator required by
	// the management protocol's multi-line command format.
	if err := m.writeLine(fmt.Sprintf("client-auth %s %s\nEND", ev.cid, ev.kid)); err != nil {
		log.Printf("openvpn[%s]: failed to send client-auth for cid=%s: %v", m.tag, ev.cid, err)
	}
}

func (m *ManagementClient) handleEstablished(ev *clientEvent) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	identity, ok := m.cidIdentity[ev.cid]
	if !ok {
		return
	}

	m.online[ev.cid] = identity

	if ip := clientRealIP(ev.env); ip != "" {
		if m.onlineIPs[identity.email] == nil {
			m.onlineIPs[identity.email] = make(map[string]int64)
		}
		m.onlineIPs[identity.email][ip] = time.Now().Unix()
	}
}

func clientRealIP(env map[string]string) string {
	if ip := env["trusted_ip6"]; ip != "" {
		return ip
	}
	return env["trusted_ip"]
}

func (m *ManagementClient) handleDisconnect(ev *clientEvent) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	identity, known := m.cidIdentity[ev.cid]
	delete(m.cidIdentity, ev.cid)
	delete(m.online, ev.cid)

	if !known {
		delete(m.sessionStats, ev.cid)
		return
	}

	// bytes_sent/bytes_received in the DISCONNECT env block are the
	// authoritative final counters for the session and take precedence over
	// whatever the last BYTECOUNT_CLI sample happened to record; fall back to
	// the last sample only if the env block didn't carry them.
	final := ClientStats{
		Uplink:   parseUintEnv(ev.env, "bytes_sent"),
		Downlink: parseUintEnv(ev.env, "bytes_received"),
	}
	if final.Uplink == 0 && final.Downlink == 0 {
		if last, ok := m.sessionStats[ev.cid]; ok {
			final = last
		}
	}

	totals := m.closedTotals[identity.email]
	totals.Uplink += final.Uplink
	totals.Downlink += final.Downlink
	m.closedTotals[identity.email] = totals

	delete(m.sessionStats, ev.cid)
}

func parseUintEnv(env map[string]string, key string) uint64 {
	v, err := strconv.ParseUint(env[key], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// handleByteCount processes a periodic ">BYTECOUNT_CLI:CID,BYTES_IN,BYTES_OUT"
// sample (subscribed to via "bytecount 5" in subscribe()). BYTES_IN/BYTES_OUT
// are cumulative-for-the-session totals, not deltas, so this simply
// overwrites the CID's last-known snapshot; see the ClientStats doc comment
// for the uplink/downlink direction mapping.
func (m *ManagementClient) handleByteCount(payload string) {
	fields := strings.Split(payload, ",")
	if len(fields) != 3 {
		return
	}
	bytesIn, errIn := strconv.ParseUint(fields[1], 10, 64)
	bytesOut, errOut := strconv.ParseUint(fields[2], 10, 64)
	if errIn != nil || errOut != nil {
		return
	}

	m.stateMu.Lock()
	m.sessionStats[fields[0]] = ClientStats{Uplink: bytesOut, Downlink: bytesIn}
	m.stateMu.Unlock()
}

func (m *ManagementClient) failPending(err error) {
	m.stateMu.Lock()
	pc := m.pending
	m.pending = nil
	m.stateMu.Unlock()

	if pc != nil {
		pc.err = err
		close(pc.done)
	}
}

func (m *ManagementClient) resetVolatileState() {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	// cidIdentity/online/sessionStats are scoped to CIDs handed out by one
	// specific management session and are meaningless after a reconnect (a
	// fresh "state on" subscription does not retroactively replay
	// CONNECT/ESTABLISHED for already-connected clients) - known v1
	// limitation: online/session counters may under-report until affected
	// clients naturally reconnect or renegotiate after a bare socket blip.
	// onlineIPs/closedTotals intentionally survive: they hold
	// historical/administrative data, not connection-session state.
	m.cidIdentity = make(map[string]clientIdentity)
	m.online = make(map[string]clientIdentity)
	m.sessionStats = make(map[string]ClientStats)
}

func (m *ManagementClient) setConn(c net.Conn) {
	m.connMu.Lock()
	m.conn = c
	m.connMu.Unlock()
}

func (m *ManagementClient) clearConn() {
	m.connMu.Lock()
	m.conn = nil
	m.connMu.Unlock()
}

// writeLine writes a raw command (which may itself contain embedded
// newlines, e.g. client-auth's "CID KID\nEND" block) directly to the
// connection, without waiting for or collecting a reply. Used exclusively
// for the fire-and-forget client-auth/client-deny replies issued from
// consume()'s own goroutine (see the package doc comment's point 3) - never
// call this and then try to read its reply via sendCommand/pending; the two
// mechanisms are intentionally independent so consume() can never block
// waiting on itself.
func (m *ManagementClient) writeLine(s string) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("openvpn[%s]: management connection not established", m.tag)
	}
	_, err := fmt.Fprintf(conn, "%s\n", s)
	return err
}

// sendCommand writes cmd and blocks for its reply. Must only ever be called
// from a goroutine other than the one running consume() (i.e. never from
// inside handleClientLine/dispatchClientEvent) - see writeLine's doc comment.
func (m *ManagementClient) sendCommand(cmd string) ([]string, error) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()

	pc := &pendingCmd{done: make(chan struct{})}
	m.stateMu.Lock()
	m.pending = pc
	m.stateMu.Unlock()

	if err := m.writeLine(cmd); err != nil {
		m.stateMu.Lock()
		if m.pending == pc {
			m.pending = nil
		}
		m.stateMu.Unlock()
		return nil, err
	}

	select {
	case <-pc.done:
		return pc.lines, pc.err
	case <-time.After(10 * time.Second):
		m.stateMu.Lock()
		if m.pending == pc {
			m.pending = nil
		}
		m.stateMu.Unlock()
		return nil, fmt.Errorf("openvpn[%s]: management command %q timed out", m.tag, cmd)
	case <-m.stopCh:
		return nil, fmt.Errorf("openvpn[%s]: management client closed", m.tag)
	}
}

// Close stops the client: no more reconnection attempts, the underlying
// connection is closed, and Close blocks until the background goroutine has
// fully exited so callers never leak it.
func (m *ManagementClient) Close() {
	if !m.stopped.CompareAndSwap(false, true) {
		return
	}
	close(m.stopCh)

	m.connMu.Lock()
	if m.conn != nil {
		m.conn.Close()
	}
	m.connMu.Unlock()

	<-m.doneCh
}

// Kill forcibly disconnects the client session identified by cid.
//
// Deliberately NOT the bare "kill" command: OpenVPN's management protocol
// gives "kill" two forms - "kill <common-name>" and "kill <IP>:<port>" -
// neither addresses a client by CID. The CID-addressed form for servers
// running in --management-client-auth mode (which this backend always uses -
// see render.go) is "client-kill CID [message]".
func (m *ManagementClient) Kill(cid string) error {
	lines, err := m.sendCommand(fmt.Sprintf("client-kill %s", cid))
	if err != nil {
		return err
	}
	if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "ERROR:") {
		return fmt.Errorf("openvpn[%s]: client-kill %s failed: %s", m.tag, cid, lines[len(lines)-1])
	}
	return nil
}

// EnforceAuthorized disconnects every session whose username no longer
// satisfies isAuthorized. Called after every auth-store mutation
// (SyncUser/SyncUsers/UpdateUsers - see user.go) so a disabled or removed
// user is dropped immediately instead of merely being unable to reconnect
// next time.
func (m *ManagementClient) EnforceAuthorized(isAuthorized func(username string) bool) {
	m.stateMu.Lock()
	type victim struct{ cid, username string }
	var victims []victim
	for cid, id := range m.cidIdentity {
		if !isAuthorized(id.username) {
			victims = append(victims, victim{cid, id.username})
		}
	}
	m.stateMu.Unlock()

	for _, v := range victims {
		if err := m.Kill(v.cid); err != nil {
			log.Printf("openvpn[%s]: failed to disconnect revoked user %q (cid=%s): %v", m.tag, v.username, v.cid, err)
		}
	}
}

// Status returns the raw "status 2" dump, for debugging/health checks.
func (m *ManagementClient) Status() (string, error) {
	lines, err := m.sendCommand("status 2")
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

// OnlineCount returns the number of currently-established sessions.
func (m *ManagementClient) OnlineCount() int {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	return len(m.online)
}

// OnlineUserCount returns the number of currently-established sessions for a
// given email (normally 0 or 1, but duplicate-cn deployments can have more).
func (m *ManagementClient) OnlineUserCount(email string) int {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	n := 0
	for _, id := range m.online {
		if id.email == email {
			n++
		}
	}
	return n
}

// UserStats returns email's cumulative traffic: closed-session totals plus
// whatever the currently-open session(s), if any, last reported.
func (m *ManagementClient) UserStats(email string) ClientStats {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	return m.userStatsLocked(email)
}

func (m *ManagementClient) userStatsLocked(email string) ClientStats {
	total := m.closedTotals[email]
	for cid, id := range m.cidIdentity {
		if id.email != email {
			continue
		}
		if s, ok := m.sessionStats[cid]; ok {
			total.Uplink += s.Uplink
			total.Downlink += s.Downlink
		}
	}
	return total
}

// AllUserStats returns cumulative traffic for every user this client has
// ever seen (closed or currently connected).
func (m *ManagementClient) AllUserStats() map[string]ClientStats {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	out := make(map[string]ClientStats, len(m.closedTotals))
	for email, totals := range m.closedTotals {
		out[email] = totals
	}
	for cid, id := range m.cidIdentity {
		s, ok := m.sessionStats[cid]
		if !ok {
			continue
		}
		t := out[id.email]
		t.Uplink += s.Uplink
		t.Downlink += s.Downlink
		out[id.email] = t
	}
	return out
}

// OnlineIPs returns email's known real client IPs and when each was last
// seen (unix seconds), aggregated from CLIENT:ESTABLISHED's trusted_ip(6).
func (m *ManagementClient) OnlineIPs(email string) map[string]int64 {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	src := m.onlineIPs[email]
	out := make(map[string]int64, len(src))
	for ip, ts := range src {
		out[ip] = ts
	}
	return out
}
