# Upstream parity

This fork tracks [PasarGuard/node](https://github.com/PasarGuard/node), and the node image builds four third-party
upstreams: Xray-core, sing-box, sing-quic and mtg. This page records which upstream changes the fork has taken, which it
has refused and why, and which upstream work is still pending. The daily drift check (`scripts/upstream_drift.sh`, run
by `.github/workflows/upstream-drift.yml`) keeps it honest. It fails when an item is neither ported nor waived with a
reason. An item is any of these:

- a commit on any PasarGuard/node branch or tag;
- a published security advisory of any of the five repositories;
- a security-looking commit on any active branch of the four third-party upstreams;
- a third-party release published after our pin.

State as of 2026-09-24, release 0.6.21. The PasarGuard/node section was re-checked on 2026-10-07 (see
[Node state on 2026-10-07](#node-state-on-2026-10-07)) and on 2026-10-10 (see
[Node state on 2026-10-10](#node-state-on-2026-10-10)). On 2026-11-02 all five sections were re-checked against the
upstream heads listed in [State on 2026-11-02](#state-on-2026-11-02); the older state sections are kept as history.

## Fork point

- The fork merged upstream tag `v0.5.4` (`e9a9ba286413c404854b80c847b588b620426660`) in `9267b04`
  ("merge upstream PasarGuard/node v0.5.4 into fork", 2026-08-31). Everything reachable from `v0.5.4` is therefore in
  the fork's history.
- On 2026-10-10 upstream `main` was still at `v0.5.4`. Upstream has since released `v0.5.5`
  (`47857d43fe777f30f9ecc5f2ffe4ca0b037507cb`, 2026-10-22) and `v0.5.6` (`675bdaa24a3a67847f2094e518f1ae4d8460aa01`,
  2026-10-27) from `dev`, and on 2026-11-02 `main` is at `v0.5.6`. The fork has not merged either tag: their commits
  are ported one by one, or waived, in the table below and in `.github/upstream-waivers.txt`.
- The other upstream branches are merged or open pull-request branches. They are listed in
  [Other upstream branches](#other-upstream-branches).

## Upstream commits after the fork point

| upstream commit | subject | status in the fork |
|---|---|---|
| `9bb6b35df2cc514385c68687bc6f771795d4b68e` | deps: gopsutil 4.26.7 to 4.26.8 (#85) | ported in 0.6.21 (`f53ffa2`) |
| `076a874e937d772ddd9228a01911df4338afecb7` | deps: grpc 1.83.1 to 1.83.2 (#84) | ported in 0.6.21 (`f53ffa2`) |
| `a69d7ed6811af281ffc01e11478638d922130f0d` | keep a running core across duplicate start and keep-alive jitter | watchdog part ported in 0.6.21 (`40248f2`); early detach in `Disconnect` and the `Stop` reply ported in 0.6.21 (`af180e2`); the `StartOrAttach` part is rejected (see below) |
| `f751dcc82569d0543838124744ddf021890ab09d` | deps: grpc 1.83.1 to 1.83.2 (#89) | nothing to port: an empty commit whose tree equals its parent; the grpc bump itself is #84 |
| `0d3bc70f546e47ebe735b7764263f15fa023f458` | deps: x/sys 0.47.0 to 0.48.0 (#88) | ported in 0.6.21 (`f53ffa2`) |
| `e6dad6e3aa5c537c7bf2eb2ccd2c0fcd231a49fb` | `StartOrAttach` always restarts a running backend | rejected, waived: it only edits `StartOrAttach`, which the fork does not carry |
| `ca24af0b58f59a25870422369e36f1b31882e5d7` | `StartOrAttach` restarts only on a config change | rejected, waived: this is the "same config = heartbeat" behaviour |
| `d44ca8e986dd92cfd76c2981c06e92f6356b61ac` | `keepAliveStale` reads the clock itself | ported in 0.6.21 with the watchdog (`40248f2`) |
| `52d5aab33ee5dd0426ccb5d202dc06c36a500162` | deps: grpc 1.83.2 to 1.84.0 (#91), plus gofmt and an upstream test fix for `keepAliveStale` | held out of 0.6.21 by the release lead on 2026-09-24 because the Go vulnerability database listed released v1.84.0 under GO-2026-6443 (golang/vulndb #6511, #6580); the database entry changed on 2026-10-06, and `3e35775` ports it on 2026-10-27 (`Upstream: 52d5aab33ee5dd0426ccb5d202dc06c36a500162`), with the cardinality tests of `eeafcd4`. The gofmt and test hunks do not apply to the fork's watchdog port |
| `be2c445f7f486043d0efc4dfbe408c4a382869f8` | xray: leave unchanged users alone during user sync (#93) | the fork's own contribution, merged upstream on 2026-09-24; ported in 0.6.21 by `40006fe`, which carries `Upstream: be2c445f7f486043d0efc4dfbe408c4a382869f8` |
| `4a9d0cf8bd38d82f391c9152f64dad6eab7ead93` | feat(wireguard): support configurable server interface MTU (#90) | new on upstream `dev` after `be2c445` on 2026-09-24; ported in 0.6.21 by `f9c4f05` (merged into release/v0.6.21 at `01eed71`), which carries `Upstream: 4a9d0cf8bd38d82f391c9152f64dad6eab7ead93` |
| `3a3d5c58efff747a12ac57b014802f4f855896df` | deps: gopsutil 4.26.8 to 4.26.9 (#95) | new on upstream `dev` on 2026-10-01; ported in 0.6.21 by `53044cc`, which carries `Upstream: 3a3d5c58efff747a12ac57b014802f4f855896df`. Same module bumps and the same `go.sum` lines as upstream (purego v0.11.1, perfstat `88456608a4f6`, go-sysconf v0.4.0, numcpus v0.12.0) |
| `1ae411b76c376aed89b54673a66b7615568f45f4` | deps: golang.org/x/sys 0.48.0 to 0.49.0 (#104) | released in v0.5.5; ported on 2026-10-27 by `d82517b`, which carries its `Upstream:` line. Same `go.mod` and `go.sum` hunks as upstream |
| `f58fce8fc8358315fb1f31eee4871fdbb0147a3d` | deps: go-chi/chi 5.3.2 to 5.4.0 (#106) | released in v0.5.5; ported on 2026-10-27 by `a074e52`, which carries its `Upstream:` line. Same hunks as upstream; the one behaviour change, the wrap writer writing the implicit 200 on Flush, does not change what the node logs or counts |
| `5cbcb5fdc16a82c819e2c095637e807c5665ea3d` | deps: go-chi/chi 5.4.0 to 5.5.0 (#108) | released in v0.5.5; ported on 2026-10-27 by `bb210c9`, which carries its `Upstream:` line. Same hunks as upstream; it only touches middleware the node does not use |
| `946ab6a5eb9d04076212cf0409bc9b4e0d5a74fd` | fix(xray): keep Shadowsocks 2022 inbounds multi-user when they start empty (#114) | released in v0.5.5; ported on 2026-11-02 by `3d18e70` (the fix) and `fb11076` (the tests, committed first and failing on main), which both carry its `Upstream:` line. The tests are upstream's with the user key literal renamed to `example-user-one-key`, because the pre-push secret guard refuses a `Password:` literal without an example marker. The placeholder code is upstream's. Since `797adcf0`, the fork's port of #58's snapshot serialiser, rebased onto this port, it sits in the empty-client case of `(*Inbound).wireClientsLocked` in `backend/xray/config.go`, where upstream has it in `snapshotInboundForSerialization`: `ToBytes` marshals a snapshot, so an empty `2022-blake3-aes` Shadowsocks inbound is serialised with one `pasarguard-placeholder` client, whose key is drawn once per process, and the live inbound is never written. `TestTheSnapshotSerialisesExactlyWhatTheMutatingSerialiserDid` compares the snapshot byte for byte with the pre-#58 serialiser, placeholder included (`config_wire_test.go`), and the #114 tests in `shadowsocks2022_tobytes_test.go` pass on it (go1.26.10, 2026-11-03). chacha20 is left alone, as upstream does |
| `71466f2a82c6c5e92d9a3c9e41944491c38abb76` | fix(xray): close restart races that drop removals or leave a stray core (#113) | released in v0.5.5; ported on 2026-11-03 by the fork's health-restart and sync-hold series pushed in `26bba5a8`; `c2ed9854` carries its `Upstream:` line, NODE-VERIFY-R3 passed the unit (verdicts/node-58-113.md). Its `canHoldUsers` hunk in `backend/xray/config.go` and its two tests `TestCanHoldUsersOnlyExcludesProtocolsWithoutUsers` and `TestUserSyncsSkipInboundsThatCannotHoldUsers` are superseded, not ported: the fork sends a delta-sync removal to an inbound only when that inbound held the user or a call to it for that user is unsettled (`stageRemovalLocked`), so an inbound that never holds users gets none of the remove calls upstream skips, whatever its protocol. `TestNodeVerifyUserSyncsSendNothingToInboundsThatCannotHoldUsers` (`backend/xray/cannot_hold_users_nodeverify_test.go`, written by NODE-VERIFY, adopted in `921eb1db`) is the evidence: `UpdateUsers` and `SyncUser` on a config with socks, mixed, http and dokodemo-door inbounds plus `API_INBOUND` send calls to the vless inbound only (finding F-R4-3) |
| `47857d43fe777f30f9ecc5f2ffe4ca0b037507cb` | v0.5.5 release: `NodeVersion` to 0.5.5 | not applicable, waived: the fork pins and versions its own `NodeVersion` |
| `bb40094ba11bfa81ae917ab22f986e90ad9bb052` | full geo files instead of the release zip's lite ones (#111) | superseded, waived: the fork pins Xray v26.3.27 and checks its geo files by sha256 |
| `d359a8f7c478044c0fb80afcda207c6d122affd5` | `StartOrAttach` restarts an unhealthy backend (#117) | not applicable, waived: it only changes `StartOrAttach`, which the fork does not carry |
| `bd745b540bd52e0300718bb294fd9f06b55f96e1` | `SyncUser` for Xray WireGuard accounts (#118) | not applicable, waived, until #64 (`e03754de`) is ported: it fixes code that #64 adds |
| `d30948378d7831827f56f174333b3f254a228d45` | refresh keep-alive on every authenticated call (#115) | released in v0.5.5; ported on 2026-11-03 by `c2e15f29`, which carries its `Upstream:` line |
| `1549c96621837b3fd7391697adc09b2c6278fac8` | a core start outlives the Start request (#116) | released in v0.5.5; ported on 2026-11-03 by `58a47bd8` (RED `d4c5c83c`), which carries its `Upstream:` line, with `7788ee80`, which pins `AddBackend` to its request's context, and `cae06171`, an abandoned Start racing a second Start and a Disconnect. The `StartOrAttach` attach half does not apply |
| `8ad52fed26401566dea3e7fd99295ccdcb4fb506` | WireGuard canonical keys, IPv6 `NODE_HOST`, counter resets (#112) | released in v0.5.5; ported on 2026-11-03 by `74e9aaa4` (keys and `NODE_HOST`, RED `9575025f`) and `ee73b4f7` (the counter rule), which carry its `Upstream:` line; `3515bf38` pins the counter resets the fork already handled. The counter rule is ported by design as a hybrid: upstream's rule (an unmarked dip in either direction restarts both counters from zero, pending delta kept) applies to the WireGuard tracker only, through `stats.NewRestartingOnAnyDip` (`ee73b4f7`, RED A `b2fe914c`; `c740d9c4` adds the Detach, hand-over and interleaving tests). OpenVPN, L2TP and MTProto keep the fork's per-direction rebase of `17dfdf5` (`stats.New()`). Measured on the real tracker: A, a WireGuard peer re-created without a mark, true traffic 500/300: the per-direction rebase reported 200/100, upstream's rule 500/300. B, an OpenVPN management socket reconnect, whose `resetVolatileState` drops open sessions without settling them into `closedTotals`: true traffic at most 53 MB / 6 MB, upstream's rule reported 10293 MB / 1030 MB (it re-bills the closed history), the per-direction rebase exactly 53 MB / 6 MB; RED B `4bb90af6` holds the OpenVPN tracker to that |
| `4b0bc4f6940cfb8c6ac3bf339f52f98b661df381` | ci: docker/login-action 4.6.0 to 4.7.0 (#119) | released in v0.5.5; ported on 2026-11-03 by `3f9afdfb`, which carries its `Upstream:` line |
| `7be885ff5410ac7fdb92fa21cf7901a416be5f05` | xray: synchronize process status checks (#86) | new on upstream `dev` on 2026-10-04. The `core.go` change is superseded, waived: upstream makes `Started()` take `c.mu`, which the fork refuses (see [Superseded: #86](#superseded-86-started-under-the-core-mutex)). Upstream's tests (`backend/xray/core_started_test.go`, adapted to the fork's state snapshot) and its `-race` CI step are ported in 0.6.21 |
| `f72ac0efacdbbfa155a1d491cceb8ce4908eab13` | fix(wireguard): remove the peer when a user loses WireGuard access (#99) | new on upstream `dev` on 2026-10-09, and the `dev` head on 2026-10-10. Ported in 0.6.21 by `cbb63a0` (the fix) and `bbc488e` (upstream's two tests, committed first and failing), which both carry `Upstream: f72ac0efacdbbfa155a1d491cceb8ce4908eab13`. The code and the tests are upstream's; upstream's explanatory comments are not carried, per the fork's no-comments rule. `4f0bbc5` adds the fork's interleaving test. See [Node state on 2026-10-10](#node-state-on-2026-10-10) |
| `03f66e9b4817a961d3085134b3e1d532fd57862d` | deps: grpc 1.84.0 to 1.85.0 (#122) | on upstream `dev` only, in no tag (2026-11-02). Held under the release-only rule and waived as `pending` until 2026-11-30; the waiver says `dev-only`, so the check turns it `UNPORTED` the day an upstream tag contains it. See [State on 2026-11-02](#state-on-2026-11-02) |
| `68e84193ace5216cbe8faf572fa3533402005cb2` | deps: gopsutil 4.26.9 to 4.26.10 (#121) | on upstream `dev` only, in no tag (2026-11-02). Held and waived like #122 |
| `ebb0d02944bdf4f7166819d31466a224f0daf7e2` | fix(wireguard): fix apparmor permission denied in e2e tests | on upstream `dev` only, in no tag (2026-11-02). Superseded, waived: the fork fixed the same failure in `b8679894` four days earlier; the AppArmor-unloading half is not taken. See [State on 2026-11-02](#state-on-2026-11-02) |

The fork commits that carry the 0.6.21 ports are `f53ffa2` (the three dependency bumps), `40248f2` (the watchdog) and
`af180e2` (the rest of `a69d7ed` apart from `StartOrAttach`).
Each carries an `Upstream: <full sha>` trailer, which is how the drift check recognises a partial or combined port.
The toolchain moved to Go 1.26.6 in `345febd` (fixes GO-2026-6090), with a govulncheck CI job from `47ef6ee`.
On 2026-10-10 it moved to Go 1.26.9 in `4c37a93`, with golang.org/x/net 0.60.0 in `1a62e65`, which together fix
GO-2026-6617, 6613, 6612, 6611, 6610, 6608, 6607, 6605, 6603, 6600 and 6599; Go 1.26.7 and 1.26.8 were released but
fix none of them. `a0b54fd` added `scripts/toolchain_floor.sh` to the repo-hygiene job, which refuses any pin below
Go 1.26.9 or x/net 0.60.0.
On 2026-10-26 it moved to Go 1.26.10 in `410815f`, which fixes GO-2026-6809 (CVE-2026-111943, crypto/x509), and
`5f9706b` raised the floor guard to 1.26.10. `4189871` makes the guard find the xray-builder stage case-insensitively
and read the Xray patch order only from that stage (F-SEC1026-V1).

Notes on the ported commits:

- `a69d7ed` bundles four changes. The fork takes three of them and refuses `StartOrAttach`.
  - `40248f2` takes the change that fixes a proven race. The keep-alive watchdog re-checks staleness while it holds the
    control lock, so a late watchdog can no longer tear down the core that a concurrent panel `Start` has just created.
    It also takes the 5 s `keepAliveGrace`.
  - `af180e2` takes the early detach in `Disconnect` and the `Stop` reply of `&Empty{}` instead of `nil`.
  - An earlier revision of this page refused the early detach because "every caller of `Disconnect` now holds the
    control lock". That was false. User-sync RPCs never take the control lock, and `cmd/node`'s deferred
    `Disconnect` held no lock. A user sync that arrived while `Disconnect` was shutting cores down therefore reached
    only the dying primary, was acknowledged, and was missing from the core that `Start` then built (finding C2 of the
    0.6.21 adversarial review).
  - The fork does not rely on the early detach alone. `af180e2` also adds a controller lifecycle lock. Every user-sync
    message or batch holds it as a reader. `Start`, `Stop`, the watchdog, `AddBackend`, `RemoveBackend` and the
    exit-time `Close` hold it as a writer, across the whole teardown, build and install. So a sync either completes on
    the old core before the change starts, or waits and is applied to the new core. With no core running, a sync is
    refused with `Unavailable`; it is never acknowledged.
  - Under that lock the early detach is not what makes user syncs safe. The fork takes it because it matches
    upstream, and because stats, `GetBaseInfo` and the other readers stop seeing a core that is going away.
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
serialisation (`a69d7ed` watchdog part and `d44ca8e`), the early detach in `Disconnect`, and the `Stop` reply.

Revisit this only if the panel stops relying on `Start` for convergence, for example after the epoch protocol below
lands in the panel and the bridge with a guaranteed full snapshot on every reconnect.

## Pending upstream work: PR #78 and `user_sync_epoch`

[PasarGuard/node#78](https://github.com/PasarGuard/node/pull/78), "Harden node lifecycle and API handling", is open and
not merged on `main` or `dev`. It is tracked here and **not ported**. Its main part is a monotonic `user_sync_epoch`
that fences `Start` and every user-sync RPC, so a stale panel or bridge worker cannot overwrite a newer user snapshot.
That is the node half of a fix for the stale-snapshot class of bugs, and it needs bridge 0.10 plus panel support.

Its commits live on the contributor's fork, not on a PasarGuard/node branch or tag, so the drift check does not see
it today. When it is merged, its commits will appear on `dev` and the check will fail until each one is ported or
waived.

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

## Superseded: #86, `Started()` under the core mutex

Upstream `7be885f` (#86, merged into `dev` on 2026-10-04) closes the data race between `Core.Started()` and the
process watcher: `Started()` now takes `c.mu`, reads a `waitDone` channel instead of `ProcessState`, and `Start` and
`Stop` call an unlocked `startedLocked()`.

The fork closed the same race on 2026-09-10 and refuses the lock:

- `9495ea7` first made `Started()` take `c.mu`, as #86 does. `cc27180` replaced that with a lock-free design:
  `Start` and `Stop` publish an immutable `{cmd, waitDone}` snapshot into an `atomic.Pointer[coreState]` under `c.mu`,
  and `Started()` reads the snapshot without any lock. Liveness comes from `waitDone`, which the watcher closes right
  after `cmd.Wait()` returns, never from `ProcessState`.
- `Started()` is called on every REST and gRPC request (middleware), by the health checks and by the panel status
  endpoint. `Stop()` holds `c.mu` across the kill, a wait of up to 5 s on `waitDone`, `killProcessTree` and
  `verifyProcessDead`. With #86 every one of those callers waits behind a stopping core.

What the fork takes from #86 is its tests, which hold for both designs:

- `backend/xray/core_started_test.go` keeps upstream's helper process and both tests. The helper publishes the
  snapshot (`publishStateLocked()`) after it installs the child, because the fork's `Started()` reads only the
  snapshot.
- `TestCoreStartedProcessExit` checks the fork's stronger contract: `Started()` must answer within 2 s while the test
  holds `c.mu`. Upstream checks `startedLocked()` there instead, because its `Started()` would block.
- The `-race` CI step `go test -race ./backend/xray -run '^TestCoreStarted' -count=10` runs in the `test` job of
  `run-tests.yml`, as upstream added it.
- Mutants of `core.go` that the tests kill under `-race`: #86's own design (`Started()` takes `c.mu`): "Started
  blocked while the core mutex was held", 3 of 3 runs; the pre-0.6.6 `ProcessState` read: DATA RACE, 3 of 3 runs; a
  snapshot that ignores `waitDone`: "exited child reports running", 3 of 3 runs.

Other open upstream work that was reviewed:

- PR #86 was merged as `7be885f`; see above.

## Other upstream branches

The drift check reads every PasarGuard/node branch and tag, not only `main` and `dev`. On 2026-10-07 the branches were
as below; on 2026-10-10 only `dev` had moved, to `f72ac0efacdbbfa155a1d491cceb8ce4908eab13` (#99). On 2026-11-02 only
`main`, `dev`, `chunck`, `debug` and `pr-61` exist; the heads of that day are in the table, and the branches deleted
since are marked.

| branch | head | status in the fork |
|---|---|---|
| `main` | `675bdaa24a3a67847f2094e518f1ae4d8460aa01` (`v0.5.6`, 2026-11-02; `e9a9ba286413c404854b80c847b588b620426660`, `v0.5.4`, on 2026-10-10) | `v0.5.4` merged; the `v0.5.5` and `v0.5.6` commits are ported or waived one by one |
| `dev` | `ebb0d02944bdf4f7166819d31466a224f0daf7e2` (2026-11-02; `f72ac0efacdbbfa155a1d491cceb8ce4908eab13` on 2026-10-10; `7be885ff5410ac7fdb92fa21cf7901a416be5f05` on 2026-10-07) | see the table above |
| `api-key` | `11baa23f1caf1173907ba7cb6dc963c5db660bad` (deleted upstream by 2026-11-02) | fully in the fork's history, nothing to list |
| `chunck` | `c194114eb1971d2e252fa1d117d655122c356d35` | `c194114` is a branch-only prototype; upstream squash-merged chunked user sync as `4f8568b` (#23), which the fork has. Superseded, waived |
| `debug` | `8047fad98ec7e9955a405a97bf90a2411d8d6678` | `af0846e` and `8047fad` add diagnostic logging and were never merged upstream. Not applicable, waived |
| `fix/xray-api` | `d09e5064d02555735ee815c8483dc88c42a8f669` (deleted upstream by 2026-11-02) | `2ae8ca9` and `d09e506` are pre-squash commits of `2cecc87` (#31), which the fork has. They were waived as superseded; no upstream ref reaches them any more, so the run of 2026-11-02 matched nothing for them and their waivers were removed |
| `refactor/inbound-map-storage` | `72f7b4f0d0acde96cc642d2dd73defe5540921e6` (deleted upstream by 2026-11-02) | `0c44dc6` and `72f7b4f` are pre-squash commits of `c3f9e61` (#24), which the fork has. Waived as superseded until 2026-11-02, when their waivers were removed for the same reason |
| `pr-61` | `4eb26fbd00387c88a4bcaf5c1d9e9e8dc4604b62` | 15 commits of the open PR #61. Rejected, waived (see below) |
| `masque` | `b7442879ef0f67b692474e0e668d65f29df8de49` (deleted upstream by 2026-11-02) | new on 2026-10-01, a feature branch that was not on `dev`. `243b818` adds a MASQUE proxy type with new panel-node fields in `common/service.proto`; `f0877ce` moves xray-core to the `v26.9.30` prerelease for native MASQUE accounts; `b744287` merges `dev` into it. Upstream squash-merged the branch as `88d5e75db621` (#96, "add Masque proxy support"; its message lists both commits), released it in `v0.5.6` and deleted the branch. The three branch waivers (`pending` until 2026-11-06) matched nothing on 2026-11-02 and were removed; the feature is tracked by the `pending` waiver of `88d5e75` (until 2026-11-20, owned by NODE-SYNC-055, ported with the Xray pin move) |

Of the 44 upstream tags on 2026-11-02, the 42 from `v0.0.1` to `v0.5.4` are in the fork's history. `v0.5.5` and `v0.5.6`
are not merged; their commits are ported or waived one by one.

PR #61, "add MTProto proxy backend via telemt", is open and not merged. It adds `MTPROTO = 2` to `BackendType` and a
telemt-based backend. The fork already ships its own MTProto backend on its mtg fork as `MTPROTO = 4`
(`backend/mtproto`): multi-secret, live `UpdateSecrets`, and plain mode. Porting #61 would collide on the enum value and
replace a working backend, so it is rejected. Revisit it if upstream merges it.

Open upstream pull requests whose heads live on contributors' forks are not branches of PasarGuard/node, so the check
does not see them. On 2026-09-24 they were:

- #93 (the fork's own contribution);
- #90 (WireGuard interface MTU);
- #86 and #78 (assessed above);
- #64 (WireGuard peers through the Xray user manager);
- #61 (also a branch, above);
- #58 (performance changes);
- #55 (log fan-out).

#90, #64, #58 and #55 are not assessed yet. When any of them is merged, its commits appear on `dev` and the check fails
until they are ported or waived.

## Third-party upstreams

The node image also builds four third-party upstreams. U6 (2026-09-24) found two HIGH Xray fixes that had shipped
upstream months after our pin, and nothing had noticed them. The drift check therefore watches these upstreams too.

### Pins

The script reads every pin from the checked commit, never from a hardcoded value, through the same Dockerfile reader as the xray build (so a declaration on a continuation line or behind a CRLF counts), and the way docker resolves it: from the `ARG` declaration in the stage that builds (`xray-builder` for `XRAY_COMMIT`, `singbox-builder` for the sing-box pins), falling back to a global declaration only when that stage declares the `ARG` without a value. Two declarations with a value in that stage, or two global ones it falls back to, stop the run with exit 2, and so does a Dockerfile in which that stage does not exist the way docker reads it (a continuation line can swallow its `FROM`):

| component | upstream | pin read from | pin on 2026-11-03 | line branch |
|---|---|---|---|---|
| `xray` | XTLS/Xray-core | `Dockerfile` `ARG XRAY_COMMIT` | `d2758a023cd7` (`v26.3.27`) | `main` |
| `sing-box` | SagerNet/sing-box | `Dockerfile` `ARG SINGBOX_VERSION`, an upstream tag | `v1.13.21` (`628cb31ffa79`); fork `SINGBOX_COMMIT` `3f35dc15e0b4` | `oldstable` |
| `sing-quic` | SagerNet/sing-quic | the `require` line of the sing-box fork's `go.mod` at `SINGBOX_COMMIT`, fetched from `SINGBOX_REPO` | `v0.6.5` (`d83826c306d7`) | `main` |
| `mtg` | 9seconds/mtg | the node `go.mod` `require` of `github.com/9seconds/mtg/v2` (a pseudo-version commit); the `replace` commit on Free-Guy-IR/mtg must contain it | `d095108334de` (fork `7d0f004a01dd`) | `master` |

Three rules keep the pins honest:

- **sing-box.** The fork is a squashed import, so reachability cannot tell which upstream release it is based on.
  `SINGBOX_VERSION` must always name the upstream release the fork's tree is based on.
- **sing-quic.** The sing-box fork `replace`s sing-quic with its `third_party` copy, which makes the `require` line
  inert, so nothing else keeps it honest. When the vendored copy is upgraded, the `require` line must be bumped to the
  same version.
- **mtg.** The upstream commits that the fork has merged are excluded as well. The check refuses to run if the fork
  commit does not contain the required upstream commit.

### Rules

For each component the script fetches every upstream branch and tag. It fetches commit objects only
(`--filter=tree:0`), into a temporary bare repository. It then checks three kinds of item.

1. **Advisories.** It checks every published advisory of the repository
   (`GET /repos/<owner>/<repo>/security-advisories?state=published`), plus every reviewed advisory in the GitHub
   Advisory Database for the Go module (`GET /advisories?ecosystem=go&affects=<module>`). Withdrawn advisories are
   skipped.
   - The status is `FIXED` when a commit reachable from the pin names the GHSA id in its message.
   - It is `PORTED` when an `Upstream: <GHSA id>` line names it, or when a ported upstream commit names it in its
     message.
   - It is `WAIVED` when the waiver file lists it, and `UNPORTED` otherwise.

   The same advisory check runs for PasarGuard/node, with the fork's own history as the pin.
2. **Security-looking commits.** This rule covers every non-merge commit that meets all three conditions:
   - it is on the line branch, or on any branch whose tip is newer than the pin commit ("active since the pin");
   - it is not reachable from the pin (or from the fork commit);
   - its message matches one of the patterns below.

   The patterns are case-insensitive extended regular expressions, matched against every line of the subject and the
   body. Such a commit is `PORTED` by an `Upstream:` line, or `WAIVED`, or else `UNPORTED`.
3. **Releases.** This rule covers every tag named `vX.Y.Z`: digits and dots, an optional leading `v`, and no
   pre-release suffix. The tag must be created after the pin commit, and its commit must not be reachable from the pin
   (or the fork commit). Only a waiver on the tag's commit clears it. Moving the pin past it removes it.

| pattern | meant to catch |
|---|---|
| `GHSA-[[:alnum:]]{4}-`, `CVE-[0-9]{4}-` | advisory references, including advisories that are not published yet |
| `security`, `vulnerab`, `exploit` | explicit security wording |
| `panic`, `crash`, `denial[- ]of[- ]service`, `(^\|[^[:alnum:]])d?dos([^[:alnum:]]\|$)` | remote crashes and DoS |
| `(^\|[^[:alnum:]])races?([^[:alnum:]]\|$)`, `deadlock` | data races and lock-ups |
| `overflow`, `out[- ]of[- ]bounds`, `unbounded\|not bounded`, `untrusted`, `exhaust`, `infinite loop`, `leak` | memory safety and resource exhaustion |
| `bypass` | access-control bypasses |

Why the rules look like this:

- **The QUIC-sniffer advisories are not published.** On 2026-09-24 both advisory APIs answer 404 for
  GHSA-hrp5-2rwj-wvmv, GHSA-xqmr-94vq-cxvx and GHSA-9h6x-9r9m-5qw2. They are visible only in the messages of
  `e5a9fb7` and `8f15190`, and rule 2 catches those.
- **Keywords miss fixes with bland subjects.** `711aea4` (XFF trust) and the grpc bump `cecc88f` match no pattern.
- **Squash merges hide fixes too.** Xray PR #6640 was merged as "Config: Fix some issues", and it surfaced only
  through its branch commit "Fix some other panic". That is why every active branch is scanned, not only the line
  branch.
- **Releases catch what the keywords miss.** Rule 3 forces an explicit decision on every release, so a fix with a bland
  subject still reaches a human through its release.
- **The keyword rule is a tripwire, not an audit.** A pin move still needs a full review of its range, as U6 did.
- **Why the active-branch cut.** A branch whose tip predates the pin cannot hold anything newer than the pin. The cut
  depends only on the pin and the upstream state, so it is deterministic. Moving the pin shrinks it.

### How a port is recognised

- **Commit messages.** An `Upstream: <sha>` line (7 to 40 hex digits, prefix match) or a
  `(cherry picked from commit <sha>)` line in any commit message of the checked history.
- **Patch files.** An `Upstream: <sha or GHSA id>` line in the header of a patch file, meaning the lines before the
  first `diff --git`. Lines inside the diff are ignored. A header counts only for a file that the build really applies:
  - the Makefile must apply `patches/xray-*.patch` on a line that is not a comment, without `--check`, `--stat`,
    `--numstat`, `--summary`, `-R` or `--reverse`, and without ignoring the failure (`|| true`);
  - the patch must be a regular file in the tree; a symlink under `patches/` never counts, because its blob is the
    link text while docker copies the file the link points at;
  - the Dockerfile at the checked commit is read the way BuildKit reads it (a trailing CR stripped from every line,
    continuation lines joined, comment lines dropped), and the xray build is accepted only when the check can follow
    it end to end. This is an allowlist, not a list of known tricks: anything it does not recognise fails closed, and
    the run then prints one line, `upstream-drift: xray: no patch header counts, because the Dockerfile's xray build
    is not one the check can follow: <instruction>`, naming the first instruction it refused. What it accepts:
    - no parser directive before the first instruction, in either form (`# syntax=`, `# escape=` in any case, or
      `// syntax=`); no byte outside printable ASCII and tab anywhere (a BOM, a vertical tab or a no-break space can
      change what BuildKit treats as a comment or a directive); no heredoc on any `RUN`, `COPY` or `ADD`;
    - exactly one stage named `xray-builder`, built on an external image pinned by digest (or `scratch`), never on
      another stage of the file;
    - in that stage only `ARG` (names `TARGET*`, `BUILD*`, `XRAY_*`), one `WORKDIR` before the patches are copied,
      `COPY patches/<name> /patches/<name>`, and shell-form `RUN` without flags. Copy destinations are cleaned the way
      docker cleans them (`//`, `.`, `..`) before they are compared, and any copy onto `/patches` other than such an
      exact self-copy fails closed;
    - each `RUN` is split on its operators outside quotes, `$(...)` substitutions included, and may join commands
      only with `&&` and `|`. Before the first apply it may set up the source, down to the arguments: `git init`
      of `.`, `git remote add origin https://...`, `git fetch` of exactly one `XRAY_COMMIT` reference with only quiet,
      depth and no-tags flags, `git checkout` of `FETCH_HEAD`, and `apk` with plain package names, so the source is
      always the pinned commit; then `git apply --verbose /patches/<name>` exactly, once per patch, reached only through
      `&&`; read-only checks (`grep`, `test`, `true`, also in pipelines; a substitution may run only these and
      `git rev-parse HEAD`); and exactly one `go build` after every apply, with an absolute `-o` path and without
      `-overlay`, `-toolexec`, `-modfile`, `-mod`, `-C` or `-pgo`. Any other command, a redirection, a subshell, a backquote, a `;`, `||` or `&`, a setup command
      after an apply, or any mention of `/patches` outside an exact apply fails closed;
    - outside that stage, the only copy from `xray-builder` is the last stage copying exactly the `go build -o`
      path to `/usr/local/bin/xray`; every other `COPY` of a file named `xray` fails closed, no `RUN` touches
      `bin/xray`, no instruction anywhere names `XRAY_EXECUTABLE_PATH`, the last stage copies no `.env` file and no
      whole directory of the build context (the node loads `.env` from its working directory), and after the xray
      `COPY` the stage only copies files to explicit paths that are neither `/usr/local/bin/xray` nor a directory
      above it, and sets `ENV`, `ENTRYPOINT`, `CMD`, `LABEL`, `EXPOSE`, `USER`, `WORKDIR`, `HEALTHCHECK` or
      `STOPSIGNAL`.
    The real Dockerfile matches this shape. A change to the xray build that the check does not recognise adds an
    `UNPORTED xray build` item that names the refused instruction, and also turns every patch-ported item `UNPORTED`,
    until the Dockerfile is written in this shape or the parser learns the new one. That is the intended direction of
    failure: the run never passes on an xray build it could not follow.
    The check's awk programs use no regular-expression interval such as `{64}`, because mawk 1.3.4 20200120 (the
    awk of Ubuntu 22.04) reads one as plain text and would refuse every digest-pinned builder. A test runs the check
    under an awk that rejects any program with an interval.
  It then counts only for the xray component. A header in a patch that nothing applies, or one that names a node,
  sing-box, sing-quic or mtg commit, ports nothing; those components are ported by commit trailers.
- **Carried in 0.6.21.** The Xray patch files and commits of 0.6.21 carry `Upstream:` lines for:
  - `e5a9fb752e0dcc127dd1740316c853571c16052f` and `8f15190c230fe6975c9b18e31316c6bc494f3863`, the QUIC sniffer;
  - `711aea4e34b0a00073d2fea6577f28a5d7c9b5eb`, trusted X-Forwarded-For;
  - `cecc88f43cb12f7ba0517080f3d82daaa07f816a`, grpc 1.83.2, in the header of `xray-grpc-1.83.2.patch`;
  - `8ee131cbbba4e9ed26e62fe3900cb9fdfb06ab89`, the HTTP inbound 1xx panic;
  - `a2cec2e580e8ea1900701b11b425e0351de2ec71`, the DNS panic on a name over 255 bytes;
  - `d878fc83f8be0f56cebbc5fb9a9baf0aa48d9990`, the SS2022 dispatch panic;
  - `0bafca94868a4ec38cd3dba0f2ba7ec6f44bbf18`, the stats `GetOrRegister*` races, as an adapted backport;
  - `24e6f6d5513382d92256a80c5f2e7a6e16e5d6c7`, x/crypto 0.57.0 and x/net 0.59.0. Since 2026-10-10 the trailer is in
    `xray-x-net-0.60.0.patch`, which goes on to x/net 0.60.0 ahead of upstream;
  - `77f98eba0978cbcb425e2f8ec3cec86bd5aa8444` and `eef6e63bc16d6e97fd98178556d49aad7978809a`, the XHTTP client races,
    in the header of `xray-xhttp-client-race.patch`. Their `pending` waivers were removed with the port.
- **What the check shows.**
  - The run of 2026-09-24 marks the six keyword commits among them as `PORTED`: `e5a9fb7`, `8f15190`, `8ee131c`,
    `0bafca9`, `a2cec2e` and `d878fc8`.
  - `711aea4`, `cecc88f` and `24e6f6d` match no keyword. Their trailers document the port, and their releases stay
    waived until the pin moves.
  - If the QUIC-sniffer advisories are published later, they show as `PORTED` automatically, because `e5a9fb7` and
    `8f15190` name them.
  - `598bde7` is not carried: its race is fixed by a fork patch with no `Upstream:` line, see
    [Fork deviations in the Xray build](#fork-deviations-in-the-xray-build).
- **sing-box and sing-quic.** The normal port is a pin move: the fork is rebased on a newer release, and
  `SINGBOX_VERSION` or the `require` line is bumped. The items then become reachable from the pin and disappear. A
  single fix cherry-picked into the fork without a pin move needs an `Upstream:` line in the node commit that moves
  `SINGBOX_COMMIT`.

### State on 2026-09-24

Heads checked:

- PasarGuard/node `dev` `4a9d0cf8bd38d82f391c9152f64dad6eab7ead93` and `main` `e9a9ba286413c404854b80c847b588b620426660`
  (`v0.5.4`). Upstream `dev` moved past `be2c445f7f48` during the day; `4a9d0cf` (#90) is the new commit.
- XTLS/Xray-core `main` `60e2a0c502d3f5f191d71b4343efe09c32338399`, and 56 branches active since the pin.
- SagerNet/sing-box `oldstable` `78d3eeedacdda487eca7f2793e29973cb2f8a86c`, and 8 active branches, including
  `stable` and `testing`.
- SagerNet/sing-quic `main` `6a3a24d65b99587fad1d4cdd567c88f212acdd63` and `dev`.
- 9seconds/mtg `master` `d095108334deb1512f4b7aece588f6e00f5ec428` (equal to the pin), and one PR branch.

**xray**

- Advisories: one, GHSA-5wf9-h793-w73c. It was waived as not applicable on 2026-09-24: a client-side
  `pinnedPeerCertSha256` check that only matters in users' apps. Since 2026-10-27 it is ported:
  `xray-pinned-ca-servername.patch` carries `64fada32b5b9` (see below), and both waivers are removed.
- Security-looking commits: 29.
  - 6 are ported by 0.6.21 patches: `e5a9fb7`, `8f15190`, `8ee131c`, `0bafca9`, `a2cec2e` and `d878fc8`.
  - 9 are pre-squash branch commits, waived as superseded.
  - 8 are waived as pending the v26.9.x pin move: `598bde7` (its race is fixed by the fork patch), `e61eeae`,
    `dbb1ea3` and the client-side dialer fixes.
  - 6 are not applicable: TUN, the client-side pinned-CA check, and fixes to code that only exists after the pin,
    `412898f` among them.
- Releases: `v26.4.13` to `v26.9.9`, waived as `pending: breaking (d7fa207 bans unencrypted outbounds; REALITY
  minimum client changes); planned project with xray-config-kit`.

**sing-box**

- Advisories: GHSA-r5hm-mp3j-285g, fixed in 2023, waived.
- Security-looking commits: 26.
  - 8 on `oldstable`: pending node 0.6.22, which moves the fork to `v1.13.21` with sing-quic `v0.6.5`.
  - 15 on the 1.14 `stable` line and 3 on the newer lines: pending the 1.14 move, or not applicable (client-app
    daemon, macOS, services that 1.13 lacks).
- Releases:
  - `v1.13.15` to `v1.13.21`: pending node 0.6.22;
  - `v1.14.0`, `v1.14.1` and `v1.14.2` (published 2026-09-24): pending a separate 1.14 move. `v1.14.2` adds no
    security-looking commit that is not already waived. Its HTTP proxy inbound panic (`bea01fe`) and HTTP/2 transport
    races (`18b0317`) are server-side code that 1.13 also has, but the node runs only Hysteria2 on sing-box.
    `3f0958b` (read loops spinning on persistent errors) and `afb0b30` (traffic counters on interrupted splice copies)
    match no keyword, and they are to be checked against 1.13 at the 1.14 move.

**sing-quic**

- Advisories: none.
- Security-looking commits: 5, all pending node 0.6.22. They are `2afc335`, the Salamander race (u6-3), plus four
  leak and handshake-bound fixes.
- Releases:
  - `v0.6.3` and `v0.6.5`: pending node 0.6.22;
  - `v0.7.0`: pending with sing-box 1.14.

**mtg**

- The node pins the fork `Free-Guy-IR/mtg` master. The drift check reads upstream `9seconds/mtg`, whose `master`
  carries `d2722ef5` (PR #549, "doctor: validate fronting domain TLS certificate"). It is not reachable from the
  fork pin and its message matches the `expired`/`untrusted` keywords, so it is flagged. It is waived as
  `not-applicable`: it changes only `internal/cli/doctor.go`, the `mtg doctor` self-diagnostic command. The node
  links the mtg library packages (`mtglib`, `network/v2`, `antireplay`, `ipblocklist`, `mtglib/obfuscation`) and
  never builds or runs the `doctor` CLI, so the fronting-domain certificate probe is unreachable at the node's
  runtime.
- No mtg advisory and no mtg release after the pin.

**node**

- `52d5aab` (grpc 1.84.0, #91) was held out of 0.6.21 and waived as `pending` while the Go vulnerability database
  listed released v1.84.0 under GO-2026-6443 (golang/vulndb #6511, #6580). The entry changed on 2026-10-06, and
  `3e35775` ports it on 2026-10-27.
- `be2c445` (#93) and `4a9d0cf` (#90) are ported by `40006fe` and `f9c4f05`. The node section of the run of 2026-09-25 lists
  33 upstream commits outside the target: 7 ported, 1 empty, 25 waived, 0 unported. It also lists 5 fork commits with
  an upstream parent, all allowlisted, and 1 upstream merge with no changes of its own.

### Node state on 2026-10-07

Heads checked with `git ls-remote https://github.com/PasarGuard/node.git` on 2026-10-07:

- `dev` `7be885ff5410ac7fdb92fa21cf7901a416be5f05`, `main` `e9a9ba286413c404854b80c847b588b620426660` (`v0.5.4`, still
  the newest of the 42 tags);
- `api-key` `11baa23f1caf`, `chunck` `c194114eb197`, `debug` `8047fad98ec7`, `fix/xray-api` `d09e5064d025`, `pr-61`
  `4eb26fbd0038`, `refactor/inbound-map-storage` `72f7b4f0d0ac`, all unchanged since 2026-09-30;
- `masque` `b7442879ef0f67b692474e0e668d65f29df8de49`, a new branch.

New upstream commits since 2026-09-24:

- `3a3d5c5` (#95, gopsutil 4.26.9) on `dev`: ported by `53044cc`.
- `7be885f` (#86) on `dev`: tests and CI step ported, the `core.go` change superseded and waived.
- `243b818`, `f0877ce` and the merge `b744287` on `masque`: pending, waived until 2026-11-06.

0.6.21 takes five server-side Xray fixes early as patches. Each fix was written test-first: its test fails on the pin
and passes with the fix.

- `8ee131c`, the HTTP inbound 1xx panic: `xray-http-100-continue.patch`, verbatim.
- `a2cec2e`, the DNS query panic on a name over 255 bytes: `xray-dns-long-domain.patch`, verbatim.
- `d878fc8`, the SS2022 inbound invalid-destination panic: `xray-ss2022-dispatch.patch`, verbatim.
- `0bafca9`, the stats `GetOrRegister*` races, which cost billed bytes and online-map IPs:
  `xray-stats-register-race.patch`, an adapted backport (see below).
- `598bde7`, the Router API data race: upstream's refactor does not apply to the pin, so `xray-router-race.patch` is
  a fork fix. The commit stays waived as pending for the rest of the refactor.
- `64fada32b5b9`, GHSA-5wf9-h793-w73c (GO-2026-6651), taken on 2026-10-27 as `xray-pinned-ca-servername.patch`.
  A pinned CA with no server name verified the chain with an empty DNSName, so any certificate that CA issued, for
  any host, was accepted. The patch is upstream's three hunks without comments. The Hysteria hunk is adapted to
  the pin, whose client struct also carries `ctx`. Five tests sit in the Xray tree. The pinned-CA test fails on the
  pin (a certificate for another host is accepted) and passes with the patch. The Dockerfile guards the
  verifier check and both dialer call sites. The 10-21 snapshot of prod core 1 has no `pinnedPeerCertSha256` key,
  so the bug was not live there.

On 2026-10-10 the XHTTP client races `77f98eb` and `eef6e63` were taken early too, as
`xray-xhttp-client-race.patch`. They had been waived as pending the v26.9.x pin move, because no production core
dials out over XHTTP. That waiver did not survive CI: since CI runs `make test_xray_patches` with `GOFLAGS=-race`,
upstream's own `TestDialWithRemoteAddr` failed on these races (run 37916395457).

The lower-risk items were checked against the pinned code on 2026-09-24:

- `e61eeae` is pending. It is not a config-time failure only: at the pin, a tunnel inbound that listens on a unix
  socket with no port panics on every accepted connection (`proxy/dokodemo/dokodemo.go:90-91`). The node's own API
  inbound is a TCP loopback tunnel (`backend/xray/config.go:839`), so only a panel-configured unix-socket tunnel inbound
  is exposed. The one-line dokodemo half of the fix applies cleanly if it is wanted earlier.
- `dbb1ea3` is pending. Upstream patches the post-pin `netstack.go`, but the pin has the same send-on-closed-channel
  pattern in `proxy/wireguard/gvisortun/tun.go` (`:164` and `:177`). It needs an xray WireGuard proxy on the gVisor
  netstack that is closed with a packet in flight.
- `412898f` is not applicable. The unlocked `h.dev` check it fixes came with the post-pin refactor `8626311`. At the
  pin, the check runs under `h.wgLock` (`proxy/wireguard/client.go:94-140`).
- `d42c981`, the DomainMatcher race, fixes code that the geodata refactor `82624bc` added after the pin, so it does not
  apply.

### Node state on 2026-10-10

Heads checked with `git ls-remote https://github.com/PasarGuard/node.git` on 2026-10-10 (last at 22:06 UTC):

- `dev` `f72ac0efacdbbfa155a1d491cceb8ce4908eab13`, `main` `e9a9ba286413c404854b80c847b588b620426660` (`v0.5.4`, still the
  newest of the 42 tags);
- `api-key`, `chunck`, `debug`, `fix/xray-api`, `masque`, `pr-61` and `refactor/inbound-map-storage` unchanged since
  2026-10-07.

The one new upstream commit is `f72ac0e` (#99) on `dev`. It is ported:

- **The bug.** A partial sync (`SyncUser`, `UpdateUsers`, chunked updates and `UpdateUsersAndRestart`) only marked as
  touched the users that `normalizeUsers` kept, and that function drops a user with empty `peer_ips` or with no
  WireGuard credentials. The panel sends exactly that shape when a user loses WireGuard access: its serializer
  (`app/node/user.py`) sends `peer_ips` as an empty list when the user has no WireGuard IPs, and partial syncs serialize
  every protocol. The user's peer then stayed on the device and in the peer store until the next `Start` or full sync,
  its freed IP could be given to another user while the old peer still held it, and handing its key to another user in
  the same batch failed the batch with "already assigned to user".
- **The port.** `bbc488e` adds upstream's two tests first; on `8959c50` both fail ("expected exactly one Remove for the
  stale peer, got []" for both shapes, and "already assigned to user"). `cbb63a0` takes upstream's change: every user
  with a non-empty email in the request counts as touched, and only the normalized users become desired peers. The fork
  keeps its own stats flush before a partial sync removes peers, so the removed peer's last traffic is sampled before
  its stats entry is marked deleted.
- **The interleaving test.** `4f0bbc5` adds `TestLosingWireGuardAccessConvergesUnderConcurrentSyncsFullSyncsAndStarts`.
  Eight groups hand their IP from user to user, half of them with the key; the user who loses access is sent with empty
  `peer_ips`, without WireGuard credentials, without proxies, or without the inbound; each change goes out as two
  `SyncUser` calls, one `UpdateUsers` batch, one chunked batch or `UpdateUsersAndRestart`, racing full syncs, restarts,
  stats ticks, readers and a `Start`. A fake device keeps each peer's allowed IPs the way the kernel does. After every
  phase the device and the peer store must hold exactly the desired peers with no IP held twice, removed peers' stats
  must be marked deleted, and every counted byte must be reported once.
  - On the fix's parent it fails in its first round: key handovers are refused, the old users' peers stay, the peer
    store gives each freed IP to two users, a restart hands the IP back to the stale peer, and the removed peers'
    stats stay live. The same on Linux (Go 1.26.6, kernel 5.15).
  - With the fix: 20 runs of the three tests under `-race` on darwin (60 of 60 pass), 10 runs under `-race` on linux
    (30 of 30), and the whole `backend/wireguard` package 3 times under `-race` on both, with no data race.
- **Mutants of the touched set**, each run against the three tests:
  - the touched set built from `normalizedUsers` again: all three fail;
  - only users that carry a WireGuard proxy count as touched: the interleaving test and the removal test ("no
    wireguard" shape) fail, the handover test passes;
  - only users that carry a public key count as touched: the same two fail;
  - users with an empty email count as touched too: all three pass. This mutant is equivalent: the peer store never
    holds a peer with an empty email, because every path that stores peers goes through `normalizeUsers`.
- **Not run here.** The real-kernel tests (`-tags=integration`, `make test-integration-wireguard`) need root and the
  `wireguard` kernel module. The Linux test host has no `wireguard` module loaded, and loading one on that production
  host is not done for a test; the `wireguard-integration` job of `run-tests.yml` runs them on GitHub's runner.
- **Drift check.** `DRIFT_COMPONENTS=node` reports `f72ac0e` as `UNPORTED` on `8959c50` (exit 1) and as `PORTED` on
  `4f0bbc5`, with 0 unported items.

### State on 2026-11-02

The scheduled drift run 41444668606 (job 125274699079) on `main` `b8679894` failed with 12 unported items. A full run of all five sections
on node `0dce28fd` the same day, with a fresh fetch, found 15: three new commits on upstream node `dev`, eleven Xray TUN
inbound commits and the Xray release `v26.10.31`. After the waiver changes below the same run exits 0 with
`upstream-drift: 0 unported items across [node xray sing-box sing-quic mtg]` and prints no stale-waiver note.

Heads checked on 2026-11-02 (the drift check's own fetch, `git ls-remote` and the GitHub releases API):

- PasarGuard/node: `main` `675bdaa24a3a67847f2094e518f1ae4d8460aa01` (`v0.5.6`, the newest of 44 tags and the newest
  GitHub release, published 2026-10-27; `v0.5.5` is `47857d43fe777f30f9ecc5f2ffe4ca0b037507cb`), `dev`
  `ebb0d02944bdf4f7166819d31466a224f0daf7e2`, `chunck` `c194114eb1971d2e252fa1d117d655122c356d35`, `debug`
  `8047fad98ec7e9955a405a97bf90a2411d8d6678`, `pr-61` `4eb26fbd00387c88a4bcaf5c1d9e9e8dc4604b62`. `api-key`, `fix/xray-api`,
  `refactor/inbound-map-storage` and `masque` no longer exist.
- XTLS/Xray-core: `main` `7c8dbcb4bbec7f80d16070069e0b2f7bf3aca62c` and 83 branches active since the pin; the newest
  release is `v26.10.31` `a4a3dff5b20c2faae567b742c172c7b95e75c083`, a final release published 2026-10-31. Upstream
  deleted the tags `v26.9.8` (`37ceb8b`), `v26.10.10` (`701af60`), `v26.10.17` (`92ccc93`) and `v26.10.29` (`0afec66`);
  their commits are in `v26.10.21` and `v26.10.31`, which stay waived.
- SagerNet/sing-box: `oldstable` `78d3eeedacdda487eca7f2793e29973cb2f8a86c` and 8 active branches; `stable`
  `c005f35300ce76427dc285481ec5695f5b179cd2` is `v1.14.4`, the newest final tag.
- SagerNet/sing-quic: `main` `7e0a5869a5f49ed0a685143345dfda0448bbc708`; the newest final tag is `v0.7.1`
  `8601a428f4db7fc011b3656080bf4c428ca62417`.
- 9seconds/mtg: `master` `2026e32e229f88ccb20831edc4874213e42d9e65`; no release after the pin.

Counts of the passing run: node 57 upstream commits outside the target (13 ported, 1 empty, 43 waived), 5 allowed fork
entries and 5 allowed rewritten merges, 1 waived upstream merge; xray 56 security-looking commits and 15 releases;
sing-box 27 and 11; sing-quic 5 and 4; mtg 1 and 0; one xray and one sing-box advisory.

New items and their decisions:

| item | decision | evidence |
|---|---|---|
| node `03f66e9b4817a961d3085134b3e1d532fd57862d` (#122, grpc 1.85.0) and `68e84193ace5216cbe8faf572fa3533402005cb2` (#121, gopsutil 4.26.10) | `pending` until 2026-11-30, held under the release-only rule | dependabot bumps of `go.mod` and `go.sum` only, on `dev` and in no tag. The reason says `dev-only`, so the check turns each one `UNPORTED` the day an upstream tag contains it, and it is then ported. govulncheck v1.8.0 (database of 2026-10-27) lists no grpc or gopsutil id for the node module |
| node `ebb0d02944bdf4f7166819d31466a224f0daf7e2` (WireGuard E2E AppArmor) | `superseded` | the fork fixed the same Ubuntu 26.04 `wg` profile denial in `b8679894` on 2026-10-29 by writing the client key under `/etc/wireguard`; with it the three E2E sync tests pass on ubuntu-latest (job 123832855477, no `Permission denied`). Upstream's other half unloads the `wg` and `wg-quick` AppArmor profiles from `TestMain` and from `make install_wg`; it is not taken, because it weakens the host's confinement and the fork's fix needs none of it |
| Xray `96387150473461be90372fb1a217bbcda3f47b5f`, `6b45eadf45790cd1a656a5156c496f877d4893af`, `c684201cd86db483eef5d9765c39446418d0ec9d`, `2218fec1735ce5bc9af389352e5ac901b0270c65`, `9ffa0f0552bcda4e6b3166282940db83cfde788b`, `2933b0c8d5dbc0b95752f79e73029149b57b6852`, `bb4cd4b6d38101b62ee0b92bfd399b8d99a377b2`, `468f745fc28f61a50ddc7a666b7a1f852a05a2d5` | `not-applicable`, `branch:windows-tun-wfp-all` | the eight commits of branch `windows-tun-wfp-all` (head `9638715`), matched on the word leak in `autoSystemWfpBlockLeak`. Each touches only `proxy/tun` (the Windows WFP filters in `tun_windows*.go`, the option's value parser `wfp_leak.go`, the README) and the TUN config parser `infra/conf/tun.go`, with their tests. The option came after the pin with `1f30491`; at `v26.3.27` `proxy/tun` has no WFP file and neither `infra/conf/tun.go` nor `proxy/tun/config.proto` knows the option. The node image runs Linux and `backend/xray` never emits a tun inbound |
| Xray `6cdd323ad82fe21bb9c1d06e6a5c5f909de2ffaf` (#7288) | `not-applicable` | the same TUN option, merged into `main` and released in `v26.10.31`, so it has no branch scope |
| Xray `967ade1e03f4c9458240a44e6247045da171869b` (#7315) | `not-applicable` | the same TUN option again, more values and always-allowed loopback, DHCP and neighbor discovery; on `main` at 2026-11-02T21:40Z, after main `f2712fdd`'s green drift run, and in no release yet. It touches only `proxy/tun` and one error string in `infra/conf/tun.go` |
| Xray `f2f292c90ce100093cfad5849bafadd49e873d26` and `266df8c6448ba01709be0dae7bb2ede6d258f756` | `superseded`, `branch:windows-tun-wfp-ports` | the two commits of branch `windows-tun-wfp-ports`, squash-merged as `6cdd323`. The tip `266df8c` equals `6cdd323` in every file the branch touches; the one `proxy/tun` difference, `config.pb.go`, comes from `main`'s regeneration `d1140de` and differs the same way between their parents `0afec66` and `316e07a` |
| Xray release `v26.10.31` (`a4a3dff5b20c2faae567b742c172c7b95e75c083`) | `pending` until 2026-11-20, in the same form as `v26.10.21` | 29 non-merge commits after `v26.10.21`; the only security-looking one is `6cdd323`. Its HTTP inbound change `bc7c895` (#7298) edits `proxy/http/server.go`, which `xray-http-100-continue.patch` also changes, and is to be checked against that patch at the pin move |

Waivers removed because the run matched nothing for them (the check's own instruction):

- node `2ae8ca9`, `d09e506` (`fix/xray-api`), `0c44dc6`, `72f7b4f` (`refactor/inbound-map-storage`) and `243b818`,
  `f0877ce`, `b744287` (`masque`): their branches were deleted and no upstream ref reaches them (checked with
  `git for-each-ref --contains` against every fetched head and tag). The masque work is tracked by `88d5e75`, above.
- node `3a3d5c5` and `f72ac0e`: the check marks both `PORTED` by the trailers of `53044cc` and of `bbc488e` and
  `cbb63a0`, so their `pending` waivers were no longer needed.
- Xray `37ceb8b`, `701af60` and `92ccc93`: release waivers of the tags `v26.9.8`, `v26.10.10` and `v26.10.17`, which
  upstream deleted. Their commits are in `v26.10.21` and `v26.10.31`. The fixes their reasons named keep their own
  waivers: `7da5dae`, `eddad37`, `00cde72` and `ac5d8a8`.
- Xray `b291961` (`privateip`) and `72b4d0c` (`limit-geodata-downloader`, "pb bypass"): both branches were deleted.
  The `infra/conf/xray.go` hunk of `b291961` is the same as that of `43c29f8` (#7249), and the `app/geodata` checks of
  `72b4d0c` are carried by `48f62b8` (#7267); both are on `main` and in `v26.10.31`, whose waiver names them. Neither
  is reachable at the pin: `requiresTransportSecurity` came with `d7fa207`, and `app/geodata` and
  `infra/conf/geodata.go` do not exist in `v26.3.27`. That closes the reachability question that NODE-MISC-UPX held
  them for.

Pending waivers that fall due soon (none is extended here; the one later date move, the #58 fold of 2026-11-03, is
named in its entry):

- 2026-11-06: the three `masque` waivers, removed above, so nothing falls due that day.
- 2026-11-08: eight node `v0.5.5` items, `47857d4` (the version bump), `24e2b12`, `bb40094`, `8ad52fe`,
  `d309483`, `1549c96`, `bd745b5` and `d359a8f`, owned by NODE-SYNC-055. `946ab6a` (#114) and `71466f2` (#113, ported
  by `c2ed9854`, pushed to main on 2026-11-03 in `26bba5a8`) are ported by commits whose `Upstream:` trailer names
  them, so their waivers are gone.
- 2026-11-10: node `a3d2d2f` (NODE-WG) and Xray `8d0a7cf` (the SS2022 races on branch `ss2022`, a decision owed by
  NODE-MISC-UPX).
- 2026-11-20: node `e03754d` (#64, NODE-WG), which also carries the xray WireGuard parts of `33ab5fb` (#58): the
  Wireguard peers case of the config snapshot, and the wg inbound, `wireguardUser` and peer assertions of
  `TestToBytesWritesSyncedClientsAndPeers` and `TestToBytesWritesEmptyListsWithoutUsers`. They rewrite the WireGuard
  peer serialisation that #64 adds and the fork does not have, so they go in with the #64 port. The rest of #58 is
  ported by `797adcf0` (the config snapshot serialisation), `9c99b8a1` (`TestToBytesConcurrentCalls` and
  `TestToBytesWhileCloning`), `83932cb0` (`counterDelta` and the xray log scanner buffer pool, RED `f7891b13`) and
  `0687f7e3` (the other seven tests of upstream `config_tobytes_test.go`).
  `TestSnapshotInboundCopiesEveryExportedField` is ported against the fork's `(*Inbound).snapshotLocked`, which
  copies the exported `Inbound` fields by hand as upstream's `snapshotInboundForSerialization` does, and fails when
  one copy is dropped. On 2026-11-03 the main session folded the separate `33ab5fb` waiver (NODE-LEAD, due
  2026-11-10) into the `e03754d` one, so these WireGuard parts are now due on 2026-11-20; the commit that removed
  that waiver carries `33ab5fb`'s `Upstream:` line, and the drift check counts #58 as ported.

### State on 2026-11-03: the sing-box pin moves to v1.13.21

The Dockerfile's `singbox-builder` stage moves from `SINGBOX_COMMIT` `4a87f503d6c1279a81c8c0ec7eb6a8ff1c7af44c`
(`SINGBOX_BRANCH` `v1.13.14-hotreload`, 2026-09-14) to `3f35dc15e0b408aa75343944a984a0c295b562d4`, the head of
Free-Guy-IR/sing-box `main` on 2026-11-03: 37 commits ahead of the old pin and 0 behind it. `SINGBOX_VERSION` moves
from `v1.13.14` to `v1.13.21`: the fork has no tags, and its commit `f533a6ba6` imports upstream `v1.13.21`
(`628cb31ffa79cffffd34c2f9cde6cae044e4fc12`), so the variable names that upstream tag as the rule above requires. The
fork's `go.mod` now requires sing-quic `v0.6.5` (`38ed9a3f2`), the version its `third_party` copy carries.

What the move brings to the image:

- sing-quic Hysteria2 fixes taken into the fork's `third_party` copy: client handshake hang and dead connection leak
  (`8522d2ab2`), server handshake not bounded (`8c28cd5c8`), dead connection leak (`bb472a10f`), Salamander key race
  (`fc9108ed5`), UDP sessions not closed on connection close (`cd90536b0`), and the vectorised Salamander write
  (`216f614ea`, upstream sing-quic `88be83fe62a55baa4277ea3f6862c5f92daff9dc`).
- sing-quic `e702a73535d54797eac67075f1464bb4c4e7a9f3` (fork `cabf51734`, with its bounds tests in `6edbfc24f`): before
  it, an authenticated Hysteria2 client could crash the server with a crafted request.
- Hysteria2 authentication published once, atomically, per session (`f493fd0dc`), and an authentication registered
  before a removal can miss it (`d21dff6cd`).
- The protocol input validation fixes of upstream sing-box `52f9bad034d291fc16869a396bf812508d50ecd6` (1.14 line,
  ported to 1.13 as `a930417d2`), sing-vmess `9b95ab8`, sing-shadowsocks, sing-shadowsocks2 and sing-shadowtls, and the
  fork's dependency raise of x/net, x/crypto, x/text and grpc past their reachable advisories (`18a8e225b`).
  `52f9bad0` is a partial port, although `d189e942` names it in an `Upstream:` line: `a930417d2` carries only its
  sing-box source hunks, and the library halves of the same fix, SagerNet/sing `8d939df03478` (`common/domain/matcher.go`),
  sing-mux `caf09fe32475` (`brutal.go`, `h2mux.go`, `protocol.go`) and sing-tun `0bdadeb4c934` (`flow_*.go`,
  `stack_*.go`, `ping/ping.go`), are missing from the image: the fork's `go.mod` at `3f35dc15e0b4` takes sing
  `v0.8.14`, sing-mux `v0.3.5` and sing-tun `v0.8.15` from the module proxy with no `replace`, and each of the three
  commits is ahead of that tag (GitHub compare, 2026-11-03). The drift check does not watch these three repositories,
  so this entry is their record: NODE-LEAD owns them, to be taken with the next sing-box pin move and no later than
  2026-11-20 (finding F-VSB-1). NODE-VERIFY judged from their file lists and the production config (one Hysteria2
  inbound, one direct outbound) that none of the three is on a path the node reaches; no live config dump was checked.

Evidence for the move:

- The fork tree at `3f35dc15e0b4` builds with the image's tags for linux/amd64, linux/arm64 and darwin/arm64 with Go
  1.26.10, and `sing-box version` reports `v1.13.21`, revision `3f35dc15e0b4`.
- `make build_singbox` builds the pin the way the Dockerfile does, from the Dockerfile's own `ARG` values. The `test`
  job of `run-tests.yml` now builds it and runs the live Hysteria2 tests against it under the race detector:
  `TestLiveSmoke_SingBoxProcessStartsAndReportsStats`, `TestAnEmptyHysteria2PasswordIsNeverAccepted` and
  `TestSharedHysteria2PasswordBillsTheSameUserEveryTime`. The job fails when any of them does not pass, so a skip
  cannot pass for a run. Before this change no CI job had a sing-box binary, and every live test skipped.
- On Linux (the omnidev host, the same test binary) the three pass with the new pin. On macOS the two Hysteria2 tests
  pass with the whole `backend/singbox` package under `-race`.
- Three other live tests, `TestLiveSmoke_ConcurrentSyncUserCoalescesIntoOneRestart`,
  `TestQueueUser_TightSequentialCallsCoalesceIntoOneRestart` and `TestSyncUser_UnchangedUserSetSkipsRestart`, fail
  the same way with the old pin `4a87f503d6c1` and with the new one: they look for the user in the generated config
  file or for a new file mtime, while a hot-reloaded user change goes through the clash API and never rewrites the
  file. They are Linux-only and had never run in CI. They are tracked as finding F-R5-SB-1 and are not part of the CI
  step until they are rewritten for the hot-reload path.
- F-R5-SB-1 is known test debt that predates the move, owned by NODE-LEAD. Evidence: NODE-VERIFY ran the three on
  Linux (omnidev) with one test binary against both pins, `3f35dc15` and `4a87f503`, and all three fail on both. The
  normalised failure lines of the two runs are identical apart from the version and goroutine count lines of the
  passing smoke test; the first two fail at their `expected generated config to contain ... user` assertions, and the
  third (`TestSyncUser_UnchangedUserSetSkipsRestart`) measures restarts by the config file's mtime, which the
  hot-reload path does not change (verdict `node-sbpin-nodeverify.md`, check 4). CI does not run them:
  `backend/singbox/coalesce_test.go` is `//go:build linux`, the live step's `-run` pattern names only the three
  Hysteria2 tests, and the race step of the same job (`go test -race ./controller/... ./backend/...`) sets no
  `SINGBOX_SMOKE_BINARY` and has no `/usr/local/bin/sing-box`, so `discoverSingBoxBinary` skips them there; no other
  workflow runs `backend/singbox`. They stay unchanged until NODE-LEAD rewrites them to check the users the core
  reports through the clash API instead of the config file.

Drift check on the moved pin (all five sections, a fresh fetch on 2026-11-03): sing-box has 16 security-looking
commits and 5 releases after `v1.13.21`, sing-quic 0 and 2 after `v0.6.5`, all waived, 0 unported. The 24 waivers
that named sing-box commits up to `v1.13.21` and sing-quic commits up to `v0.6.5` matched nothing and are removed:
sing-box `6766b17`, `bd602b6`, `f260e77`, `d763884`, `890544e`, `ee808c4`, `7067276`, `0f17638`, `63d4cdf`,
`c639c27`, `d5c6c6a`, `3708fa1`, `17ec3c7`, `45ca32d`, `b5ebaa1`, `56f91df` (`v1.13.20`) and `628cb31`
(`v1.13.21`); sing-quic `b767090`, `35e2070`, `9bc0c88`, `8cdd427`, `2afc335`, `39c8586` and `d83826c` (`v0.6.5`).
The same run found the new mtg release `v2.3.0`, which is recorded in its own commit.

### govulncheck on the patched Xray build

govulncheck v1.8.0 was run with `GOOS=linux` on `./main/...` of the pinned v26.3.27, with every 0.6.21 patch applied
(the patches as of node commit `d18d291`), on 2026-09-24.

- Before the 0.6.21 bumps, six ids were symbol-reachable: GO-2026-6443, GO-2026-6348 and GO-2026-6061 in grpc
  1.79.3, GO-2026-5970 in x/text, and GO-2026-5026 and GO-2026-4918 in x/net. `xray-grpc-1.83.2.patch` clears all
  six.
- GO-2026-6354 and GO-2026-6355 (x/crypto/ssh, fixed in 0.56.0) were module-level only. `xray-x-net-0.59.0.patch`
  takes upstream `24e6f6d`'s x/crypto 0.57.0 and clears them.
- On 2026-10-10 (database of 2026-10-08), the same run with that patch and Go 1.26.6 found 12 symbol-reachable ids:
  GO-2026-6617, 6612, 6611, 6610 and 6603 in x/net 0.59.0 and the standard library, and 6613, 6609, 6608, 6607, 6605,
  6600 and 6599 in the standard library. `4f358c2` renames the patch to `xray-x-net-0.60.0.patch` and moves Xray to
  x/net 0.60.0, and the builder image moves to Go 1.26.9. With both, the run finds none at symbol or package level.
- Two ids are still listed at module level. Neither has a symbol-level or a package-level result:

| id | module | fixed version | state |
|---|---|---|---|
| GO-2026-5932 | `golang.org/x/crypto`, package `openpgp` | none: the package is unmaintained | not reachable per the govulncheck call graph on the patched build tree at node `d18d291` (2026-09-24): no package in the build imports `x/crypto/openpgp`. There is no fix to take |
| GO-2026-5841 | `github.com/klauspost/compress` v1.17.4 (indirect), package `s2` | v1.18.7 | not reachable per the govulncheck call graph on the patched build tree at node `d18d291` (2026-09-24): no package in the build imports `s2`. Upstream Xray `main` (`60e2a0c`) still requires v1.17.4, so there is no upstream bump to take |

- These are recorded here because `.github/upstream-waivers.txt` accepts only a 40-hex commit sha or a GHSA id, and the
  drift check matches only Xray's own advisories, not dependency advisories. A GO- id in the waiver file would be
  refused or reported as matching nothing.
- The final gate re-runs govulncheck and prints this list.

### Not covered

- **SagerNet/sing, sing-vmess, grpc-go and other Go dependencies.** The sing-box fork vendors or requires them. The
  govulncheck CI job scans only the node module, so it does not cover them. On 2026-10-10, govulncheck v1.8.0 with
  Go 1.26.9 on `./cmd/sing-box` at `SINGBOX_COMMIT` `4a87f50`, with the image's build tags, found no standard library
  id. It found 20 symbol-reachable ids in x/net 0.50.0, x/crypto 0.48.0 (ssh), x/text 0.34.0 and grpc 1.79.1. The
  node binds the sing-box gRPC and clash APIs to 127.0.0.1 and serves Hysteria2 over QUIC, so their HTTP/2 server paths
  are reachable only from the host itself. Moving the sing-box fork's dependencies is a separate, open decision.
- **Open pull requests on contributors' forks** (listed above).
- **Pre-release tags** (alpha, beta, rc). Their commits are covered by rule 2 on the active branches.
- **Alpine packages.** Since 0.6.21 the alpine and golang base images are pinned by digest, and the geo data comes from
  the v26.3.27 release zip with sha256 checks in the Dockerfile (U6 u6-6). The apk packages installed on top of the
  base still float.

## Fork deviations in the Xray build

The xray-builder stage of the Dockerfile applies every `patches/xray-*.patch` and greps the result after each one, so
a patch that is missing, reordered or edited down fails the image build. `make test_xray_guards` proves that the
guards cover every fix: it runs the stage's apply-and-guard chain on the pinned commit, then drops the diff of one
non-test file from one patch at a time and requires the chain or the linux/amd64 build to fail. On 2026-09-30 all 55
removals fail; before the guards for `sessionrevoke/config.proto`, `sessionrevoke/operation.go`, `singbridge/pipe.go`,
`shadowsocks_2022/outbound.go` and `wireguard/bind.go` were added, those five applied, passed every guard and built
(r2-B review). On 2026-10-11, at node `125a51e`, all 56 removals fail; the 56th is the `packet.go` diff of
`xray-ss2022-singbridge-close-race.patch`.

Every `go test` line of `make test_xray_patches` states its own race mode: `-race` with `CGO_ENABLED=1`, or
`-race=false`. CI runs the target as `CGO_ENABLED=1 GOFLAGS=-race make test_xray_patches`. A `-race` or `-race=false`
on the command line wins over `GOFLAGS`, so CI and a plain local run use the same mode on every line, and `GOFLAGS`
cannot switch a line's mode without anyone noticing. Only the two router stall tests,
`TestAddingALargeDomainRuleDoesNotStallRoutePicking` and `TestRemovingAWebhookRuleDoesNotStallRoutePicking`, run with
`-race=false`. They bound route-pick latency (200 ms and 500 ms), and those bounds were set without the race
detector. The router's race freedom is covered by the `-race` line for `TestRuleAndBalancerUpdatesRaceWithRoutePicking`
and `TestConcurrentRuleWritersLoseNoUpdateWhileRoutesArePicked`.

### FORK DEVIATION: trusted forwarder CIDRs and the trusted header's own value (patches/xray-xff-trusted-header.patch)

- **Builds on:** upstream 711aea4e34b0a00073d2fea6577f28a5d7c9b5eb (#6309), carried as patches/xray-trusted-xff.patch. The new patch applies after it in lexical order (Makefile glob and Dockerfile).
- **Upstream behaviour (711aea4):** when a header named in `streamSettings.sockopt.trustedXForwardedFor` is present, WebSocket, HTTPUpgrade, XHTTP (splithttp) and gRPC (Tun/TunMulti) take the client address from the FIRST `X-Forwarded-For` entry. The trusted header only gates that choice. Cloudflare appends to a client-sent `X-Forwarded-For`, so behind CF the first entry stays client-controlled. Anyone can also send the trusted header straight to the port.
- **Pinned v26.3.27 without any XFF patch:** `sockopt.trustedXForwardedFor` is already parsed (infra/conf/transport_internet.go:1079, config.proto field 23). With the list empty, the websocket, httpupgrade and splithttp hubs honour `X-Forwarded-For` unconditionally (websocket/hub.go:68-78). gRPC honours `x-real-ip` metadata with no setting at all.
- **Fork behaviour (this patch):**
  - A header is trusted only when BOTH conditions hold: (a) `sockopt.trustedXForwardedFor` names a header that is present, and (b) the environment variable `XRAY_TRUSTED_FORWARDER_CIDRS` is set and the TCP peer IP is inside one of its CIDRs. IPv4-mapped IPv6 peers are unmapped first.
  - With the variable unset or empty, no header is trusted, even when `trustedXForwardedFor` is configured, and the TCP peer is always used. This fails closed for relay and direct inbounds.
  - Once trusted, the address comes from the trusted header's own value, for example `CF-Connecting-IP`, which Cloudflare always overwrites. The first `X-Forwarded-For` entry is never used.
  - A value that is not exactly one valid IP (non-IP text, a comma list, or repeated header lines) falls back to the TCP peer, never to `X-Forwarded-For`. When `X-Forwarded-For` itself is the trusted key, a single-value header is accepted and a list falls back to the peer.
  - With no trusted list, the TCP peer is used. gRPC no longer reads `x-real-ip`; that removal is already part of 711aea4.
- **Environment parsing:**
  - The variable is read once per process (sync.OnceValue) from `XRAY_TRUSTED_FORWARDER_CIDRS`, the xray platform env flag `xray.trusted.forwarder.cidrs`.
  - Entries are comma-separated. Whitespace around each entry is trimmed, and empty entries between commas are ignored. A value made only of separators counts as empty, so nothing is trusted.
  - Any entry that is not a valid CIDR makes the whole setting fail closed: no header is trusted, and exactly one error is logged ("XRAY_TRUSTED_FORWARDER_CIDRS is not a valid CIDR list, so no trusted header will be honoured"). The core does not panic.
  - A bare IP without a prefix length counts as invalid.
  - An entry written as an IPv4-mapped IPv6 prefix, for example `::ffff:127.0.0.1/128`, counts as invalid too, with
    its own error log naming the entry. Peers are unmapped before the check, so such an entry could never match; before
    this rule it parsed without error and silently trusted nothing (F-r2B-4). Write the IPv4 form, `127.0.0.1/32`.
  - A peer on a unix socket listener is reported as `0.0.0.0` (Xray's `UnixConnWrapper`), so it is trusted only by an
    entry that contains `0.0.0.0`, such as `0.0.0.0/32` or `0.0.0.0/0`.
- **Why the environment and not config:** the node starts xray with `cmd.Env = append(os.Environ(), ...)` (backend/xray/core.go:269), so the container environment reaches the core with no Xray proto or panel change. The variable is per node and applies to every inbound of that node's xray.
- **Residuals:**
  1. On CF-fronted inbounds that sit behind a local socat TLS terminator (the Cloudflare-fronting recipe), the TCP peer xray sees is 127.0.0.1. The CIDR check cannot tell Cloudflare from a direct client that connects to socat. Those ports must be firewalled to Cloudflare's published ranges at the host.
  2. Rollout: every inbound that relies on `trustedXForwardedFor` today reports the TCP peer after this patch until `XRAY_TRUSTED_FORWARDER_CIDRS` is set on that node's container. For socat-fronted ports the value is 127.0.0.1/32 (see residual 1). For inbounds reached directly by Cloudflare it is Cloudflare's ranges.
  3. `ApplyTrustedXForwardedFor` keeps its name and its upstream doc comment. The comment still says "overridden by X-Forwarded-For"; the code now takes the trusted header's value. Under the no-comment rule the comment was left untouched.
  4. `ParseXForwardedFor` stays in common/protocol/http/headers.go (from the 711aea4 carry). No transport calls it, and the build guard fails if one does.
- **Tests:**
  - `testing/trustedheader` holds 16 scenarios, among them two lists with an IPv4-mapped entry that must fail closed, run in websocket, httpupgrade, splithttp, grpc/encoding (Tun and TunMulti) and common/protocol/http. Each scenario re-executes the test binary with its own `XRAY_TRUSTED_FORWARDER_CIDRS`.
  - `TestInvalidCIDRListLogsOnce` covers 32 concurrent first uses; it passes under `-race`.
  - `TestRepeatedOrListTrustedHeaderFallsBackToThePeer` (from the r2-B review) sends the trusted header as repeated
    lines, as a comma list, empty, and repeated `X-Forwarded-For` lines as the trusted key; each must give the TCP peer.
    It fails when the single-value check `len(found) == 1` is loosened to `>= 1`.
  - The upstream `TestDialWithRemoteAddr` (ws, httpupgrade, splithttp), `TestApplyTrustedXForwardedFor` and `TestRemoteAddrFromContext` now expect the TCP peer when the variable is unset.
  - All of these run in `make test_xray_patches`.
- **Removal condition:** drop this patch only when upstream Xray takes the address from the trusted header's value and gates it on the peer address, or when the owner decides to accept upstream's first-entry behaviour.

### FORK DEVIATION: routing API data race fixed at the pin (patches/xray-router-race.patch)

- **Upstream:** `598bde74122f5f0207c7c6a56ccb74fbbbe33fca` (#6678) fixes the race with a refactor of `app/router` that
  does not apply to the pinned v26.3.27. The fork carries its own fix in the same shape. The patch deliberately has no
  `Upstream:` line, and the commit stays waived as `pending` for the parts of the refactor that are not carried.
- **The race at the pin:**
  - `pickRouteInternal` ranged over `r.rules` without a lock, while `ReloadRules` (`AddRule`) appended to it and
    `RemoveRule` replaced it under `r.mu`.
  - The balancer API (`GetPrincipleTarget`, `SetOverrideTarget`, `GetOverrideTarget`, `OverrideBalancer`) read the
    balancers map while `ReloadRules` wrote it. A concurrent map read and write is a fatal runtime error.
  - The node calls `AddRoutingRule` and `RemoveRoutingRule` through `RoutingService` (`backend/xray/routing.go:60-73`)
    while traffic is routed.
- **The first 0.6.21 fix stalled routing (F-r2B-2, fixed):** it made every route pick and balancer lookup take a read
  lock, while `ReloadRules` built conditions and balancers and `RemoveRule` and `ReloadRules` closed webhooks under the
  write lock. Every routing decision on the node then waited for the whole `AddRoutingRule`: with a 400000-domain rule
  the slowest concurrent pick took 315 ms on a darwin test machine (the review measured 4.7 s against 112 ms on the pin),
  and removing a webhook rule whose endpoint was slow held every pick for up to the webhook client's 5 s timeout
  (1.9 s in the test).
- **The fork fix**, in `app/router` only:
  - the rules and balancers are one immutable table published through an `atomic.Pointer`. `PickRoute` takes one
    snapshot and uses it for both passes; `ListRule`, `RuleExists` and the balancer lookups read the current table.
    None of them takes a lock;
  - `r.mu` stays a `sync.Mutex` and is taken only by `ReloadRules`, `RemoveRule` and `Close`, so concurrent writers
    cannot lose each other's rules or balancers. A writer builds a new table from the current one, including the domain
    matchers and balancers, and publishes it with one store. No published slice or map is ever written again;
  - the webhooks of replaced (`ReloadRules` without append) or removed rules are closed after the mutex is released;
  - a failed `ReloadRules` publishes nothing and closes the webhooks it created. At the pin a failed append kept the
    balancers it had added, and a failed replace left an empty rule set;
  - `Init` builds its table the same way and keeps the pin's semantics: no duplicate `ruleTag` check at startup.
- **Not carried from upstream:** the inline webhook sweep and the removal of `OverrideBalancer`. Upstream closes the
  old webhooks while it still holds the mutex; the fork closes them after releasing it.
- **Kept from the pin:** a notifier that a route pick loaded just before its rule was removed may still be fired after
  it is closed. That window exists at the pin and upstream as well.
- **Tests**, all in `make test_xray_patches`:
  - `TestRuleAndBalancerUpdatesRaceWithRoutePicking` runs `AddRule` (a rule plus a balancer), `SetOverrideTarget` and
    `RemoveRule` 300 times against four readers of `PickRoute`, `GetOverrideTarget` and `ListRule`, under `-race`.
  - `TestConcurrentRuleWritersLoseNoUpdateWhileRoutesArePicked` runs eight writers that add 60 rules and balancers each
    and remove every other rule, plus eight concurrent adds of one contested `ruleTag`, against four readers, under
    `-race`. After each `AddRule` or `RemoveRule` returns, the writer's own pick must see the change. At the end exactly
    one contested add has succeeded and no rule or balancer is lost or duplicated. With the writer mutex removed it
    fails with lost balancers and eight accepted contested adds.
  - `TestAddingALargeDomainRuleDoesNotStallRoutePicking` (from the r2-B review) adds a 400000-domain rule while picks
    run: the slowest pick took 315 ms with the read-lock fix and at most 9 ms over six runs with this one (limit
    200 ms).
  - `TestRemovingAWebhookRuleDoesNotStallRoutePicking` (from the r2-B review) removes a rule whose webhook POST is
    held by the endpoint: an unrelated pick took 1.9 s with the read-lock fix and passes with this one (limit 500 ms).
    It fails again when the webhooks are closed before the mutex is released and picks take the mutex.
  - The full `app/router` suite passes with and without `-race`.
- **Mutants:** the pin's `slices.Clip` line, an equivalent mutant in the review, no longer exists. Every writer now
  copies the rules into a new slice, so a published slice is never appended to.
- **Removal condition:** drop the patch when the pin moves to a release that contains `598bde7` (v26.9.x).

### FORK DEVIATION: a revoked user's UDP flows are cut too (patches/xray-session-revoke.patch)

- **Upstream:** Xray has no session revoke. `RemoveUserOperation` removes a user from the inbound, and every
  connection or flow the user already has keeps running. The fork's `RevokeUserOperation` (package
  `app/proxyman/sessionrevoke`) closes them, and the node sends it when a user is removed or its credential changes.
- **The gap in the first 0.6.21 patch (F-r2B-1, fixed):** only the TCP and unix socket workers passed their
  dispatcher through the revocable wrapper. A UDP flow never registered with the revoke registry, so a revoke closed
  nothing on UDP. The Shadowsocks (AEAD, not 2022) server keeps the user it decoded from a source's first packet and
  checks the later packets of that source against that user only, and the UDP worker keeps a flow until it has been
  idle for 2 minutes. A client that kept sending therefore kept its UDP flow after its user was removed or rotated:
  40 and 39 echoes in 4 s after `SyncUser` in the r2-B node test.
- **The fork fix:**
  - the UDP worker wraps its dispatcher per flow like the TCP worker, so the flow's first dispatch binds it to its user
    and a revoke cancels the flow's context and closes it. A new flow that still carries a revoked user object is
    refused at dispatch;
  - the Shadowsocks UDP loop stops reading and dispatching once the flow's context is cancelled, so the packets
    already queued in the flow are dropped and not forwarded;
  - when a flow ends, the worker removes it from its table only if the table still points to that flow, under the
    worker lock. Before, the cleanup of a revoked flow could remove the newer flow the same source had already opened;
  - a released session is never registered again (U-N15). Shadowsocks 2022 dispatches a UDP flow from the goroutine
    that sing's NAT table starts for it, so that dispatch can land after the flow's `Process` returned and the worker
    called `Release`. The first version of this fix then registered the session with nothing left to release it: 47
    of 50 such flows stayed in the registry for the life of the process, and every revoke of the user walked them.
    `Release` now marks the session and a later dispatch on it is not registered. The revoke checks still run first,
    so a released session whose user was revoked is still refused and closed.
- **Stock core:** unchanged. On the stock Xray core a removed or rotated Shadowsocks AEAD user keeps an open UDP flow
  until it has been idle for 2 minutes; the r2-B test reproduces this on the stock v26.3.27 core as well.
- **Residuals:**
  1. A UDP flow is bound to the first user that dispatches on it. Shadowsocks AEAD decodes a source with one user
     only, so this is exact there. For Shadowsocks 2022 several users could share one source address only behind a
     relay that multiplexes them onto one UDP socket; revoking the first of them then cuts the shared flow, and it
     forms again on the next packet with the users that remain.
  2. Shadowsocks 2022 UDP flows were already cut before this fix by its own user update; hysteria runs over QUIC and is
     not handled by the UDP worker.
  3. The two Shadowsocks 2022 late-dispatch tests ran without `-race` until F-SINGBRIDGE-RACE was fixed: the same late
     dispatch tripped a data race in `common/singbridge/packet.go`, where `PacketConnWrapper.Close` released `cached`
     while `ReadPacket` wrote it. That code comes from upstream through `xray-ss2022-dispatch.patch`. It is fixed by
     `xray-ss2022-singbridge-close-race.patch` (see its own section below), and both tests now run under `-race`.
- **Tests:**
  - `TestRevokeCutsAUDPFlowAndKeepsTheSourcesNextFlow` (`app/proxyman/inbound`): a revoke closes the flow, the next
    flow with the revoked user is refused and removed, a later flow of the same source with the new credential is kept
    when the old flow's cleanup runs, and no session stays registered.
  - `TestRevokeRacingUDPPacketsNewFlowsAndReAddsCutsOnlyRevokedGenerations`, under `-race`: 16 sources send packets
    without pause while one user's credential is revoked 31 times, with a new credential added between most revokes;
    every flow of a revoked generation ends, the other
    user's flows stay open, and each source ends with exactly one live flow, which is the one the worker routes to.
  - `TestAUDPFlowStopsDispatchingOnceItsContextIsCancelled` (`proxy/shadowsocks`): packets read after the cancel are
    not dispatched.
  - Each of the four fix lines has its own failing mutant: the unwrapped UDP dispatcher, the cleanup without the
    identity check, the missing `Release`, and either Shadowsocks context check.
  - The node test `TestRotatingOrRemovingAShadowsocksUserCutsItsUDPFlow` (`backend/xray`, from r2-B) runs the real
    core: it fails on the core without this fix and passes with it.
  - `TestADispatchThatOutlivesItsUDPFlowLeavesNoSessionRegistered` (`app/proxyman/inbound`), under `-race`: a proxy
    dispatches only after its `Process` returned and the worker released the flow, and no session may stay registered.
    It fails 10 of 10 on the first version of the fix.
  - `TestAShadowsocks2022UDPFlowEndedByAnInvalidPacketLeavesNoSessionRegistered` and
    `TestAShadowsocks2022UDPFlowEndedByADuplicatedDatagramLeavesNoSessionRegistered`: 50 real Shadowsocks 2022 flows
    each, ended by a bad or a replayed datagram. 48 to 50 of 50 stayed registered on the first version, none now.
  - `TestLateDispatchesRacingRevokeLeaveNoReleasedSessionRegistered` (`app/proxyman/sessionrevoke`), under `-race`:
    4000 dispatches that arrive after their release race a revoke loop; all 4000 stayed registered on the first
    version.
  - `TestALateDispatchOfARevokedUserIsRefusedAfterItsFlowWasReleased` fails when the released check is moved above
    the revoke checks, which would let a revoked user's late dispatch through.
- **Removal condition:** none while the node relies on `RevokeUserOperation`; it goes with the session revoke patch.

### FORK DEVIATION: singbridge Close no longer releases the datagrams ReadPacket is using (patches/xray-ss2022-singbridge-close-race.patch)

- **Upstream:** no upstream branch or tag fixes this race. On 2026-10-10 all 116 branches and 135 tags of
  XTLS/Xray-core were checked (`main` `701af60772cda123492f86e383e1fdba066614a9`, which is `v26.10.10`). Wherever
  `common/singbridge/packet.go` exists, it is one of three blobs: `fef955e` (before the pin), `d4dccb4` (the pin) or
  `fde4bed` (`d878fc8`, which the fork carries as `xray-ss2022-dispatch.patch`). Upstream removed `common/singbridge`
  with the SS2022 refactor `65e853ed845e822b818386f0aeac93f5622dc41a` (#6831, first released in `v26.9.30`), which
  does not apply to the pin. So the patch has no `Upstream:` line.
- **The race at the pin plus `xray-ss2022-dispatch.patch`:**
  - sing's `bufio.CopyPacketConn` runs the two copy directions as one `task.Group` with `FastFail`. When one direction
    ends, it calls `Close` on both conns while the other direction can still be inside `ReadPacket`, and only then waits
    for it.
  - `PacketConnWrapper.Close` released `w.cached` while `ReadPacket` split it and wrote it back, with no
    synchronisation (`packet.go:105` against `:54` and `:74`). Besides the data race, `ReadPacket` could copy a
    datagram out of a buffer that `Close` had already returned to the pool. The datagram then reached the client empty,
    with the flow's default address instead of its source, or with the bytes of whatever reused that buffer. A buffer
    that both released at once could enter the pool twice, and two later users would then share one backing array.
  - The Shadowsocks 2022 inbounds (`inbound.go`, `inbound_multi.go` and `inbound_relay.go`) and the Shadowsocks 2022
    outbound reach it on every UDP flow that ends while a reply is being read.
- **The fork fix**, in `common/singbridge/packet.go` only:
  - `cached` and a new `closed` flag are guarded by a mutex. The mutex is never held across the blocking link read;
  - `Close` marks the wrapper closed, releases what is cached and drops the slice. A second `Close` is a no-op;
  - a `ReadPacket` after `Close` returns `io.ErrClosedPipe` without reading the link. sing's `IsClosedOrCanceled`
    counts that error as a normal close;
  - a read that comes back from the link after `Close` releases what it got and returns `io.ErrClosedPipe` instead of
    caching it;
  - concurrent reads merge what is left of their batch into the cache instead of replacing it, so no datagram is
    dropped unreleased. sing reads each wrapper from one goroutine, so this changes nothing on that path.
- **Kept from upstream:** `ReadPacket` still ignores the error of the link read, as `d878fc8` does. On a link error it
  returns an empty datagram with no address and a nil error. That behaviour is out of scope here, and the fix does not
  change it.
- **Tests**, `common/singbridge/packet_close_race_test.go`, run under `-race` by `make test_xray_patches`:
  - `TestReadPacketRacingCloseOnTheSameWrapper`: in each of 300 rounds, one goroutine reads a three-datagram batch
    while another closes the wrapper. Each delivered datagram must carry its own bytes and source port, and every
    datagram the wrapper took from the link must be released at the end;
  - `TestReadPacketAfterCloseNeitherReadsTheLinkNorDelivers`: after `Close`, and after a second `Close`, `ReadPacket`
    returns `io.ErrClosedPipe`, delivers nothing and does not read the link again;
  - `TestAReadHeldInTheLinkDuringCloseReleasesWhatItGetsAndDeliversNothing`: a read blocked in the link while `Close`
    runs gets a batch afterwards, delivers nothing and releases the whole batch;
  - `TestConcurrentReadsWritesAndClosesNeverUseAReleasedBuffer`, the interleaving test: in each of 40 rounds four
    readers, three writers and two closers share one wrapper. Each datagram is delivered at most once and intact, every
    datagram taken from the link is released, every reader ends with `io.ErrClosedPipe`, and every upload reaches the
    link;
  - `TestAShadowsocks2022UDPFlowEndedByAnInvalidPacketLeavesNoSessionRegistered` and
    `TestAShadowsocks2022UDPFlowEndedByADuplicatedDatagramLeavesNoSessionRegistered` run the real Shadowsocks 2022 path
    and now run under `-race` as well.
- **Measured on 2026-10-10 and 2026-10-11:**
  - Before the fix, at node `dd782ad` (the pin, the earlier patches and these tests), each of the four new tests failed
    in 10 of 10 separate `-race` processes. The racing and interleaving tests reported the race at `packet.go:105`
    against `:57` and `:74`. Re-run with the 1-minute load below 20, the racing test failed 10 of 10 at `-count=10`.
  - The two Shadowsocks 2022 tests under `-race` before the fix: the invalid-packet test failed in 8 of 10 processes at
    a load near 100 and in 9 of 10 at a load below 20; the duplicated-datagram test failed 10 of 10 at both. Every
    failure reported the race at `packet.go:105` against `:54` or `:74`.
  - With the fix, at node `125a51e`: the four tests at `-race -count=20` passed 80 of 80 with no race report, and
    the interleaving test at `-race -count=10` passed 10 of 10. Both Shadowsocks 2022 tests at `-race -count=10`
    passed 10 of 10 with no race report. Each of these held at a load near 130 and again at a load below 20.
  - Mutants, each run with the four tests at `-race -count=3`. Reverting the whole fix fails 12 of 12. A `Close`
    without the mutex and a cached branch read without it both bring the race back. Dropping the closed check before
    the link read fails 9 of 12, including `TestReadPacketAfterCloseNeitherReadsTheLinkNorDelivers`. Dropping the
    closed check after the link read fails 8 of 12, including
    `TestAReadHeldInTheLinkDuringCloseReleasesWhatItGetsAndDeliversNothing`. Replacing the cache instead of merging
    into it fails the interleaving test 10 of 10 at `-count=10`.
  - One mutant survives and is equivalent: dropping `w.cached = nil` after the release in `Close`. `ReleaseMulti` has
    already set every element to nil, and the closed flag stops every later read before it reaches the cache, so the
    line changes no behaviour. It is kept so that a closed wrapper does not hold the slice.
- **Patch order:** the patch extends the `packet.go` that `xray-ss2022-dispatch.patch` writes. Its name sorts right
  after that patch, so `make build_xray`'s glob applies it in the same order as the Dockerfile. Named
  `xray-singbridge-close-race.patch`, it would sort before `xray-ss2022-dispatch.patch` and fail to apply.
- **Removal condition:** drop the patch when the pin moves past `65e853e`, which removes `common/singbridge`.

### Adapted backport, not a deviation: stats GetOrRegister races (patches/xray-stats-register-race.patch)

- It carries upstream `0bafca94868a4ec38cd3dba0f2ba7ec6f44bbf18` with its `Upstream:` line. The Manager methods, the
  interface and the call sites are upstream code.
- **Differences:**
  - hunks for code that does not exist at the pin are left out: the `trackOnlineIP` helper, TUN inbound stats, and the
    restructured WireGuard client and server;
  - the pin names the online map field `onlineMap`, not `onlineMaps`;
  - the interface keeps the pin's doc comment for `UnregisterOnlineMap`.
- **Test:** `TestPerUserStatsCountEveryByteOnceUnderConcurrentFirstTrafficAndReset` starts 16 connections per user at
  once through `dispatcher.WrapLink`, with a `QueryStats` reset poller running throughout. It checks that every byte is
  counted exactly once and that every IP reaches the online map, over 200 rounds under `-race`.
  - On the pin, round 0 counted 56000 of 64000 bytes.
  - With the fix, 5 runs of 200 rounds pass.

### Adapted backport, not a deviation: XHTTP client races (patches/xray-xhttp-client-race.patch)

- It carries upstream `77f98eba0978cbcb425e2f8ec3cec86bd5aa8444` (#6665) and `eef6e63bc16d6e97fd98178556d49aad7978809a`
  (#6694) with their `Upstream:` lines. Every changed line of `transport/internet/splithttp/client.go` and `dialer.go`
  is upstream code.
- **The races at the pin:**
  - `WaitReadCloser.Set` wrote the embedded reader while `Read` checked it without synchronization (`client.go:179`
    against `:189`). `Close` and `Set` both closed the wait channel and relied on `recover`.
  - `DefaultDialerClient.closed` was a plain bool. A failed stream-down or packet POST wrote it, and XMUX read it
    through `IsClosed()` on other dials.
  - `uploadWriter.Write` read `buff.Len()` after `WriteMultiBuffer` had handed the buffer to the uploader, which clears
    it once it is posted (`dialer.go:614`). Besides the race, `Write` could report fewer bytes than it wrote.
- **Difference:** only the hunk context. The pin does not carry `986c512`, `9d9eaf3` or `1e036ce`; the first two keep
  their own pending waivers.
- **Reach:** client side only. No production core dials out over XHTTP (checked 2026-09-25), so the port mainly makes
  the -race CI run honest and protects a future XHTTP outbound. The server-side XHTTP code is untouched.
- **Why now:** CI runs `make test_xray_patches` with `GOFLAGS=-race`. Upstream's own `TestDialWithRemoteAddr` failed on
  these races (run 37916395457), and the splithttp `TestTrustedHeaderScenarios` line hits the same two families.
- **Tests:** `client_race_test.go` is fork code, committed before the fix (`08475ef`).
  - One -race test for each race: `TestAStreamDownBodySetWhileAReadWaitsIsDeliveredWhole`,
    `TestAFailedStreamDownMarksTheClientClosedWhileOthersPollIt`,
    `TestUploadWriterCountsEachWriteWhileTheUploaderReleasesItsBuffers` and
    `TestUploadWriterDeliversLargeWritesExactlyOnceToAConcurrentUploader`.
  - `TestAStreamClosedBeforeItsResponseClosesTheLateBodyOnce` holds the close-before-response behaviour: the late
    body is closed exactly once and a read returns no data.
  - `TestConcurrentClosesRacingTheResponseCloseItsBodyOnceAndWakeTheWaitingRead` (`10c22bd`, added after a review)
    covers the path eef6e63 made safe: in each of 50 rounds a Read waits while two goroutines close the stream as the
    response arrives. The Read must return only bytes of this stream's body, and the body must be closed exactly
    once.
  - The interleaving test `TestConcurrentStreamsOpenReadAndCloseWithoutLosingOrDuplicatingData` runs 96 stream-down,
    early-close, failed, stream-up and packet-up requests at once on one client while another goroutine polls
    `IsClosed`. It checks that each stream reads exactly its own body and that every response body is closed exactly
    once. It waits for each scripted body before counting closes (`b93ea8e`): an early-close stream returns before
    its body exists, and without the wait one `-test.count=10` iteration in ten failed with no data race.
  - On the pin with every fork patch, under -race, in separate processes: `TestDialWithRemoteAddr` failed 20 of 20 runs
    and the splithttp `TestTrustedHeaderScenarios` 5 of 5, all with data races. Five of the six new tests failed 10 of
    10 with data races; the close-before-response test passed, as designed. The concurrent-Close test, added later,
    failed 10 of 10 there with data races.
  - With the fix, on 2026-10-11 at a 1-minute load of 3.7 to 9.2 (`go1.26.6`, -race, separate processes):
    `TestDialWithRemoteAddr` passed 20 of 20 and the splithttp `TestTrustedHeaderScenarios` 10 of 10. The four
    one-race tests and the close-before-response test passed 20 of 20 each. The interleaving test passed 20 runs and
    10 runs of `-test.count=10`, and the concurrent-Close test 20 runs and 5 runs of `-test.count=10`. All of them
    together passed 3 runs of `-test.count=10`. No run reported a data race.
  - Mutants of the fixed code, one change each:
    - the old `WaitReadCloser`: `TestDialWithRemoteAddr`, the Set test, the interleaving test and the concurrent-Close
      test each failed 10 of 10, and `TestTrustedHeaderScenarios` 3 of 3, all with data races;
    - the plain `closed` bool: the closed-flag test and the interleaving test each failed 10 of 10 with data races;
    - the length read after `WriteMultiBuffer`: both upload tests failed 10 of 10, `TestTrustedHeaderScenarios` 3 of
      3, and `TestDialWithRemoteAddr` 9 of 10, with data races;
    - without `Set`'s `wait.Done` check, or without `Close`'s first `wait.Close`: the close-before-response test and
      the concurrent-Close test each failed 5 of 5, because the late body was never closed.
  - Each of those five mutants also fails a Dockerfile guard.
  - `CGO_ENABLED=1 GOFLAGS=-race make test_xray_patches` passes all 18 recipe lines (28 package runs, no data race)
    on this branch merged with the singbridge fix. Without that fix, the run stops at the earlier SS2022 line on the
    singbridge `PacketConnWrapper` race. `make test_xray_guards` removes 57 non-test file diffs on this branch, and 58
    with the singbridge patch; none is left uncaught.
- **Not changed, upstream as well:** the `GotConn` trace callback in `OpenStream` writes the returned addresses. If an
  HTTP/2 retry calls it again after `OpenStream` has returned, that write races the caller. This is the same in the pin
  and in upstream `main`, and it is to be reported upstream, not forked.
- **Removal condition:** drop the patch when the pin moves past `eef6e63`.

### FORK DEVIATION: WireGuard outbound dials through the tunnel taken under the lock (patches/xray-wireguard-client-close.patch)

- **The bug at the pin:** `Process` in `proxy/wireguard/client.go` called `processWireGuard` under `h.wgLock`, then
  read `h.net` again after unlocking it (`:210` and `:225`).
  - `Close` runs a goroutine that closes `h.net` and sets it to nil under the lock, so an in-flight `Process` raced it:
    a data race, and a nil `h.net` panicked.
  - After a `Close`, the next `Process` rebuilt the device and called `netBind.Close` a second time, which panicked
    with "close of closed channel".
- **Upstream:** the refactor `8626311` (#6287) removed this structurally, and it does not apply to the pin. `412898f`
  fixes a race that the refactor itself added, so it is not applicable. There is no smaller upstream fix to carry, so
  the patch has no `Upstream:` line.
- **The fork fix:**
  - `processWireGuard` returns the tunnel it read under the lock, and `Process` dials through that value only;
  - a closed handler refuses new work;
  - `netBind.Close` tolerates a second close.
- **Reach:** the node never configures an xray WireGuard outbound by itself. The panel can push any xray config, so a
  WireGuard outbound (for example WARP) is possible, and the handler is closed when the core stops or restarts.
- **Test:**
  - `TestProcessAfterCloseDoesNotPanic`.
  - `TestProcessWhileClosingDoesNotRaceOrPanic`: four workers run the real `Process` while the handler is closed 50
    times, under `-race`. On the pin it reported "close of closed channel", and then a data race at `client.go:87`
    against `:225`. With the fix, 10 runs pass under `-race`.
  - Pins for each fix line, added after the r2-B review found them unpinned; each fails on its own mutant:
    `TestAClosedHandlerNeverBuildsANewTunnel` (without the `h.closed` check a Process after Close builds a tunnel that
    nothing closes), `TestADialAfterCloseUsesTheTunnelProcessTookUnderTheLock` (a DNS lookup held open while the
    handler closes; a TCP or UDP dial through `h.net` then panics on the nil tunnel), `TestClosingABindTwiceDoesNotPanic`
    (without the tolerant close, "close of closed channel"), and `TestProcessTCPWhileClosingDoesNotRaceOrPanic`, the
    TCP twin of the race test.
- **Removal condition:** drop the patch when the pin moves past `8626311`.

### FORK DEVIATION: Dockerfile.xray removed

- **Upstream file:** PasarGuard/node's `Dockerfile.xray` builds the node and ships the prebuilt upstream Xray release
  that `make install_xray` downloads through `install_core.sh`.
- **Why the fork removed it:** no workflow builds it, and every image built from it would carry an Xray core without
  any of the fork's Xray patches: the commander message size, session revoke, the QUIC sniffer, trusted X-Forwarded-For,
  the trusted header, the HTTP 1xx, DNS, SS2022, stats and router fixes, and the grpc, x/crypto and x/net bumps. The
  only supported image is `Dockerfile`, whose xray-builder stage builds the pinned commit with every patch.
- **Upstream changes to it:** on 2026-09-24 no upstream commit outside the fork head touches `Dockerfile.xray` (checked
  on every upstream branch and tag). A later upstream change to it is waived as `not-applicable`, citing this entry.
- **Kept:** the Makefile's `install_xray` target, which CI uses for the stock xray, and `Dockerfile.wireguard`, which
  has no Xray.
- **Residual:** `.specify/memory/constitution.md` still names `Dockerfile.xray` in its 1.1.1 sync report, as a record of
  that amendment.

## The drift check

`scripts/upstream_drift.sh` runs five sections: `node`, `xray`, `sing-box`, `sing-quic` and `mtg`. The node section
fetches every PasarGuard/node branch and tag, pruning deleted ones, into `refs/upstream-drift/node/heads/*` and
`refs/upstream-drift/node/tags/*`. It then lists every non-merge upstream commit that is reachable from any of them and
not from the checked commit. The label on each line names the branches that contain the commit, or its tags when no
branch does. Each commit is classified:

| status | meaning |
|---|---|
| `PORTED` | a fork commit has the same `git patch-id --stable` (a verbatim cherry-pick), or a commit message in the fork's history has an `Upstream: <sha>` trailer or a `(cherry picked from commit <sha>)` line naming it (7 to 40 hex digits, prefix match), or a `patches/*.patch` header has such a line |
| `EMPTY` | the upstream commit changes no file, so there is nothing to port |
| `WAIVED` | `.github/upstream-waivers.txt` lists it with a reason |
| `ALLOWED` | a fork commit with an upstream parent (see below) that the waiver file allowlists as `allowed-merge` |
| `UNPORTED` | none of the above; the check exits 1 |

Two more kinds of item close the ways upstream work can slip past the commit list:

- **Fork commits with an upstream parent** (`node entry`). A fork-authored commit whose parent is an upstream commit
  brings upstream history into the fork without porting it. A `git merge -s ours` of an upstream head looks exactly
  like this, and it would hide every upstream commit it reaches. Every such commit must be listed in the waiver file as
  `<fork sha> allowed-merge: <reason>`, after checking that it really carries the upstream changes; otherwise it is
  `UNPORTED`. On 2026-09-25 there are four: the fork-point merge `9267b04` of `v0.5.4`, and three merges from 2024
  that come before the fork point.
- **Merges that rewrite the automatic result** (`node rewritten merge`).
  - Scope: every merge in the target history that no upstream tag contains, and whose recorded result differs from
    what git merges on its own (`git show --remerge-diff`). This means a conflict resolution, or a merge that drops one
    side, as `git merge -s ours` does.
  - Each one must be allowlisted as `allowed-merge` after review.
  - It is checked even when an upstream ref also contains the merge. The entry check above cannot see such a merge,
    for example after a fork branch with an ours-merge in it has been contributed upstream.
  - On 2026-09-29 there are three: the fork point `9267b04`, and two conflict resolutions from before the fork point,
    `bd8ceea` and `61932a4`.
- **Upstream merges with changes of their own** (`node merge`). The commit list skips merge commits. So every upstream
  merge outside the target must be ported or waived, like any other commit, when its combined diff (`git show --cc`)
  or its remerge diff (`git show --remerge-diff`) is not empty. A merge that drops one side's change has an empty
  combined diff but a non-empty remerge diff.
  - Both diffs are read in full; an earlier `| grep -q .` let git die of SIGPIPE on a large diff, so the merge was
    skipped.
  - A clean merge is only counted.
  - The check needs git 2.36 or newer and refuses to run on an older one.

The third-party sections are described above, and they use the same `PORTED`, `WAIVED` and `UNPORTED` statuses, plus
`FIXED` for an advisory already fixed in the pin. Merge commits are skipped, because their content is the commits they
merge. Upstream commits the fork already has through a merge, such as everything up to `v0.5.4`, are reachable and
never listed. The check exits 0 when nothing is `UNPORTED`, 1 when something is, and 2 on any error: a failed fetch, a
failed API call, a missing pin, or a malformed waiver.

### Porting an upstream commit

Add one trailer line per upstream commit to the porting commit's message, with the full SHA:

```
Upstream: a69d7ed6811af281ffc01e11478638d922130f0d
```

`git cherry-pick -x` is always recognised through its `(cherry picked from commit ...)` line. The patch-id alone
matches only when the fork's diff equals upstream's including the unchanged context lines around each change: a
cherry-pick of `#88` onto a `go.mod` that does not yet have `#84` and `#85` applies cleanly but has a different
patch-id. A partial port, or several upstream commits folded into one, is recognised only by the trailer. So write the
trailer (or use `-x`) on every port, and record a partial port in the table above. For a third-party fix carried as a
patch file, the trailer can go in the porting commit or in the patch file's header. An advisory can be named directly
with `Upstream: GHSA-xxxx-xxxx-xxxx`.

The check trusts the trailer: it does not verify how much of the upstream commit was taken, and a port that is later
reverted still counts as ported. Reviewers check the ported part against this page.

### Waiving an upstream item

Add one line to `.github/upstream-waivers.txt`:

```
<full 40-hex upstream sha or GHSA id> <category>: <reason>
```

- The id is the full upstream commit SHA. For a release, it is the SHA of the tag's commit, which the check prints.
  For an advisory, it is the GHSA id.
- `category` is one of `rejected`, `superseded`, `pending`, `not-applicable` or `allowed-merge`. `allowed-merge` takes the
  sha of a fork commit instead of an upstream one (see the node entry check above).
- A `pending` waiver must carry exactly one `until:YYYY-MM-DD`: zero-padded, no space after the colon, a real calendar
  date, and at most 30 days after today. A missing, second, spaced, unpadded or impossible date makes the line
  malformed, and the check exits 2.
  - An issue reference `issue:<owner>/<repo>#<number>` is optional. It must be well formed, and it never replaces the
    date, so no waiver is permanent.
  - After its `until:` date, the waiver no longer waives anything. Its item turns `UNPORTED`, and the run lists it under
    `EXPIRED WAIVERS`.
  - Extending a date is a decision, not a formality. A waiver that hides a live bug in shipped code gets a fix or an
    owner, not a new date.
  - `DRIFT_TODAY=YYYY-MM-DD` sets today's date for tests.
- The reason is mandatory and must say why, for example
  `superseded: the fork fixed this in <fork sha>` or `not-applicable: upstream release tooling`. A `pending` reason
  names the release or project that will take it.
- A waiver for work that lives only on a branch must say so with a `branch:<ref>` token (for example `branch:pr-61`).
  The node commit and node merge checks, and the security-looking commit check of every third-party component, then
  grant `WAIVED` only while every upstream ref that contains the commit is one of the named branches. The day upstream merges that branch into another ref, deletes it and tags a release, the
  commit becomes reachable from a mainline ref or a tag, and the item turns `UNPORTED` with a message naming the refs
  that now reach it. Several `branch:` tokens are allowed. The scope is required, not optional: a waived commit
  that no tag and no release-line branch reaches (node: `main`, `dev`; third party: the line branch and any branch
  named `main`, `master`, `dev`, `stable`, `testing`, `unstable` or `oldstable`) is `UNPORTED` until its waiver names
  the branches that do, whatever the reason says. Bot branches (`renovate/*`, `dependabot/*`) never break a scope,
  because they only rebase onto other branches; a commit that only bot branches reach is still branch-only, and its
  waiver names them. A waiver whose reason says the
  upstream work is unmerged (`not merged`, `never merged`, `revisit if upstream merges`) but carries no `branch:`
  token is also a malformed line; the check exits 2. A malformed `branch:` token also exits 2. A token is read even
  when punctuation wraps it, as in `(branch:pr-61),`. And whatever its tokens, a waiver whose reason says the work is
  unmerged or still open (`unmerged`, `not merged`, `not yet merged`, `never merged`, `still open`, `open pull
  request`, `awaiting upstream merge` and similar) turns its item `UNPORTED` once a release line or a tag carries
  the commit, because its reason is then false. In the same way, a waiver whose reason says the work is unreleased
  (`dev-only`, `unreleased`, `not yet released`, `... release contains it`) turns its item `UNPORTED` once an
  upstream tag contains the commit: under the release-only rule a released commit is to be ported, so the waiver
  has to be rewritten.
- The file has no comments and no blank lines; every line is a waiver. The check refuses a malformed line or a
  duplicate id.
- When a waived item later turns out to be ported, the check prints a note that the waiver can be removed.
- Every run ends with a `PENDING WAIVERS (N)` block. It lists each waived item whose category is `pending`, across all
  five sections, with its component, id and reason. Pending items stay waived and do not change the exit code; the
  block is there so they are never forgotten.
- On a full run, a waiver that matches nothing checked also gets a note: its branch was deleted, or the pin moved past
  it. Remove it.

### Running it locally

```
bash scripts/upstream_drift.sh
```

- It needs `git`, `curl` and `jq`. The GitHub API allows 60 unauthenticated requests an hour. A full run makes at least
  10, one pair per component. With `GITHUB_TOKEN` set, it sends that token instead.
- `DRIFT_COMPONENTS="node xray"` runs only the named sections. The default is all five.
- `DRIFT_CACHE=<dir>` keeps the third-party repositories and the advisory answers in `<dir>`, instead of a temporary
  directory. `UPSTREAM_FETCH=0` then skips every fetch and API call, and it reuses the node refs and that cache.
- `DRIFT_TARGET=<ref>` checks another branch or commit instead of `HEAD`.
- `DRIFT_MAX_API_PAGES` (default 50) bounds the advisory paging.
  - The check follows the `rel="next"` Link header of each response, the cursor pagination both advisory endpoints
    use. An earlier `&page=N` was ignored by the API, and it looped forever at 100 or more advisories.
  - A next link outside the API URL, or a listing longer than the bound, stops the run with an error.
- `make test-drift` runs both synthetic suites: `scripts/upstream_drift_test.sh` and the independent verifier's
  `scripts/upstream_drift_bypass_test.sh`, which holds the Dockerfile-reading and branch-scope bypasses. Each file
  guards its own case count.
- `bash scripts/upstream_drift_test.sh` runs the check against synthetic repositories: ours-merges, evil and
  side-dropping merges, waiver dates, patch scoping, the Dockerfile apply shapes (shell comments, heredoc bodies, a
  no-op `SHELL`, a moved `WORKDIR`, a `cd`, reverse applies and refetches, an exec-form revert, a binary copied from
  another stage), branch-scoped waivers before and after a merge, a large patch, and advisory paging through a stubbed
  curl. It needs no network, and each case runs under `CASE_TIME_LIMIT` seconds (default 60). The harness counts the
  cases it declares against the cases it runs and fails loudly if it stops early, so a silent abort can never read as a
  pass.
- `UPSTREAM_URL`, `UPSTREAM_SLUG` and `UPSTREAM_WAIVERS` override the defaults.
- A shallow clone is refused, because commits behind the shallow boundary are invisible, which makes the result wrong
  in both directions. `DRIFT_ALLOW_SHALLOW=1` runs anyway with a warning, for a quick local look only. Some local
  checkouts are shallow: if `git rev-parse --is-shallow-repository` says `true`, run the check in a full clone.

### In CI: a red drift check blocks image builds and releases

`.github/workflows/upstream-drift.yml` runs the check daily, on demand, on every pull request to `main`, and on every
push to `main`, to a `release/**` branch or of a `v*` tag. It has read-only repository permissions. It passes only the
job's own read-only `GITHUB_TOKEN`, for the API rate limit, and it pushes nothing. A pull request from a fork gets the
same read-only token, so the check needs no secret.

`upstream-drift.yml` and `run-tests.yml` are also reusable workflows (`workflow_call`). `run-tests.yml` holds the test
suite, the controller and backend tests under the race detector, the Xray patch tests under the race detector, the
replay of the image build's Xray patch guards (`make test_xray_guards`), the real-kernel WireGuard tests, the MTProto
fronting leg, govulncheck and the workflow policy check. Every
workflow that publishes something calls both of them first, on the same commit, and its publishing jobs `needs:` both:

| workflow | trigger | gated jobs | what it publishes |
|---|---|---|---|
| `image-branch.yml` | push to a branch whose name has no `/` | `image` | `ghcr.io/free-guy-ir/node:<branch>`, moved only while the branch still points at the built commit |
| `image-manual.yml` | run by hand | `image` | `:edge` from `main`, the branch-named tag from another branch, or a custom tag; never `:latest` or a release-like tag |
| `publish-release.yml` (Publish Release) | run by hand from a release tag on `main` or a `release/**` branch | `assets`, `image`, `publish`, `latest` | the draft release's zips and checksums and `:<tag>`, then the release itself, then `:latest`, pointed at the image of GitHub's latest release |

So a red drift check blocks every image build and every release, by design: nothing ships while an upstream item is
neither ported nor waived, even when the commit being built did not cause the drift. A new upstream commit turns every
build red until someone ports or waives it. The failing job ends with an annotation that names the case:

- **Unported upstream items (exit 1).** Port or waive them in their own pull request. A re-run checks the same commit
  again and stays red, so build from a commit that has the port or the waiver: push the branch again, or tag that
  commit as the next release.
- **The check could not complete (exit 2, or any other failure).** A fetch, an API call, a pin, the waiver file or the
  script failed, so parity is unknown. Fix the error, or re-run the workflow if it was a transient network or API
  failure.

A red run on a pull request that did not touch upstream code means upstream moved, in the same way. A newer run of the
same workflow for the same event and ref cancels an older drift job that is still running, and the older build then
does not run. A release stays a draft, with no assets and no image tag, until Publish Release has passed every gate on
its tag; the Releasing section of `README.md` describes the steps.
