# Contract: L2TP counted state and final record

## Final record (written by ip-down)

One file per ended session in the L2TP final-record directory, name `<ifname>.<shell pid>.<epoch>`
(unchanged). Lines `key=value`:

| Key | Since | Meaning |
|-----|-------|---------|
| `user` | before | PPP peer name |
| `tag` | before | inbound tag |
| `ifname` | before | ppp interface |
| `rx`, `tx` | before | final interface counters |
| `pid` | this feature | pppd pid copied from the session file |
| `started` | this feature | start time copied from the session file |

Readers MUST accept records without `pid`/`started` (written by the previous script) and match them by
`ifname` alone.

## Counted-state file

One file per inbound tag in the L2TP run directory, next to the session and final-record directories,
written atomically (temporary file in the same directory, then rename), mode `0600`.

```
v1
<ifname> <pid> <started> <rx> <tx>
...
```

- First line `v1`; one line per counted session; fields separated by single spaces; integers base 10.
- Missing file: nothing has been counted in this container yet.
- Present but unreadable, wrong version or malformed line: the reader treats every live session and
  final record present at that moment as counted up to its current value and logs one line.
- Written only when the content changed during a poll. A failed write logs once per failure streak, keeps
  the in-memory state and is retried at the next changing poll.

## Poll semantics

1. Read final records (sorted by epoch, pid, path as today) and live sessions.
2. For each final record: baseline = counted entry for its identity (or for its `ifname` when it has no
   identity); count `max(final - baseline, 0)`; delete the entry; delete the record file.
3. For each live session whose `ifname` did not appear in step 2: baseline = counted entry for its
   identity (zero if none); count `max(current - baseline, 0)`; store `current`.
4. Drop counted entries whose identity is neither live nor consumed in this poll.
5. Persist if changed.
