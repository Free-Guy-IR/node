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

State as of 2026-09-24, release 0.6.21.

## Fork point

- The fork merged upstream tag `v0.5.4` (`e9a9ba286413c404854b80c847b588b620426660`) in `9267b04`
  ("merge upstream PasarGuard/node v0.5.4 into fork", 2026-08-31). Everything reachable from `v0.5.4` is therefore in
  the fork's history.
- Upstream `main` has no commit after `v0.5.4`. All later upstream work is on `dev`.
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
| `52d5aab33ee5dd0426ccb5d202dc06c36a500162` | deps: grpc 1.83.2 to 1.84.0 (#91), plus gofmt and an upstream test fix for `keepAliveStale` | held out of 0.6.21 by the release lead (2026-09-24); the node stays on grpc 1.83.2. The Go vulnerability database still lists released v1.84.0 as affected by GO-2026-6443 (golang/vulndb #6511, #6580), although the fix is verified in v1.84.0 `http2_server.go`, so the bump would turn govulncheck red. Waived as `pending` until the database entry is corrected. The fork's method port of `keepAliveStale` (`40248f2`) needs neither the gofmt nor the upstream test fix |
| `be2c445f7f486043d0efc4dfbe408c4a382869f8` | xray: leave unchanged users alone during user sync (#93) | the fork's own contribution, merged upstream on 2026-09-24; ported in 0.6.21 by `40006fe`, which carries `Upstream: be2c445f7f486043d0efc4dfbe408c4a382869f8` |
| `4a9d0cf8bd38d82f391c9152f64dad6eab7ead93` | feat(wireguard): support configurable server interface MTU (#90) | new on upstream `dev` after `be2c445` on 2026-09-24; ported in 0.6.21 by `f9c4f05` (merged into release/v0.6.21 at `01eed71`), which carries `Upstream: 4a9d0cf8bd38d82f391c9152f64dad6eab7ead93` |

The fork commits that carry the 0.6.21 ports are `f53ffa2` (the three dependency bumps), `40248f2` (the watchdog) and
`af180e2` (the rest of `a69d7ed` apart from `StartOrAttach`).
Each carries an `Upstream: <full sha>` trailer, which is how the drift check recognises a partial or combined port.
The toolchain moved to Go 1.26.6 in `345febd` (fixes GO-2026-6090), with a govulncheck CI job from `47ef6ee`.

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

Other open upstream work that was reviewed:

- PR #86, "xray: synchronize process status checks": the fork has had an equivalent fix since node 0.6.6 (a
  lock-free `coreState` snapshot and a `waitDone` channel in `backend/xray/core.go`).

## Other upstream branches

The drift check reads every PasarGuard/node branch and tag, not only `main` and `dev`. On 2026-09-24 the branches were:

| branch | head | status in the fork |
|---|---|---|
| `main` | `e9a9ba286413c404854b80c847b588b620426660` | merged (`v0.5.4`) |
| `dev` | `4a9d0cf8bd38d82f391c9152f64dad6eab7ead93` | see the table above |
| `api-key` | `11baa23f1caf1173907ba7cb6dc963c5db660bad` | fully in the fork's history, nothing to list |
| `chunck` | `c194114eb1971d2e252fa1d117d655122c356d35` | `c194114` is a branch-only prototype; upstream squash-merged chunked user sync as `4f8568b` (#23), which the fork has. Superseded, waived |
| `debug` | `8047fad98ec7e9955a405a97bf90a2411d8d6678` | `af0846e` and `8047fad` add diagnostic logging and were never merged upstream. Not applicable, waived |
| `fix/xray-api` | `d09e5064d02555735ee815c8483dc88c42a8f669` | `2ae8ca9` and `d09e506` are pre-squash commits of `2cecc87` (#31), which the fork has. Superseded, waived |
| `refactor/inbound-map-storage` | `72f7b4f0d0acde96cc642d2dd73defe5540921e6` | `0c44dc6` and `72f7b4f` are pre-squash commits of `c3f9e61` (#24), which the fork has. Superseded, waived |
| `pr-61` | `4eb26fbd00387c88a4bcaf5c1d9e9e8dc4604b62` | 15 commits of the open PR #61. Rejected, waived (see below) |

All 42 upstream tags, `v0.0.1` to `v0.5.4`, are in the fork's history.

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

The script reads every pin from the checked commit, never from a hardcoded value:

| component | upstream | pin read from | pin on 2026-09-24 | line branch |
|---|---|---|---|---|
| `xray` | XTLS/Xray-core | `Dockerfile` `ARG XRAY_COMMIT` | `d2758a023cd7` (`v26.3.27`) | `main` |
| `sing-box` | SagerNet/sing-box | `Dockerfile` `ARG SINGBOX_VERSION`, an upstream tag | `v1.13.14` (`25a600db24f7`) | `oldstable` |
| `sing-quic` | SagerNet/sing-quic | the `require` line of the sing-box fork's `go.mod` at `SINGBOX_COMMIT`, fetched from `SINGBOX_REPO` | `v0.6.1` (`907fec5e8e6b`) | `main` |
| `mtg` | 9seconds/mtg | the node `go.mod` `require` of `github.com/9seconds/mtg/v2` (a pseudo-version commit); the `replace` commit on Free-Guy-IR/mtg must contain it | `d095108334de` (fork `c543d656a39a`) | `master` |

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
- **Patch files.** An `Upstream: <sha or GHSA id>` line in the header of a `patches/*.patch` file, meaning the lines
  before the first `diff --git`. Lines inside the diff are ignored.
- **Carried in 0.6.21.** The Xray patch files and commits of 0.6.21 carry `Upstream:` lines for:
  - `e5a9fb752e0dcc127dd1740316c853571c16052f` and `8f15190c230fe6975c9b18e31316c6bc494f3863`, the QUIC sniffer;
  - `711aea4e34b0a00073d2fea6577f28a5d7c9b5eb`, trusted X-Forwarded-For;
  - `cecc88f43cb12f7ba0517080f3d82daaa07f816a`, grpc 1.83.2, in the header of `xray-grpc-1.83.2.patch`;
  - `8ee131cbbba4e9ed26e62fe3900cb9fdfb06ab89`, the HTTP inbound 1xx panic;
  - `a2cec2e580e8ea1900701b11b425e0351de2ec71`, the DNS panic on a name over 255 bytes;
  - `d878fc83f8be0f56cebbc5fb9a9baf0aa48d9990`, the SS2022 dispatch panic;
  - `0bafca94868a4ec38cd3dba0f2ba7ec6f44bbf18`, the stats `GetOrRegister*` races, as an adapted backport;
  - `24e6f6d5513382d92256a80c5f2e7a6e16e5d6c7`, x/crypto 0.57.0 and x/net 0.59.0.
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

- Advisories: one, GHSA-5wf9-h793-w73c. It is waived as not applicable: a client-side `pinnedPeerCertSha256` check
  that only matters in users' apps.
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

- Nothing after the pin: no commit, no advisory, no release.

**node**

- `52d5aab` (grpc 1.84.0, #91) is held out of 0.6.21 and waived as `pending`: the Go vulnerability database still
  lists released v1.84.0 as affected by GO-2026-6443 (golang/vulndb #6511, #6580), and the fix is verified in v1.84.0
  `http2_server.go`. It is ported when the database entry is corrected.
- `be2c445` (#93) and `4a9d0cf` (#90) are ported by `40006fe` and `f9c4f05`. The node section of the run of 2026-09-25 lists
  33 upstream commits outside the target: 7 ported, 1 empty, 25 waived, 0 unported. It also lists 5 fork commits with
  an upstream parent, all allowlisted, and 1 upstream merge with no changes of its own.

0.6.21 takes five server-side Xray fixes early as patches. Each fix was written test-first: its test fails on the pin
and passes with the fix.

- `8ee131c`, the HTTP inbound 1xx panic: `xray-http-100-continue.patch`, verbatim.
- `a2cec2e`, the DNS query panic on a name over 255 bytes: `xray-dns-long-domain.patch`, verbatim.
- `d878fc8`, the SS2022 inbound invalid-destination panic: `xray-ss2022-dispatch.patch`, verbatim.
- `0bafca9`, the stats `GetOrRegister*` races, which cost billed bytes and online-map IPs:
  `xray-stats-register-race.patch`, an adapted backport (see below).
- `598bde7`, the Router API data race: upstream's refactor does not apply to the pin, so `xray-router-race.patch` is
  a fork fix. The commit stays waived as pending for the rest of the refactor.

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

### govulncheck on the patched Xray build

govulncheck v1.8.0 was run with `GOOS=linux` on `./main/...` of the pinned v26.3.27, with every 0.6.21 patch applied
(the patches as of node commit `d18d291`), on 2026-09-24.

- Before the 0.6.21 bumps, six ids were symbol-reachable: GO-2026-6443, GO-2026-6348 and GO-2026-6061 in grpc
  1.79.3, GO-2026-5970 in x/text, and GO-2026-5026 and GO-2026-4918 in x/net. `xray-grpc-1.83.2.patch` clears all
  six.
- GO-2026-6354 and GO-2026-6355 (x/crypto/ssh, fixed in 0.56.0) were module-level only. `xray-x-net-0.59.0.patch`
  takes upstream `24e6f6d`'s x/crypto 0.57.0 and clears them.
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

- **SagerNet/sing, sing-vmess, grpc-go and other Go dependencies.** The sing-box fork vendors or requires them. They
  are covered by govulncheck in CI, not by this check.
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
(r2-B review).

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
  - An entry written as an IPv4-mapped IPv6 prefix, for example `::ffff:127.0.0.0/104`, counts as invalid too, with
    its own error log naming the entry. Peers are unmapped before the check, so such an entry could never match; before
    this rule it parsed without error and silently trusted nothing (F-r2B-4). Write the IPv4 form, `127.0.0.0/8`.
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
    worker lock. Before, the cleanup of a revoked flow could remove the newer flow the same source had already opened.
- **Stock core:** unchanged. On the stock Xray core a removed or rotated Shadowsocks AEAD user keeps an open UDP flow
  until it has been idle for 2 minutes; the r2-B test reproduces this on the stock v26.3.27 core as well.
- **Residuals:**
  1. A UDP flow is bound to the first user that dispatches on it. Shadowsocks AEAD decodes a source with one user
     only, so this is exact there. For Shadowsocks 2022 several users could share one source address only behind a
     relay that multiplexes them onto one UDP socket; revoking the first of them then cuts the shared flow, and it
     forms again on the next packet with the users that remain.
  2. Shadowsocks 2022 UDP flows were already cut before this fix by its own user update; hysteria runs over QUIC and is
     not handled by the UDP worker.
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
- **Removal condition:** none while the node relies on `RevokeUserOperation`; it goes with the session revoke patch.

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
- **Upstream merges with changes of their own** (`node merge`). The commit list skips merge commits. So every upstream
  merge outside the target whose combined diff (`git show --cc`) is not empty must be ported or waived, like any other
  commit. A clean merge has an empty combined diff and is only counted.

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
- A `pending` waiver must carry `until:YYYY-MM-DD`, at most 30 days ahead, or an issue reference
  `issue:<owner>/<repo>#<number>`, or both; otherwise the line is malformed and the check exits 2.
  - After its `until:` date, the waiver no longer waives anything. Its item turns `UNPORTED`, and the run lists it under
    `EXPIRED WAIVERS`.
  - Extending a date is a decision, not a formality. A waiver that hides a live bug in shipped code gets a fix or an
    owner, not a new date.
  - `DRIFT_TODAY=YYYY-MM-DD` sets today's date for tests.
- The reason is mandatory and must say why, for example
  `superseded: the fork fixed this in <fork sha>` or `not-applicable: upstream release tooling`. A `pending` reason
  names the release or project that will take it.
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
suite, the real-kernel WireGuard tests, the MTProto fronting leg, govulncheck and the workflow policy check. Every
workflow that publishes something calls both of them first, on the same commit, and its publishing jobs `needs:` both:

| workflow | trigger | gated jobs | what it publishes |
|---|---|---|---|
| `docker-build-dev.yml` | push to a branch whose name has no `/` | `image` | `ghcr.io/free-guy-ir/node:<branch>`, moved only while the branch still points at the built commit |
| `docker-build.yml` | run by hand | `image` | `:edge` from `main`, the branch-named tag from another branch, or a custom tag; never `:latest` or a release-like tag |
| `release.yml` (Publish Release) | run by hand from a release tag | `assets`, `image`, `publish` | the draft release's zips and checksums, `:<tag>` and `:latest`, then the release itself |

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
