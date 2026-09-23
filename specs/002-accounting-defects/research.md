# Research: Node accounting defects

## R1 MTProto: why no user authenticates after a Start

- **Finding**: `backend/mtproto/mtproto.go` `New` calls `applyInitialUsers(users)` before the instance
  loop fills `b.instances`. Since `b044ce8` (2026-09-09) `applyInitialUsers` keeps a user only if
  `wantsUser` finds one of the user's inbound tags in `b.instances`, which is still empty, so every
  initial user is dropped and each instance's `ProxyOpts.Secrets` is empty. The middle-proxy path reads
  `b.secretsByID` live and is empty too. A user regains access only when the panel later sends that
  user through `SyncUser`, a chunked `UpdateUsers`, or a full `SyncUsers`. The existing scope tests
  build a `Backend` by hand with `instances` pre-filled, so they never exercised `New`.
- **Decision**: register the configured instance tags before seeding the users (either fill a tag set
  from `mtCfg.Instances` that `wantsUser` consults, or seed the users once `b.instances` is complete
  and before any proxy is built). The first `ProxyOpts.Secrets` must carry the users.
- **Consequence**: this is a client-authentication outage after every Start, not only an attribution
  gap. The flat node-level traffic with no user rows is unauthenticated probing and domain fronting.
- **Alternatives rejected**: dropping the scope check (re-opens the multi-core leak `b044ce8` closed);
  calling `SyncUsers` from the controller after `New` (fixes only one call site, hides the bug).

## R2 L2TP: what "already counted" means across a restart

- **Finding**: `New` builds empty `ifSeen`/`cumRx`/`cumTx`; `poll` counts a final record whole when its
  interface is not in `ifSeen`, and a live session from `[0,0]`. The session files and final records
  under the L2TP run directory survive a Start and a process restart inside the same container.
- **Finding**: ip-down writes the final record before it removes the session file, so a poll in that
  window sees both; today it counts the final, deletes the interface's baseline, then counts the live
  session again from zero.
- **Finding**: the baseline is keyed by interface name only; a session that vanished without a final
  record (pppd killed) leaves its baseline behind, and a new session on the same interface name is
  under-counted until it exceeds the old counter.
- **Decision**: counted state keyed by `(ifname, pid, started)` taken from the session file; ip-down
  copies `pid` and `started` into the final record; a final record is matched by its identity (by
  interface name only when it carries none, i.e. written by the old script); a live session whose
  interface has a final record in the same poll is skipped (counted, if it is a new session, from zero at
  the next poll); state persisted atomically per tag in the run directory after every changing poll and
  loaded by `New`; a missing file means nothing counted, an unreadable file means "everything present
  is counted up to its current value" (one log line).
- **Decision**: a Start additionally hands the per-user tracker, `cumRx/cumTx`, `totalRx/totalTx` and
  the two node-level trackers to the next instance of the same tag (see R4), so deltas counted but not
  yet read by the panel survive.
- **Alternatives rejected**: baselining every present session at its current value on `New` (loses up
  to one poll interval and every final record's unread tail); keeping the baseline only in memory
  (double counts after a process restart in the same container).

## R3 WireGuard: sampling, flushes and counter restarts

- **Finding**: `jobs.go` samples a peer only if its last handshake is at most 45 s old; active peers
  re-handshake every 120-180 s, so bytes are sampled late, and bytes after the last handshake+45 s are
  never sampled.
- **Finding**: `syncUsersFull` uses `ApplyPeersReplaceAll` and `restartLocked` uses `ReplacePeers`; the
  kernel handles `WGDEVICE_F_REPLACE_PEERS` by removing every peer and adding the listed ones, so every
  peer's counters restart at zero. `Shutdown` deletes the interface and a new backend recreates it.
- **Finding**: the tracker detects a counter restart only when the next sample is below the previous
  one; if a peer moves more than its old cumulative value before the next sample, the old value is lost.
- **Decision**: sample all peers every tick; `Sample.Quiet` (true when the handshake is zero or older
  than 45 s) keeps `LastActiveTime` and `EndpointIP` untouched, so online status and the IP list keep
  their semantics; flush (sample all peers) before any kernel call that removes or replaces peers and
  before `Close`; `Tracker.RestartCounters(keys)` after a replacement and after adopting a hand-over:
  `pending = Current - Base; Current = 0; Base = -pending`; serialise sampling with sync operations
  through the existing `syncMu` (outermost lock).
- **Decision**: `Shutdown` flushes, then hands the tracker to the next backend of the same interface
  name; `New` adopts it, restarts all counters at zero, and marks entries for keys that are no longer
  peers as removed so their pending deltas are reported once.
- **Alternatives rejected**: relying on the tracker's rollback detection after a replacement (wrong
  when growth exceeds the old value); leaving the 45 s gate and sampling "recently active" peers (still
  loses the tail).

## R4 Accounting hand-over across a Start

- **Decision**: a small package-level registry per backend package (`l2tp`, `wireguard`) keyed by the
  L2TP inbound tag / WireGuard interface name, guarded by a mutex. `Shutdown` deposits after its final
  flush/poll; `New` takes the entry (removing it) and puts it back if it fails. One backend per key
  runs, so a deposit never finds its key occupied; if it does, it logs an error and replaces the older
  entry. No expiry.
- **Rationale**: the controller drops the old backend object on every Start (`Disconnect` ->
  `Shutdown`, then `buildBackend`), so state that should outlive it must live outside it; keeping the
  registry inside each package keeps the fork seam local.
- **Out of scope**: Xray, sing-box, OpenVPN and MTProto keep their counters in the core process the
  Start replaces; carrying those is a separate change.

## R5 Direction labels

- **Finding**: `pkg/stats/interface_counters.go` `buildDeltaStats` emits `tx` as `uplink` and `rx` as
  `downlink`. WireGuard passes `Rx = peer.ReceiveBytes` (client to server), L2TP `rx_bytes` of the ppp
  interface (client to server), OpenVPN `bytes_received` (client to server): all three are inverted.
  MTProto passes `Tx = client->Telegram` and `Rx = Telegram->client`, so its labels are right today.
- **Decision**: `buildDeltaStats` emits `rx` as `uplink` and `tx` as `downlink`; MTProto swaps the
  values it passes (per-user samples, per-inbound and node-level) so its output is unchanged.
- **Proof of byte-identical billing**: for every input the multiset `{uplink, downlink}` per name equals
  `{rx, tx}` before and after, and the sum is `rx+tx`; tests assert both, and the MTProto tests that
  pin its labels stay unchanged and green.
- **Operational note**: charts written before the release are inverted for WireGuard, L2TP and OpenVPN.
