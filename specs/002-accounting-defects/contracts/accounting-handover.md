# Contract: accounting hand-over across a Start

Scope: `backend/l2tp` (key: inbound tag) and `backend/wireguard` (key: interface name). Each package
owns its own registry; nothing is shared between packages.

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
