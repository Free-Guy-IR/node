# Upstream parity

This fork tracks [PasarGuard/node](https://github.com/PasarGuard/node). This page records which upstream changes
the fork has taken, which it has refused and why, and which upstream work is still pending. The daily drift check
(`scripts/upstream_drift.sh`, run by `.github/workflows/upstream-drift.yml`) keeps it honest: it fails as soon as
upstream `main` or `dev` has a commit that is neither ported nor waived with a reason.

State as of 2026-09-23, release 0.6.21.

## Fork point

- The fork merged upstream tag `v0.5.4` (`e9a9ba286413c404854b80c847b588b620426660`) in `9267b04`
  ("merge upstream PasarGuard/node v0.5.4 into fork", 2026-08-31). Everything reachable from `v0.5.4` is therefore in
  the fork's history.
- Upstream `main` has no commit after `v0.5.4`. All later upstream work is on `dev`.

## Upstream commits after the fork point

| upstream commit | subject | status in the fork |
|---|---|---|
| `9bb6b35df2cc514385c68687bc6f771795d4b68e` | deps: gopsutil 4.26.7 to 4.26.8 (#85) | ported in 0.6.21 |
| `076a874e937d772ddd9228a01911df4338afecb7` | deps: grpc 1.83.1 to 1.83.2 (#84) | ported in 0.6.21 |
| `a69d7ed6811af281ffc01e11478638d922130f0d` | keep a running core across duplicate start and keep-alive jitter | watchdog part ported in 0.6.21; the `StartOrAttach` part is rejected (see below) |
| `f751dcc82569d0543838124744ddf021890ab09d` | deps: grpc 1.83.1 to 1.83.2 (#89) | nothing to port: an empty commit whose tree equals its parent; the grpc bump itself is #84 |
| `0d3bc70f546e47ebe735b7764263f15fa023f458` | deps: x/sys 0.47.0 to 0.48.0 (#88) | ported in 0.6.21 |
| `e6dad6e3aa5c537c7bf2eb2ccd2c0fcd231a49fb` | `StartOrAttach` always restarts a running backend | rejected, waived: it only edits `StartOrAttach`, which the fork does not carry |
| `ca24af0b58f59a25870422369e36f1b31882e5d7` | `StartOrAttach` restarts only on a config change | rejected, waived: this is the "same config = heartbeat" behaviour |
| `d44ca8e986dd92cfd76c2981c06e92f6356b61ac` | `keepAliveStale` reads the clock itself | ported in 0.6.21 with the watchdog, where it applies |

The fork commits that carry the 0.6.21 ports are confirmed when the release branches are merged. Each of them carries
an `Upstream: <full sha>` trailer, which is how the drift check recognises a partial or combined port.

Notes on the ported commits:

- `a69d7ed` bundles four changes. The fork takes the one that fixes a proven race: the keep-alive watchdog re-checks
  staleness while it holds the control lock, so a late watchdog can no longer tear down the core that a concurrent
  panel `Start` has just created. Its `StartOrAttach` is refused. Its 5 s `keepAliveGrace` and its `Stop` reply of
  `&Empty{}` instead of `nil` are optional and do not change behaviour for the panel, which polls well inside
  `keep_alive`.
- `d44ca8e` only changes the helper's signature. Upstream's own `controller/controller_test.go` on `dev` still calls
  the old three-argument form and does not compile, so upstream's test file must not be copied verbatim.
- `#84` also moves the indirect `golang.org/x/crypto`, `golang.org/x/net` and `golang.org/x/text` modules.

## Rejected: `StartOrAttach` and "same config = heartbeat"

Upstream `dev` (`a69d7ed`, then `ca24af0`) replaced the unconditional restart in `Start` with `StartOrAttach`: when a
backend is running and the incoming `Start` has the same backend type, config and `exclude_inbounds`, the node only
refreshes the keep-alive and returns. The `users` field of that `Start` is deliberately ignored; the comment on
`sameBackendConfig` says users "are managed through their own sync RPCs".

The fork does not port this, for these reasons:

1. **Our panel relies on every `Start` carrying the authoritative, complete user set.** The panel's connect path
   (`_connect_single_node_local` in the panel's `app/operation/node.py`) loads the current users of the node's core
   from the database and sends them in `Start`, on first connect, on reconnect and after a panel restart. The fork's
   `Start` (`controller/rpc/base.go`) disconnects any running backend and `StartBackend` builds a new one from
   `Backend.users`. That restart is the moment a node converges to the panel's view.
2. **Porting it would leave disabled, expired and deleted users running.** Any user change the node missed while the
   panel was down, restarting, or unreachable would survive the reconnect, because the reconnect `Start` looks like a
   heartbeat and its user list is thrown away. Nothing in our panel follows `Start` with a full `SyncUsers` to repair
   that.
3. **Adding a full sync after every `Start` removes the only benefit.** On xray, `SyncUsers` goes through
   `applyConfigWithRestart` (`backend/xray/user.go`), so a `Start` plus a full sync restarts the core anyway. The point
   of `StartOrAttach`, not restarting on a panel reconnect, disappears.
4. **It does not fit the fork's extra backends.** `StartBackend` shuts the extra backends down and the panel adds them
   back after `Start`. A heartbeat `Start` that skips `StartBackend` breaks that contract in ways upstream never had to
   consider.
5. Upstream's bridge has a full-sync fence (`full_sync_fence()` and `sync_users_from_source()`) for re-sending an
   authoritative user snapshot. Our bridge fork does not carry it, so nothing would repair the user list that a
   heartbeat `Start` ignored.

What the fork keeps from the same upstream change is the part that is independent of the user list: the watchdog
serialisation (`a69d7ed` watchdog part and `d44ca8e`).

Revisit this only if the panel stops relying on `Start` for convergence, for example after the epoch protocol below
lands in the panel and the bridge with a guaranteed full snapshot on every reconnect.

## Pending upstream work: PR #78 and `user_sync_epoch`

[PasarGuard/node#78](https://github.com/PasarGuard/node/pull/78), "Harden node lifecycle and API handling", is open and
not merged on `main` or `dev`. It is tracked here and **not ported**. Its main part is a monotonic `user_sync_epoch`
that fences `Start` and every user-sync RPC, so a stale panel or bridge worker cannot overwrite a newer user snapshot.
That is the node half of a fix for the stale-snapshot class of bugs, and it needs bridge 0.10 plus panel support.

Because it is not a commit on upstream `main` or `dev`, the drift check does not see it today. When it is merged, its
commits will appear on `dev` and the check will fail until each one is ported or waived.

Before porting it, all of this has to be true:

1. **Upstream merges it**, or the owner decides to port it from the PR as it stands. The PR's own upstream CI did not
   compile or test it.
2. **The protobuf field numbers are renumbered.** The PR adds `user_sync_epoch_supported = 4` and
   `user_sync_epoch = 5` to `BaseInfoResponse`. The fork already uses 4 (`supported_backends`, repeated string) and 5
   (`filter_rules_stripped`, bool). The fields must move to free numbers, and the bridge fork must use the same
   numbers. `Backend.user_sync_epoch = 6`, `User` 4, `Users` 2 and `UsersChunk` 4 are free in the fork today.
3. **The bridge fork carries the bridge 0.10 client side**: probe the node's epoch floor, obtain a write lease, and
   send the granted epoch in `Start`, `SyncUser`, `SyncUsers` and `SyncUsersChunked`. The panel must allocate and
   persist epochs.
4. **The legacy path is decided.** In the PR, epoch-zero duplicate `Start`s stay keep-alive only, which is the rejected
   `StartOrAttach` behaviour above. Our panel is an epoch-zero client until step 3 ships, so either the legacy path
   keeps the fork's always-restart `Start`, or the node change ships only together with epoch activation.
5. **The rollout order is followed**: deploy the node first without activating positive epochs; drain every legacy
   panel and bridge worker; upgrade the shared store, the bridge and the panel together; only then resume user
   changes. Rolling back the bridge after activation needs a full node restart from an authoritative snapshot.
6. The rest of the PR is weighed separately:
   - fail closed on a missing or zero `API_KEY`: 0.6.21 carries the fork's own fix, not the PR's;
   - watchdog serialisation: taken from `a69d7ed` in 0.6.21;
   - bounded REST and user-sync stream bodies: post-auth only, and REST is unused in production;
   - surfacing a failed xray user removal: the fork already does this, with a session revoke;
   - `Xray.Restart()` under `syncMu`: not in the fork yet.

Other open upstream work that was reviewed:

- PR #86, "xray: synchronize process status checks": the fork has had an equivalent fix since node 0.6.6 (a
  lock-free `coreState` snapshot and a `waitDone` channel in `backend/xray/core.go`).

## The drift check

`scripts/upstream_drift.sh` fetches upstream `main` and `dev` into `refs/remotes/upstream/*` and lists every
non-merge upstream commit that is not reachable from the checked commit. Each one is classified:

| status | meaning |
|---|---|
| `PORTED` | a fork commit has the same `git patch-id --stable` (a verbatim cherry-pick), or a commit message in the fork's history has an `Upstream: <sha>` trailer or a `(cherry picked from commit <sha>)` line naming it (7 to 40 hex digits, prefix match) |
| `EMPTY` | the upstream commit changes no file, so there is nothing to port |
| `WAIVED` | `.github/upstream-waivers.txt` lists it with a reason |
| `UNPORTED` | none of the above; the check exits 1 |

Merge commits are skipped because their content is the commits they merge. Upstream commits the fork already has
through a merge, such as everything up to `v0.5.4`, are reachable and never listed.

### Porting an upstream commit

Add one trailer line per upstream commit to the porting commit's message, with the full SHA:

```
Upstream: a69d7ed6811af281ffc01e11478638d922130f0d
```

`git cherry-pick -x` is always recognised through its `(cherry picked from commit ...)` line. The patch-id alone
matches only when the fork's diff equals upstream's including the unchanged context lines around each change: a
cherry-pick of `#88` onto a `go.mod` that does not yet have `#84` and `#85` applies cleanly but has a different
patch-id. A partial port, or several upstream commits folded into one, is recognised only by the trailer. So write the
trailer (or use `-x`) on every port, and record a partial port in the table above.

The check trusts the trailer: it does not verify how much of the upstream commit was taken, and a port that is later
reverted still counts as ported. Reviewers check the ported part against this page.

### Waiving an upstream commit

Add one line to `.github/upstream-waivers.txt`:

```
<full 40-hex upstream sha> <category>: <reason>
```

- `category` is one of `rejected`, `superseded`, `pending` or `not-applicable`.
- The reason is mandatory and must say why, for example
  `superseded: the fork fixed this in <fork sha>` or `not-applicable: upstream release tooling`.
- The file has no comments and no blank lines; every line is a waiver. The check refuses a malformed line or a
  duplicate SHA.
- When a waived commit later turns out to be ported, the check prints a note that the waiver can be removed.

### Running it locally

```
bash scripts/upstream_drift.sh
```

- `UPSTREAM_FETCH=0` skips the fetch and uses the existing `refs/remotes/upstream/*`.
- `DRIFT_TARGET=<ref>` checks another branch or commit instead of `HEAD`.
- `UPSTREAM_BRANCHES`, `UPSTREAM_URL` and `UPSTREAM_WAIVERS` override the defaults.
- A shallow clone is refused, because commits behind the shallow boundary are invisible, which makes the result wrong
  in both directions. `DRIFT_ALLOW_SHALLOW=1` runs anyway with a warning, for a quick local look only.

The workflow runs daily, on demand, and on every pull request to `main`. It has read-only repository permissions,
uses no secrets, and pushes nothing. A red run on a pull request that did not touch upstream code means upstream moved:
port or waive the new commits in their own pull request.
