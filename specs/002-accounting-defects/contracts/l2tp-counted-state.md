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
v2
<ifname> <pid> <started> <rx> <tx>
consumed <ifname> <pid> <started> <rx> <tx>
final <final record file name>
...
```

- First line `v2`; fields separated by single spaces; integers base 10. (`v1` was never released.)
- `<ifname> ...` is a watermark of a session that is (or was until the grace) listed.
- `consumed ...` is the watermark of a session whose final record has been counted; it keeps the final
  values so a lingering listing of the same session adds nothing, and an old-format final never matches it.
- `final <name>` names a final record that was present when the state was unreadable and counts as
  already counted.
- Missing file: nothing has been counted in this container yet.
- Present but unreadable, wrong version or malformed line: the reader treats every live session and
  final record present at that moment as counted up to its current value and logs one line.
- Written only when the content changed during a poll. A failed write logs once per failure streak, keeps
  the in-memory state and is retried at the next changing poll.
- A watermark that is neither listed nor held by an unremovable final record gains one miss per poll and
  is dropped after 360 misses (the grace, an hour at the default 10 s interval); misses restart at zero
  after a load.

## Poll semantics

1. Read final records (sorted by epoch, pid, path as today; dot files skipped) and live sessions (dot
   files and files without `started=` skipped for this poll).
2. For each final record: remove its file first; only if that succeeded (or it is already gone) count
   `max(final - baseline, 0)`, where the baseline is the watermark of its identity (an old-format record
   matches the most recently live unconsumed watermark of its interface, or none), and keep the final
   values as a consumed watermark. A record that cannot be removed is not counted this poll, keeps its
   watermark, and is logged once per failure streak.
3. For each live session whose `ifname` did not appear in step 2: baseline = its watermark (zero if none);
   count `max(current - baseline, 0)`; store `max(current, baseline)`.
4. Age watermarks that were neither listed nor held (the grace), then persist if changed.
