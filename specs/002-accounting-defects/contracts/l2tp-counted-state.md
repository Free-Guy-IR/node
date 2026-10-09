# Contract: L2TP session, per-pppd record, final record and counted state

All files live under the L2TP run directory `/run/pg-l2tp` inside the container:

| Directory | Written by | Read by | One file per |
|-----------|-----------|---------|--------------|
| `sessions/<ifname>` | ip-up | the poll, ip-down | listed ppp session |
| `pppd/<pid>` | ip-up | ip-up, ip-down, the record sweep | pppd process that ran ip-up |
| `final/<ifname>.<shell pid>.<epoch>` | ip-down | the poll | ended session (or IPCP phase) |
| `counted/<inbound tag>` | the poll | a new backend instance | inbound tag |

The directories `final`, `pppd` and `sessions` are created with mode `0700` when the scripts are written.
Every file is written to a dot-prefixed temporary name in the same directory and renamed into place;
readers skip dot-prefixed names.

## Scripts

`ip-up` and `ip-down` are generated per inbound tag and replaced atomically (temporary file, then
rename, mode `0755`), so a pppd that runs a script while Restart or Start rewrites it executes either the
old or the new complete script, never a truncated one. They contain no comment lines and use only POSIX
`sh` (busybox ash in the image, dash and bash in tests).

The directories the scripts use are assigned single-quoted. The scripts read a record line by line as
whole lines (`IFS= read -r`, a last line without a newline included) and cut the key at the first `=`;
the value is everything after it, kept byte for byte, so a user name may contain or end in `=` (the node
accepts any printable ASCII in a user name). Lines without `=` are ignored.

## Session file (written by ip-up)

Lines `key=value`:

| Key | Since | Meaning |
|-----|-------|---------|
| `user` | before | PPP peer name (`PEERNAME`); ip-up writes nothing without it |
| `tunnel_ip`, `client` | before | peer tunnel address, `REMOTENUMBER` |
| `tag` | before | inbound tag |
| `pid` | this feature | `PPPD_PID`, `0` when pppd did not export it |
| `started` | this feature | session start, unix seconds (see below) |
| `ifname` | this release | the interface name, also the file name |
| `ifindex` | this release | `/sys/class/net/<ifname>/ifindex` at ip-up, empty when unreadable |
| `proc` | this release | process fingerprint `<boot_id>:<pppd starttime>`, empty when unreadable |
| `mono` | this release | boot time of the session start `<boot_id>:<uptime in centiseconds>`, kept across a renegotiation like `started`; empty when unreadable |

`proc` is `/proc/sys/kernel/random/boot_id` and field 22 of `/proc/<pid>/stat` (the pppd start time in
clock ticks since boot). It identifies one pppd process across its whole life and tells it apart from a
later pppd that reuses the pid after a container restart or a reboot.

A file without `started=` is still being written and is skipped for that poll.

### Session identity and `started`

Identity is `(ifname, pid, started)`. ip-up takes `started` from the clock, except:

- If the per-pppd record of the same pid has the same non-empty `proc`, the same `ifname` and the same
  `ifindex` (or either `ifindex` is empty), this ip-up is an IPCP renegotiation of the same pppd on the
  same interface, and it reuses the record's `started`. The renegotiated session keeps its identity and
  is counted from its consumed watermark, so the bytes before the renegotiation are billed once.
- Otherwise, if the record's numeric `started` is not before the clock, `started` becomes the record's
  value plus one. Start times of one pid are therefore strictly increasing, and a pid reused within one
  second, on any interface name, never takes the identity of an ended session.

## Per-pppd record (written by ip-up)

`pppd/<pid>` has the same content as the session file and is written, when `pid` is not `0`, before the
session file. It exists so that a pppd's own identity survives the loss or replacement of its session
file:

- ip-up of a renegotiation reads it to keep `started` (above);
- a late ip-down reads it when the session file is no longer its own (below).

Lifecycle: overwritten by every ip-up of that pid; never removed by the scripts. Every poll of every
running inbound sweeps the directory. A record whose pppd is provably gone (see "pppd liveness") gains
one miss per consecutive poll; any poll that finds it running or cannot decide resets the count; after
360 misses (the grace, an hour at the default 10 s interval) it is removed if still unchanged. The count
is per backend instance and restarts at zero in a new instance.

## ip-down

1. Source of the session being ended, in order:
   - the session file `sessions/<ifname>` if its `pid` is this pppd's `PPPD_PID` (or either is `0` or
     empty: a file of the previous ip-up script);
   - else this pppd's own record `pppd/<PPPD_PID>`, if pppd exported `PEERNAME`, the record names the
     same interface and the record's `user` is `PEERNAME`;
   - else, if pppd exported `PEERNAME` and a non-zero pid, `PEERNAME`, the inbound tag and the pid alone
     (a pid-only final, below);
   - else nothing is written.
2. Counters: the interface counters from sysfs only while `ifindex` there equals the recorded `ifindex`,
   checked before and after reading them (the interface is still this pppd's); otherwise pppd's own
   `BYTES_RCVD`/`BYTES_SENT`; for a source without a recorded `ifindex` and without those variables, the
   sysfs counters; otherwise `0`.
3. The final record is written (temporary file, then rename) before the session file is touched.
4. Only when the source was the session file: the file is moved aside to a dot-prefixed name. If the
   moved file is not the one billed (another pid or start time, written in between by the next pppd), it
   is put back by hard link, or by rename when nothing took its place; the aside copy is then removed.

A late ip-down of a pppd that already exited therefore never bills, rewrites or removes the session file
of the next pppd on the same interface name, and still bills its own tail from its record, also when the
poll reaped its session file first.

## Final record (written by ip-down)

One file per ended session or renegotiated IPCP phase, name `<ifname>.<shell pid>.<epoch>`. Lines
`key=value`:

| Key | Since | Meaning |
|-----|-------|---------|
| `user` | before | PPP peer name |
| `tag` | before | inbound tag |
| `ifname` | before | ppp interface |
| `rx`, `tx` | before | final counters |
| `pid` | this feature | pppd pid from the source; omitted when empty |
| `started` | this feature | start time from the source; omitted when empty |
| `proc` | this release | process fingerprint from the source; omitted when empty |
| `mono` | this release | boot time of the session start from the source; omitted when empty |

Kinds, by the keys present:

- identity final: `pid` and `started`; matched to the watermark of its identity;
- pid-only final: `pid` (non-zero) without `started`; written only when ip-down has neither its session
  file nor its record. It is matched to the unconsumed watermark with the same interface name and pid,
  preferring the fewest misses, then the latest start, then the highest pid, and each one logs an error
  line when billed;
- old-format final: any other record (no `pid`/`started` pair and no non-zero lone `pid`), as written by
  the pre-fix script; matched by `ifname` alone to the most recently live unconsumed watermark of its
  interface.

### Reading rules

- A record that cannot be opened, or cannot be read to the end, is left in place and retried at every
  poll; the first failure of a streak logs one line. While it is pending, every watermark of the
  interface named in its file name is held (not aged). When the counted state is unreadable, it keeps the
  fallback window open and is decided by that window once it can be read (below). After 360 consecutive
  failing polls it is set aside as
  `.<name>.unreadable` with one log line and is not billed.
- A readable record of this inbound (or untagged) without a user, without an interface, without `rx` or
  `tx`, or with a number that is not a non-negative integer (empty `pid` and `started` read as `0`) is set
  aside at once as `.<name>.unreadable` with one log line naming it, and is never billed or deleted.
  A failed set-aside logs once per failure streak and is retried at the next poll.
- A readable record of another inbound is never removed or set aside by this inbound. A record that
  cannot be read has no known inbound, so whichever inbound reaches the end of the grace first sets it
  aside.
- Records set aside keep their content for inspection; they are dot-prefixed and never read again.

## Listing and read errors

`ENOENT` on the session or final-record directory means there is nothing to list. Any other error
listing either directory (for example `EMFILE`, `EACCES`, `ENOTDIR`) holds every watermark for that
poll, so none ages while the sessions or final records cannot be seen. A session file that cannot be
read (other than `ENOENT`) holds the watermarks of its interface. Each of the three kinds of failure logs
one line per failure streak. A session that is not listed in a poll is not counted in it and is counted
from its watermark once it is listed again.

## pppd liveness

The poll reaps a session file only when its pppd is provably gone:

- `/proc/<pid>/stat` answers `ENOENT` or `ESRCH`;
- the process there is not a pppd, or is a zombie or dead;
- the session records `proc` and the start time there differs, or the boot id is readable and differs.

Any other read error keeps the session listed and counted. A pid matching `proc` counts as the same
pppd even when the boot id cannot be read. A session with pid `0` is gone only when its interface is
absent (`ENOENT`). The reaper moves the file aside and removes it only if it is still the file it judged;
otherwise it puts it back. The user sync terminates a session only when its pppd is proven running.

## Counted-state file

Still `v2`: the lines below are unchanged, and this release adds the optional `prior` and `since` lines. One
file per inbound tag, written atomically, mode `0600`.

```
v2
<ifname> <pid> <started> <rx> <tx>
consumed <ifname> <pid> <started> <rx> <tx>
final <final record file name>
prior <final record file name>
since <T> [<boot>:<centis> [<later boot>...] [+]]
...
```

- First line `v2`; fields separated by single spaces; integers base 10. (`v1` was never released.)
- `<ifname> ...` is a watermark of a session that is (or was until the grace) listed.
- `consumed ...` is the watermark of a session whose final record has been counted; it keeps the final
  values so a lingering listing of the same session adds nothing, and an old-format final never matches it.
  A renegotiated session is listed again under its consumed identity and keeps counting from it.
- `final <name>` names a final record that was present when the state was unreadable and counts as
  already counted.
- `prior <name>` (optional, added in this release) names a final record that was present, and not named
  `final`, when the open window first listed the final records (below). An empty name or one holding a
  space or `/` makes the file unreadable.
- `since <T> [<boot>:<centis> [<later boot>...] [+]]` (optional, added in this release, header still `v2`)
  is an unreadable-state fallback whose window is still open (below): its wall time `T`, its boot time,
  the boots the node started in after it while the window stayed open, and `+` once the window has listed
  the final records present at the fallback. A malformed, zero, negative or repeated `since` line, a
  malformed boot time or a later boot holding `:` makes the file unreadable. A reader without these line
  types falls back as for any unreadable file.
- Missing file: nothing has been counted in this container yet.
- Present but unreadable, wrong version or malformed line: the reader logs one line and falls back:
  - every live session it can list and whose counters (and interface index) it can read is counted up to
    its current value, and every final record it can read is named `final` (counted);
  - if that load could list and read everything, nothing more is needed;
  - otherwise the fallback keeps a window open with its wall time `T` and its boot time (boot id and
    uptime when the unreadable state was found): until it closes, a session or identity final record that
    has no watermark is taken as counted (the session up to its value when first seen, the final record
    whole) if it started before the fallback, and counts from zero (a final record: billed whole) if it
    started at or after it; a final record without identity and without a watermark is taken as counted;
  - "started before the fallback" is decided on boot time when both the record and the fallback carry
    one: same boot, strictly earlier uptime; a live session of any other boot is after (a live session is
    of the current boot); a final record of a boot the node started in after the fallback is after. A
    final record of any other boot, one the window cannot place, is before if it was present when the
    window first listed the final records (named `prior`) or the window has not listed them yet, and after
    otherwise: it was written after that listing, so its session ran in a boot after the fallback whose id
    the node could not read or did not keep. While the node cannot read its own boot id, such a final
    record waits unbilled, and the boot id is read again at every poll. Records without a boot time fall
    back to the wall clock: `started` strictly before `T`;
  - the window lists the final records present at the fallback at the load that builds it or, when they
    cannot be listed then, at the first later load or poll that can list them; every final record listed
    then, readable or not, that is not named `final` is named `prior`. A `prior` name is dropped when its
    record is billed or taken as counted, and every one when the window closes;
  - the window closes after 360 consecutive clean polls: every session listed and resolved, no listing,
    read or removal failure, no unreadable or waiting final record.

  Boot time, not wall time: uptime only moves forward within a boot, so a wall clock stepped back or ahead
  cannot make a session counted before the fallback look new (billed again) or one started after it look
  old (never billed). Strictly earlier: a session started in the centisecond of the fallback counts from
  zero; the previous process could only have counted it by polling it within that same centisecond before
  it stopped. For records without a boot time (written by the previous scripts, or when `/proc/uptime`
  could not be read), the wall-clock rule keeps the old exposure: a clock stepped back or ahead across
  `T` misjudges them.
- The fallback state is written when it is built, and again at every load or poll that changes it, including
  while its window is open, so a restart never falls back again over what was counted; a restart during an
  open window continues it with the same `T`. A load that notes a later boot writes it at once, so a crash
  and reboot before the first poll do not forget that boot.
- Written only when the content changed during a load or a poll. A failed write keeps the in-memory state
  and is retried at the next changing poll; a poll's failed write logs once per failure streak, and a
  failed write at a load is logged by the poll that retries it.
- A watermark that is neither listed nor held gains one miss per poll and is dropped after 360 misses;
  misses restart at zero after a load. Held means: matched by an unremovable final record, of the same
  interface and pid as an unremovable identity final without `proc` that has no watermark of its own, or
  of the interface of an unreadable final record.

## Poll semantics

1. Read live sessions, then final records (sorted by epoch, pid, path; dot files skipped). Set aside the
   unusable records and account the unreadable ones as above.
2. For each final record: remove its file first; only if that succeeded (or it is already gone) count
   `max(final - baseline, 0)` and keep the final values as a consumed watermark. The baseline is the
   watermark its kind matches (above). An identity final without `proc` whose identity has no watermark
   takes the baseline of the fallback below. A record that cannot be removed is not counted this poll,
   holds its watermarks, and is logged once per failure streak.
3. For each live session whose `ifname` did not appear in step 2 and whose recorded `ifindex`, if any,
   still names its interface before and after the counters are read: baseline = its watermark; a session
   without a watermark and without `proc` takes the baseline of the fallback below, others start at zero;
   count `max(current - baseline, 0)`; store `max(current, baseline)`. A session whose interface index no
   longer matches, or whose counters cannot be read, is kept listed but not counted in that poll; a counter
   that cannot be read is never taken as zero.
4. Age watermarks that were neither listed nor held (the grace), persist if changed, then sweep the
   per-pppd records.

Fallback for a session or identity final without `proc`: the most recent watermark (latest `started`)
with the same interface name, the same non-zero pid and an earlier `started`, used only when the current
(or final) counters are not below it in either direction; that watermark is then raised to the current
counters so a late final of the earlier identity adds nothing. Otherwise the baseline is zero.

## Node-level series

The node-level outbound and inbound series (`Outbound(s)`, `Inbound(s)`) are sampled and applied to their
counter trackers under the backend lock, so two reset reads can never be applied out of their sampling
order. The trackers of a new instance count from zero, so the first read after the first poll reports
what that poll counted; a hand-over carries them to the next instance.

## Known residuals

The first two are reachable only when the scripts could not record what they normally record:

1. Same-pid watermark borrow for a session or final without a fingerprint. The production ip-up always
   writes `proc`; it is empty only when `/proc` could not be read at ip-up, or for files of the pre-fix
   script, which never coexist with this one because an image upgrade recreates the container. Such a
   session that reuses the pid and interface name of an earlier session, and whose first poll already
   reads at least that session's watermark, is under-billed by that watermark.
2. Pid-only final matching. Written only when ip-down finds neither its session file nor its own record
   (the record could not be written, or was removed from outside). If this pppd was never polled and an
   unconsumed watermark of an earlier pppd with the same pid and interface name is still within the grace,
   the tail is billed against that watermark. Every pid-only final logs an error line.

The unreadable-state fallback under-bills by design ("counting only growth from now on"): the bytes a
session present at the fallback moved after the previous process last counted it and before the fallback
first sees it are not billed. When its session file could not be read at the fallback, that includes its
growth until the file can first be read.

A final record of a boot after the fallback that the window cannot place is also taken as counted when it
was already present the first time the window could list the final records. That needs the final records
to be unlistable at the fallback and until that boot's session had ended, and that boot's id to be
unreadable for the whole life of its process (or the write of the boot noted at its load to have failed).
It is never billed twice: a final record present at that first listing is billed whole only when its own
boot time places it after the fallback.
