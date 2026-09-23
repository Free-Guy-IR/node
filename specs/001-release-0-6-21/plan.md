# Implementation Plan: Node release 0.6.21

**Branch**: `release/v0.6.21` | **Date**: 2026-09-23 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/001-release-0-6-21/spec.md`

## Summary

Release 0.6.21 carries the A9-b Xray session-kill commits `fdde319..4af18c2` unchanged, pins the
reported version to 0.6.21, and fixes audit F3 so the node-level and per-inbound traffic reads stop
draining each other: on sing-box the node takes over the reset of the `inbound>>>` counters and keeps
one uncollected balance per consumer; on OpenVPN the per-inbound read gets its own
`InterfaceCountersTracker` per instance. The image `ghcr.io/free-guy-ir/node:v0.8.21-session-kill` is
built by the repository's release workflow from a GitHub pre-release, and rolled out node 33 first, then
the fleet in the waves of the KG entity "Node fleet map 2026-09-23" with digest-pinned, parametrized
scripts that keep the admin password off every argv. Design decisions are in [research.md](research.md).

## Technical Context

**Language/Version**: Go 1.26.3 (module `github.com/pasarguard/node`); xray and sing-box built with
`GOTOOLCHAIN=local`

**Primary Dependencies**: xray-core `d2758a0` + `patches/xray-commander-message-size.patch` +
`patches/xray-session-revoke.patch`; sing-box fork `4a87f503` (v2ray_api stats, clash_api hot reload);
`pkg/stats` (`Tracker`, `InterfaceCountersTracker`)

**Storage**: none on the node; the panel stores `node_usages`, `node_inbound_usages`, `node_user_usages`

**Testing**: `go test ./... -p 1` with the xray from `make build_xray` installed as CI does;
`make test_xray_patches`; `go vet ./...`; `go test -race` for the new ledger and trackers

**Target Platform**: Linux containers, `linux/amd64` and `linux/arm64`, `ghcr.io/free-guy-ir/node`

**Project Type**: network daemon (gRPC/REST node controlled by the PasarGuard panel) plus its image

**Performance Goals**: victim cut within 5 s of the panel action (measured 87-369 ms end to end in the
A9-b evidence); one extra sing-box stats RPC per node-level or per-inbound read, no more

**Constraints**: panel reads Outbounds and Inbounds with reset once per cycle, possibly concurrently;
per-user reads untouched; no explanatory comments; strict patch versioning; test/build host has 6.2 GB
free and is not used to build

**Scale/Scope**: 21 connected nodes (6 disabled untouched); code change limited to 7 files plus 3 new
files; 3 rollout scripts parametrized

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Gate | Pre-design | Post-design |
|---|---|---|---|
| I Committed code only | Image built by the release workflow from tag `v0.8.21-session-kill`; no working tree is a build context | PASS | PASS |
| II Fail before, pass after | F3 tests and the version pin are committed and run red on `4af18c2` before the fix commits; full suite green against patched xray; `-race` on ledger and trackers | PASS | PASS |
| III No explanatory comments | New files and hunks carry none; the existing comment blocks in `backend/singbox/stats.go` and `backend/openvpn/stats.go` are left untouched | PASS | PASS (see Complexity Tracking) |
| IV Strict patch versioning | `0.6.20` -> `0.6.21`; `v0.8.20-degraded-start` -> `v0.8.21-session-kill`; owner confirms the tag | PASS | PASS |
| V Safe rollout | Node 33 smoke, node 56 stopwatch before any other Xray node; never `POST /api/node/{id}/update`; both compose files, tag and digest pins; digest pin; backup + rollback digest recorded first; password from a 0600 file | PASS | PASS |
| VI Identity and secrecy | Commits as Free-Guy-IR noreply including the merge commit (local no-ff merge), explicit paths, no trailer, no AI name in files or history, no IP or credential in the repo; CI verified on the PR sha, the merge sha and the tag; every secret (panel, database, GitHub) passed by 0600 file or stdin | PASS | PASS |
| VII Fork seam | Ledger in a new file; OpenVPN change is one field and a tracker selector; `pkg/stats` and per-user paths untouched; drain error returned, not swallowed | PASS | PASS |

## Design

### D1. Version pin (FR-003)
- `controller/base_info_test.go`: new test `TestNodeVersionIsPinned` asserting `NodeVersion == "0.6.21"`.
  It fails on `4af18c2` (`0.6.20`).
- `controller/controller.go:25`: `const NodeVersion = "0.6.21"`.

### D2. F3 sing-box: node-side inbound ledger (FR-005, FR-007)
- `backend/singbox/api/stats.go`: add `func (c *Client) DrainInboundCounters(ctx) ([]*statscmd.Stat, error)`
  calling `QueryStats(ctx, "inbound>>>", true)`, and a new exported `BuildStatResponse` that returns the
  existing `buildStatResponse(stats)`. Additions only: `buildStatResponse`, its six call sites,
  `GetUsersStats`, `GetUserStats` and `QueryStats` are byte-identical.
- `backend/singbox/inbound_ledger.go` (new): `inboundLedger` with a mutex and one
  `map[string]int64` per consumer (`consumerOutbounds`, `consumerInbounds`), keyed by the full counter
  name. `collect(ctx, client, consumer, tag, reset)`: under the mutex, drain, add every drained value to
  both maps, then snapshot the consumer's map (only names under `inbound>>>tag>>>` when `tag != ""`), and
  when `reset` delete exactly the snapshotted names from that consumer's map. A drain error returns
  before anything is credited or cleared. Zero-value entries are not reported. The zero value of
  `inboundLedger` is ready to use (maps created lazily), so `&SingBox{client: c}` works in tests on both
  the base and the fix.
- `backend/singbox/singbox.go`: `SingBox` gains `inbounds inboundLedger`; its lifetime is the backend
  object, so it survives `restartCoreWithConfig` (the client and the ledger stay; the core's counters
  restart from zero, which the ledger treats as ordinary new traffic).
- `backend/singbox/stats.go`: Outbounds/Outbound -> `s.inbounds.collect(ctx, s.client, consumerOutbounds, name, reset)`;
  Inbounds/Inbound -> same with `consumerInbounds`. UsersStat/UserStat lines unchanged.

### D3. F3 OpenVPN: second tracker (FR-006, FR-007)
- `backend/openvpn/process.go`: `instanceProcess` gains `inboundStatsTracker *stats.InterfaceCountersTracker`,
  initialised in `newInstanceProcess` next to `statsTracker`.
- `backend/openvpn/stats.go`: `instanceStat` and `instancesStat` take a selector
  `func(*instanceProcess) *stats.InterfaceCountersTracker`; Inbound(s) pass the inbound tracker,
  Outbound(s) pass the existing one. `userStat` and `UsersStat` unchanged; `DeltaClamped` unchanged.

### D4. Tests (FR-009)
- `backend/singbox/stats_ledger_test.go` (new): an in-process fake `StatsServiceServer` with real
  semantics (atomic counters, `Swap(0)` on reset, `Patterns` substring filter), wired to a `SingBox`
  whose `client` dials it. Cases map 1:1 to spec US2 scenarios 1, 2, 3, 5, 6 plus: single Inbound and
  Outbound by tag; concurrent Outbounds/Inbounds under `-race` with exact totals; a drain error leaves
  both balances intact; the user read reaches the fake with pattern `user>>>` and the caller's reset flag.
- `backend/openvpn/stats_trackers_test.go` (new, package `openvpn`): a `Backend{instances, statsTracker}`
  with one `newInstanceProcess(...)` whose `mgmt` is `newManagementClient(tag, "", nil)` with
  `closedTotals` set under `stateMu`; traffic grows by editing `closedTotals`. Outbounds reset then
  Inbounds reset both report the full delta; the reverse order likewise; Inbound by name likewise;
  UsersStat unchanged.
- Both new test files use only API that exists at `4af18c2` (`GetStats`, `SingBox{client}`,
  `newInstanceProcess`, `newManagementClient`), so the red run on the base is a behavioural failure,
  not a compile failure.
- Red run: both test files and the version pin are committed first and executed on top of `4af18c2`;
  the failing output is saved to the KG release entity.

### D5. Dockerfile conflict (FR-010)
The release Dockerfile is the one committed at `4af18c2`. The main tree's uncommitted
`xray-router-rules-snapshot.patch` and its Dockerfile hunk stay out. Handoff to that owner: rebase onto
the release; add its `COPY` after the session-revoke `COPY`, and its `git apply` + assert after the
session-revoke asserts; `make build_xray` applies `patches/xray-*.patch` in glob order
(commander-message-size, router-rules-snapshot, session-revoke), so run a combined apply of all three on
`d2758a0` before merging.

### D6. CI, tag and image (FR-004, FR-011, FR-011a)
1. Open PR `release/v0.6.21` -> `main`; the test workflow
   (patched xray + stock fallback) must be green on the head sha; every run for every pushed sha checked.
2. Owner confirms tag `v0.8.21-session-kill`; merge with a local `git merge --no-ff` in a scratch
   worktree of `origin/main`, authored as Free-Guy-IR noreply, pushed to `main` (commits keep their
   shas, the PR closes as merged; a refused push goes back to the owner); create an annotated tag on the
   merge commit (Free-Guy-IR) and publish a GitHub **pre-release** for it. `docker-build.yml` pushes only
   `ghcr.io/free-guy-ir/node:v0.8.21-session-kill` (multi-arch, signed); `release.yml` attaches binaries.
   `latest` does not move. Merging to `main` also runs the dev-image workflow (`:main`); that is expected.
3. Record the manifest-list digest; verify both platforms, `xray version` = `d2758a0` build, the binary
   contains `sessionrevoke`, and the node logs `Starting Node: v0.6.21`.

### D7. Rollout (FR-012 to FR-019)
Source of truth for hosts, compose dirs, pins and access: KG "Node fleet map 2026-09-23".
- Scripts: copy the panel-host `inner20.sh`, `verify20.sh`, `rc1.sh` to new versioned copies taking
  `TAG`, `DIGEST`, `EXPECTED_VERSION` and `PASSWORD_FILE`; rewrite `image: ghcr.io/free-guy-ir/node[:@]...`
  in both compose files to `ghcr.io/free-guy-ir/node@<digest>`; back up to `.bak-v821-<ts>`; the admin
  password is read from a 0600 file into curl via `--data @file` or stdin, never argv. Contract in
  [contracts/rollout-scripts.md](contracts/rollout-scripts.md). `deploy_l2tp_node.sh` is not used.
- Gate per node (FR-016): container image == new digest; reconnect; panel status exactly `connected`
  and `node_version == "0.6.21"`; log `Starting Node: v0.6.21` + successful user sync, no error/panic;
  usage rows keep arriving.
- Order: node 33 (image smoke; confirm free disk before pull and keep the rollback image), then 69, 70;
  node 56 + stopwatch test; W1 47, 57, 65; W2 27, 23, 52, 58; W3 64, 21, 10, 30; W4 40 then 41, 68, 71
  one at a time; W5 20; W6 63, then the F3 ratio check after 1 h. Stop on the first failed gate.
- Rollback: restore `.bak-v821-<ts>` files (digest `sha256:6c5420a4...`, 0.6.20), `up -d`, reconnect,
  same gate with `0.6.20`.

## Project Structure

### Documentation (this feature)

```text
specs/001-release-0-6-21/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── stat-reset-semantics.md
│   └── rollout-scripts.md
├── checklists/requirements.md
└── tasks.md
```

### Source Code (repository root)

```text
controller/
├── controller.go                 # NodeVersion
└── base_info_test.go             # version pin test
backend/singbox/
├── api/stats.go                  # drain + exported builder
├── inbound_ledger.go             # new
├── singbox.go                    # ledger field
├── stats.go                      # routing to the ledger
└── stats_ledger_test.go          # new
backend/openvpn/
├── process.go                    # inboundStatsTracker
├── stats.go                      # tracker selector
└── stats_trackers_test.go        # new
```

**Structure Decision**: existing single Go module; changes stay inside the two affected backends and
the controller constant. Rollout scripts live on the panel host, outside the repository.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| `backend/singbox/stats.go` and `backend/openvpn/stats.go` keep their old explanatory comment blocks, which will describe the pre-fix routing | Principle III forbids removing existing comments unless the owner asks | Deleting them silently breaks the same rule; the owner is asked in T018 of tasks.md whether to remove them |
