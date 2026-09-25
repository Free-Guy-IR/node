# Data Model: Node accounting defects

## L2TP session identity

| Field | Source | Notes |
|-------|--------|-------|
| ifname | session file name / final record `ifname=` | reused by later sessions |
| pid | session file `pid=` (pppd pid) / final record `pid=` | 0 when pppd did not export it |
| started | session file `started=` / final record `started=` | unix seconds; kept by ip-up across an IPCP renegotiation of the same pppd, strictly increasing per pid otherwise |

Identity = `(ifname, pid, started)`. A final record written by the pre-fix ip-down script has only
`ifname`; it matches the counted entry with that interface name. A pid-only final record (`pid` without
`started`) matches the unconsumed counted entry with the same interface name and pid.

Not part of the identity, but recorded by ip-up next to it:

| Field | Source | Used for |
|-------|--------|----------|
| ifindex | `/sys/class/net/<ifname>/ifindex` | counting a session, and reading final counters, only while the interface is still its own |
| proc | `<boot_id>:<pppd starttime>` | telling one pppd process apart from a later one on the same pid (renegotiation, liveness, reaping) |

## L2TP per-pppd record

`/run/pg-l2tp/pppd/<pid>`, same content as the session file, written by every ip-up of that pid before
the session file. Read by ip-up (to keep `started` across a renegotiation) and by a late ip-down (its own
identity when its session file was replaced or reaped). Removed by the poll after its pppd has been
provably gone for 360 consecutive polls.

## L2TP counted state (per inbound tag)

| Field | Meaning |
|-------|---------|
| identity | as above |
| rx, tx | counter values already counted for that session |

Lifecycle: created at the first poll that sees the session; updated by every poll; marked consumed when
the session's final record is counted (a renegotiated session keeps counting from it); removed after the
grace once the session is neither listed nor held by a pending final record. Persisted after every poll
that changed it; loaded by `New`. The file format is unchanged (`v2`): see
[contracts/l2tp-counted-state.md](contracts/l2tp-counted-state.md).

## L2TP final record

`user`, `tag`, `ifname`, `pid`, `started`, `proc`, `rx`, `tx` (`pid`, `started` and `proc` omitted when
unknown); file name `<ifname>.<shell pid>.<epoch>` as today. States: present -> billed and removed; or
unreadable -> retried every poll -> billed, or set aside as `.<name>.unreadable` after 360 polls; or
unusable (no user, no interface, no counters, malformed number) -> set aside at once. Never deleted
unbilled.

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
