# Feature Specification: Node accounting defects (ships in node release 0.6.21)

**Feature Branch**: `release/v0.6.21` (feature directory `specs/002-accounting-defects`)

**Created**: 2026-09-23

**Status**: Draft

**Input**: User description: "Fix four node-side per-user traffic accounting defects found by the
2026-09-23 independent traffic reconciliation audit, inside the unreleased node release 0.6.21
(NodeVersion stays 0.6.21): L2TP over-billing after every Start, the MTProto attribution gap after a
Start, WireGuard session-tail under-billing, and the inverted uplink/downlink labels of WireGuard, L2TP
and OpenVPN. Every fix ships with a test that fails on eab2601 and passes after."

**Base**: `eab2601` on `release/v0.6.21` (NodeVersion 0.6.21, already carrying the Xray session kill,
the F3 stats ledger and the sing-box drain outside the ledger lock). Production runs 0.6.20.

**Billing paths changed by this spec (constitution, Release Constraints)**: L2TP per-user stats (the
session poll and the `UsersStat`/`UserStat` answers); WireGuard per-user stats (peer sampling, the
user-sync paths, restart and the backend lifecycle); MTProto user authorisation at backend start; the
shared uplink/downlink labelling used by WireGuard, L2TP, OpenVPN and MTProto. No other backend's
per-user path changes.

## Clarifications

### Session 2026-09-23

Answered by the lead without the owner (owner rule: never ask him), choosing the safest option each
time; the reason is recorded with each answer.

- Q: Should a WireGuard peer that never completed a handshake but has non-zero kernel counters be
  accounted? → A: Yes, like every other peer; its online status stays offline. Reason: the kernel
  counters are cumulative, so excluding such a peer only postpones its bytes to its first completed
  handshake and loses them if it is removed first; accounting them keeps one rule for every peer.
- Q: What does a new L2TP instance do when the counted-state file exists but cannot be read or parsed?
  → A: It treats every session and final record present at that moment as already counted up to its
  current value, logs once, and counts only later growth. A missing file means nothing was counted
  yet and everything present is counted whole. Reason: an unreadable file must never lead to billing
  bytes twice; the most it can lose is the growth of one poll interval.
- Q: How long is an accounting hand-over kept when no new backend with the same key starts (the new
  configuration dropped that L2TP tag or WireGuard interface)? → A: Until a backend with the same key
  takes it; it never expires. A `New` that took it and then fails puts it back unchanged. Only one
  backend per key runs, so a second deposit for an occupied key is unreachable; if it ever happens it
  logs an error line and the newer deposit replaces the older. Reason: pending deltas are billable
  bytes; memory is bounded by the number of distinct keys; the unreachable branch stays loud.
- Q: Do the node-level and per-inbound/interface series of WireGuard, L2TP and OpenVPN flip labels
  together with the per-user series? → A: Yes; one labelling rule serves all three series. Reason:
  node charts and user charts must agree on what uplink means.
- Q: May the WireGuard periodic sampler run between a pre-replacement flush and the counter restart
  that follows the kernel replacement? → A: No; sampling and the flush, kernel change and counter
  restart sequence are serialised, and a sampling tick waits while a user sync runs. Reason: an
  interleaved sample taken after the kernel reset but before the restart is recorded could count the
  same bytes twice.

### Session 2026-09-24

- Q: What are WireGuard's node-level counters in the accounting hand-over? → A: The interface byte
  counter behind the node-level (`Outbounds`) and per-interface (`Outbound`) series, which read one
  shared counter. At Shutdown the bytes it counted but the panel has not read are carried; the next
  backend of the same interface reports them once and counts the recreated interface from zero, even
  without a hand-over. A node-level read already running when Shutdown starts finishes before the
  hand-over is taken. Reason: a Start deletes and recreates the interface, so its counter starts at
  zero, and a read that reached the interface by name after the recreation would bill the
  successor's bytes twice.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An MTProto user can connect right after the panel restarts the node's core (Priority: P1)

The panel sends Start to the MTProto node (a reconnect, a config change or a panel restart). Every user
the panel sends with that Start, and who is on one of the node's MTProto inbounds, can authenticate and
use the proxy immediately, and the user's traffic is attributed to that user. Today no user can
authenticate after a Start until the panel happens to re-send that particular user; the only traffic
left is unauthenticated probing and domain fronting, attributed to nobody. On the production MTProto
node this has been the state since 2026-09-22 19:59:50.

**Why this priority**: It is a live service outage for paying users, not only a billing error.

**Independent Test**: Start the MTProto backend with a user carrying the instance's inbound tag and a
second user carrying another protocol's tag, then check that the first is authorised on the instance
at once and the second is not, without any later sync call.

**Acceptance Scenarios**:

1. **Given** a Start that carries user A on inbound `mtproto-tg`, **When** the backend is up,
   **Then** A's secret is accepted by the `mtproto-tg` listener before any SyncUser/SyncUsers/UpdateUsers call.
2. **Given** a Start that carries user B with MTProto credentials but only non-MTProto inbounds,
   **When** the backend is up, **Then** B is refused (the inbound scoping added for multi-core nodes is kept).
3. **Given** an authorised user A, **When** A's traffic flows, **Then** it is attributed to A in the
   per-user stats.

---

### User Story 2 - An L2TP user is billed once for each byte across core restarts (Priority: P1)

A user holds an L2TP session while the panel sends Start to the node (or the node process restarts
inside the same container). The user is billed for the bytes of that session exactly once: bytes the
previous backend instance already counted are not counted again, bytes that arrived after its last count
are counted once, and a session that ended around the restart is counted once. Today every Start re-adds
each live session's whole cumulative counter and each not-yet-read final record, over-billing about
3.95 GB since 2026-09-08 (a lower bound).

**Why this priority**: It over-charges users with every Start.

**Independent Test**: With a fake session directory and fake interface counters, count a live session
and a final record in one backend instance, shut it down, grow the counters, start a new instance and
check that the new instance reports only the growth.

**Acceptance Scenarios**:

1. **Given** a live session already counted up to N bytes, **When** a new instance starts and the
   counter now reads N+d, **Then** the new instance reports d for that user, not N+d.
2. **Given** a session that ended after the old instance last counted it at N bytes and whose final
   record says N+d, **When** the new instance reads the final record, **Then** it reports d once.
3. **Given** a session the old instance never saw, **When** the new instance reads its final record or
   its live counter, **Then** it reports the whole amount once.
4. **Given** a final record that is written while the ending session is still listed as live,
   **When** a poll sees both, **Then** the session's bytes are counted once, not twice.
5. **Given** an interface name reused by a new session after an old session vanished without a final
   record, **When** the new session is polled, **Then** its bytes are counted from zero, not from the old
   session's counter.

---

### User Story 3 - A WireGuard user is billed for every byte, including the end of a session (Priority: P2)

A WireGuard user transfers data and then stops, disconnects, is removed, or the node's interface is
replaced or recreated (full user sync, restart, Start). Every byte the kernel counted for that peer is
reported, including bytes after the peer's last handshake and bytes still unreported when the peer or the
interface disappears. Today a peer is only read while its last handshake is at most 45 s old, so bytes
after the last handshake+45 s are never read and are lost when the peer is removed or the interface is
recreated; the audit measured 0.2-1 % loss and up to 90 % of one user's bucket.

**Why this priority**: Under-billing; smaller than P1 but systematic.

**Independent Test**: With a fake device, give a peer a handshake older than 45 s and growing counters,
run the sampler and check that the growth is reported; remove the peer, replace all peers, and shut the
backend down after unsampled growth, and check that nothing is lost.

**Acceptance Scenarios**:

1. **Given** a peer whose last handshake is older than 45 s and whose counters grew by d, **When** the
   sampler runs, **Then** d is reported for that user.
2. **Given** unsampled growth d on a peer, **When** the peer is removed by a user sync, **Then** d is
   reported at the next stats read.
3. **Given** unsampled growth d on peers, **When** a full sync or restart replaces the peer list (which
   restarts every peer's kernel counters at zero), **Then** d is reported once and later growth is
   counted from zero without loss or double counting.
4. **Given** unsampled growth d and unreported deltas p, **When** the panel sends Start (the old backend
   shuts down, the interface is deleted and a new backend recreates it), **Then** d+p are reported by the
   new backend.
5. **Given** a peer whose last handshake is older than 45 s, **When** online status or the online IP
   list is asked for, **Then** the answers are the same as before this change.

---

### User Story 4 - Uplink means client to server on every protocol (Priority: P3)

The operator reads the per-user and per-node charts. For every protocol, uplink is what the user sent
and downlink is what the user received. Today WireGuard, L2TP and OpenVPN label server-transmitted bytes
(the user's download) as uplink, the opposite of Xray, sing-box and MTProto.

**Why this priority**: Reporting only; the billed total (uplink+downlink) is not affected.

**Independent Test**: Feed each of WireGuard, L2TP, OpenVPN and MTProto a known client-to-server and
server-to-client amount and check the labels and that the per-user sum is unchanged.

**Acceptance Scenarios**:

1. **Given** a WireGuard, L2TP or OpenVPN user that sent 10 and received 90 bytes, **When** stats are
   read, **Then** uplink is 10 and downlink is 90 (per user, per interface/instance and node level).
2. **Given** an MTProto user that sent 10 and received 90 bytes, **When** stats are read, **Then**
   uplink is 10 and downlink is 90, as before this change.
3. **Given** any input, **When** stats are read, **Then** uplink+downlink per user is byte-identical to
   the value on eab2601.

### Edge Cases

- L2TP: a final record written by the pre-fix ip-down script (no session identity in it) is matched by
  interface name; if no counted state exists for it, it is counted whole, once.
- L2TP: the counted-state file is missing: nothing was counted yet, everything present is counted
  whole. The file exists but is unreadable or corrupt: every session and final record present is taken
  as counted up to its current value, one log line is written, and the instance never fails to start
  because of it.
- L2TP: a session disappears without a final record (pppd killed): its counted state is dropped; bytes
  after its last poll are lost as today (no counter left to read).
- L2TP: the container is recreated (new image): the run directory is new, so there are no sessions,
  final records or counted state; nothing to double count.
- WireGuard: a peer that never completed a handshake but has non-zero counters is accounted; its
  online status stays offline.
- WireGuard: a user sync whose kernel call fails does not restart any counter; the flush taken before
  the call is an ordinary sample and loses or duplicates nothing.
- WireGuard: a Start whose new configuration drops some peers: their pending deltas are reported once
  and the entries are then removed.
- WireGuard: the carried-over state for an interface name is taken by exactly one new backend.
- Hand-over: no new backend with the same key starts: the hand-over waits. A `New` that fails after
  taking it puts it back.
- WireGuard: a periodic sampling tick that falls while a user sync runs waits for the sync to finish.
- MTProto: a Start with zero users starts with an empty secret set, as today.
- Direction: node-level and per-inbound series of WireGuard, L2TP and OpenVPN flip labels together with
  the per-user series; MTProto's node-level and per-inbound series keep their labels.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001** (L2TP): The L2TP backend MUST keep, per inbound tag, the counter values it has already
  counted for each ppp session, identified by interface name plus session identity (pppd pid and start
  time), and MUST persist that counted state in the node's L2TP run directory after every poll that
  changes it, atomically.
- **FR-002** (L2TP): A new L2TP backend instance MUST load the counted state before its first poll and
  MUST report, for each live session and each final record, only the growth beyond the counted value
  (never negative); a session or final record without counted state MUST be reported whole, once.
- **FR-003** (L2TP): The ip-down script MUST record the session identity in the final record; a poll
  that processes a final record MUST NOT count, in the same poll, a live session with the same interface
  name.
- **FR-004** (L2TP): When the panel sends Start to a node, per-user and node-level L2TP deltas already
  counted but not yet read by the panel MUST be handed to the next L2TP instance of the same inbound tag
  in the same process, so a Start loses no counted bytes.
- **FR-005** (MTProto): `New` MUST authorise every initial user that has valid MTProto credentials and
  carries at least one of the configured instance tags, before any listener accepts connections; users
  without such a tag MUST stay refused.
- **FR-006** (WireGuard): Each sampling tick MUST read every peer's cumulative counters and account the
  delta for every peer that belongs to a known user, regardless of handshake age.
- **FR-007** (WireGuard): Online status and the online IP list MUST keep their current semantics: only
  samples taken while the peer's last handshake is at most 45 s old may mark activity or update the
  endpoint.
- **FR-008** (WireGuard): Before removing peers, before replacing the whole peer list (full user sync
  and restart), and before deleting the interface on shutdown, the backend MUST sample all peers; after a
  replacement it MUST account every re-added peer's counters from zero while keeping the pending deltas.
- **FR-009** (WireGuard): On shutdown the backend MUST hand its per-user accounting state and the
  interface bytes its node-level counter counted but the panel has not read, keyed by interface name, to
  the next WireGuard backend of the same interface in the same process, which MUST report the pending
  deltas once, count the recreated interface's peer and interface counters from zero and release entries
  for peers that are no longer configured after reporting them.
- **FR-009a** (WireGuard): Periodic sampling MUST be serialised with every flush, kernel peer change
  and counter restart sequence.
- **FR-009b** (hand-over): A hand-over MUST be kept until a backend with the same key takes it; a `New`
  that fails after taking it MUST put it back; a deposit that finds its key occupied MUST log an error.
- **FR-010** (Direction): For WireGuard, L2TP and OpenVPN, uplink MUST be the bytes the node received
  from the client and downlink the bytes it sent to the client, in per-user, per-inbound/interface and
  node-level stats.
- **FR-011** (Direction): MTProto's uplink/downlink values MUST stay as on eab2601.
- **FR-012** (Direction): For every protocol, per-user uplink+downlink MUST equal the eab2601 value for
  the same input.
- **FR-013**: Every fix MUST ship with a test that fails on eab2601 and passes after, and the failing
  run MUST be recorded.
- **FR-014**: No explanatory comments in code or tests; no IP address in committed files; commits by
  Free-Guy-IR without a trailer; no AI or vendor name in committed files; nothing pushed.
- **FR-015**: NodeVersion MUST stay 0.6.21.

### Key Entities

- **Counted L2TP session state**: per inbound tag; per session (interface name, pppd pid, start time):
  the rx/tx values already counted. Lives as long as the run directory (the container).
- **L2TP final record**: written by ip-down when a session ends: user, tag, interface name, session
  identity, final rx/tx.
- **Accounting hand-over**: per backend kind and key (L2TP inbound tag, WireGuard interface name): the
  per-user tracker and node-level counters a shutting-down instance leaves for its successor in the same
  process; taken at most once.
- **Per-user tracker entry**: per user key: last cumulative value, reported base, pending delta,
  activity time and endpoint.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After a Start, 100 % of MTProto users sent with the Start and on an MTProto inbound are
  authorised at once (0 users need a later sync).
- **SC-002**: Across any number of L2TP instance restarts in one container, the bytes reported for a
  session equal its final counter exactly (0 bytes double counted, 0 bytes lost among bytes the node
  counted).
- **SC-003**: For WireGuard, the bytes reported for a peer equal the kernel counter growth up to the last
  flush, whatever the handshake age, through peer removal, peer-list replacement and interface
  recreation by Start (0 bytes lost among sampled or flushed bytes).
- **SC-004**: For WireGuard, L2TP and OpenVPN, uplink equals client-to-server bytes in 100 % of test
  inputs; MTProto labels unchanged; per-user uplink+downlink byte-identical to eab2601 in 100 % of inputs.
- **SC-005**: Every new test fails on eab2601 and passes on the fix; `go vet`, gofmt and the full suite
  as CI runs it are green; `-race` is green on the changed packages.

## Assumptions

- The Linux kernel restarts a WireGuard peer's byte counters when the peer list is replaced
  (`ReplacePeers`), and deleting the interface drops all counters.
- The ppp interface statistics are monotonic for the life of one pppd session; a new session on a reused
  interface name has a different pppd pid or start time.
- The ip-down script writes the final record before it removes the session file, and a new session
  cannot take the interface name before the old pppd released it.
- The panel reads per-user stats with reset every 30 s; a Start is followed by reads of the new backend.
- Bytes lost when the node process dies without a graceful shutdown (in-memory counts not yet read by
  the panel) remain lost, as for every other backend; the hand-over covers the in-process Start path.
- Historical per-user and per-node charts written before this release are inverted for WireGuard, L2TP
  and OpenVPN; they are not rewritten.
- A node is served by one panel. The in-process accounting hand-over (L2TP, WireGuard, MTProto,
  OpenVPN) carries the bytes not yet read by the panel across a Start to whichever panel reads next.
  When a node is moved to another panel, at most one poll of the old panel's unread per-user bytes would
  reach the new panel, which bills them by numeric user id. Operational rule: restart the node container
  before moving a node to another panel; the restart empties every in-process hand-over. Follow-up (not in
  this release): a node-side panel identity carried in gRPC metadata, with the matching bridge change, so
  the node can refuse a hand-over to a different panel mechanically.
- Carrying accounting across a Start for Xray and sing-box (whose counters live in the core process that
  the Start replaces) is out of scope. MTProto and OpenVPN keep their counters in the node process and,
  by a scope decision on 2026-09-23, hand their unread accounting over like L2TP and WireGuard.
