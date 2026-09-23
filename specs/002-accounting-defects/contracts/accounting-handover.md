# Contract: accounting hand-over across a Start

Scope: `backend/l2tp` (key: inbound tag), `backend/wireguard` (key: interface name), and, added by the
2026-09-23 scope decision, `backend/mtproto` and `backend/openvpn` (one backend per node, fixed key). Each
package owns its own registry; nothing is shared between packages.

| Operation | Caller | Effect |
|-----------|--------|--------|
| deposit(key, state) | `Shutdown`, after its final poll/flush | stores `state`; an occupied key logs an error line and is replaced |
| take(key) | `New`, before the first poll/sample | returns and removes the entry, or nothing |
| put back(key, state) | `New`, when it fails after `take` | stores the untouched `state` again |

## WireGuard adoption

- After the new interface exists, every adopted entry's counters restart at zero
  (`Tracker.RestartCounters`), keeping pending deltas.
- Entries whose key is not a configured peer of the new backend are marked removed
  (`Tracker.RemoveStats`), reported once by the next read with reset, then deleted.

## L2TP adoption

- The per-user tracker, `cumRx/cumTx`, `totalRx/totalTx`, both node-level trackers and the in-memory
  counted state are adopted as they are. Without a hand-over (process restart) the counted state is
  loaded from the counted-state file (see [l2tp-counted-state.md](l2tp-counted-state.md)); the two are
  equal unless a write failed, in which case the in-memory state is the more recent one.
- Users present in the adopted tracker but absent from the new user list are forgotten the same way
  `SyncUsers` forgets removed users (pending reported once).

## Tracker additions (`pkg/stats`)

- `Sample.Quiet bool`: when true the sample updates counters only; `LastActiveTime` and `EndpointIP`
  are left untouched. Zero value keeps today's behaviour for every existing caller.
- `(*Tracker).RestartCounters(keys []string)`: for each existing key, keeps the pending delta
  (`Current - Base`, floored at zero) and restarts `Current` at zero.

## MTProto adoption

- Shutdown stops every proxy (streams drained, middle-proxy connections force-closed and waited for),
  takes a final sample and deposits the detached per-user tracker plus the unread node-level outbound and
  per-inbound counters (read with swap-to-zero). A stopped backend answers stats with an error.
- New takes the hand-over before any listener accepts connections: carried node-level and per-inbound
  bytes are added to the new counters (a tag that is gone is reported once under its name, then dropped);
  adopted per-user keys of configured tags and authorised users restart at zero, the rest are removed
  (reported once).

## OpenVPN adoption

- Shutdown stops the supervisor, stops each process with a bounded management-socket drain (sessions
  without a DISCONNECT are settled with their last byte-count sample), takes a final sample and deposits
  the detached per-user tracker, the unread part of both per-instance trackers and the last known IPs.
- New takes the hand-over before the first process starts: every per-instance tracker counts from zero
  with the carried unread bytes (`InterfaceCountersTracker.CountFromZero`), adopted per-user keys restart
  at zero or are removed as for MTProto, and a gone tag is reported once, then dropped.
