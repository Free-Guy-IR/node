# Data Model: Node accounting defects

## L2TP session identity

| Field | Source | Notes |
|-------|--------|-------|
| ifname | session file name / final record `ifname=` | reused by later sessions |
| pid | session file `pid=` (pppd pid) / final record `pid=` | 0 when pppd did not export it |
| started | session file `started=` / final record `started=` | unix seconds |

Identity = `(ifname, pid, started)`. A final record written by the pre-fix ip-down script has only
`ifname`; it matches the counted entry with that interface name.

## L2TP counted state (per inbound tag)

| Field | Meaning |
|-------|---------|
| identity | as above |
| rx, tx | counter values already counted for that session |

Lifecycle: created at the first poll that sees the session; updated by every poll; removed when the
session's final record is counted, or when the session is no longer listed and has no final record.
Persisted after every poll that changed it; loaded by `New`. File format: see
[contracts/l2tp-counted-state.md](contracts/l2tp-counted-state.md).

## L2TP final record

`user`, `tag`, `ifname`, `pid`, `started`, `rx`, `tx`; file name `<ifname>.<shell pid>.<epoch>` as today.

## Per-user tracker entry (`pkg/stats.Entry`)

Unchanged fields: `Email`, `CurrentRx/Tx`, `BaseRx/Tx`, `LastDeltaRx/Tx`, `LastActiveTime`, `EndpointIP`,
`IsDeleted`. Reported delta = `Current - Base`. New operations:

- `Sample.Quiet`: update `Current` (with the existing re-add and rollback handling) but leave
  `LastActiveTime` and `EndpointIP` untouched.
- `RestartCounters(keys)`: for each existing key, `pending = Current - Base` (floored at 0),
  `Current = 0`, `Base = -pending`. The next sample `s` then reports `pending + s`.

## Accounting hand-over

| Backend | Key | Payload |
|---------|-----|---------|
| L2TP | inbound tag | per-user tracker, `cumRx/cumTx`, `totalRx/totalTx`, node-level outbound and inbound trackers, counted state |
| WireGuard | interface name | per-user tracker |

States: absent -> deposited (by `Shutdown`) -> taken (by `New`, entry removed) -> put back (by a `New`
that fails after taking it). A deposit for an occupied key is unreachable (one backend per key); it logs
an error line and replaces the older entry.
