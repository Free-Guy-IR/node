---

description: "Task list for node release 0.6.21"
---

# Tasks: Node release 0.6.21

**Input**: Design documents from `specs/001-release-0-6-21/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: required. The spec demands fail-before/pass-after (FR-009, SC-006); every test task runs red
on the unfixed code before its implementation task.

**Organization**: by user story. US1 = Xray session kill (already implemented, verify only),
US2 = F3 traffic series, US3 = version, build and fleet rollout.

## Format: `[ID] [P?] [Story] [owner] Description. Files: ...`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1, US2, US3
- **[owner]**: `[impl]` implementation agent, `[verify]` independent verification agent (never the
  author of the change it checks), `[release]` release engineer, `[ops]` rollout operator,
  `[owner]` the human owner
- **Files**: exact paths touched, or `Files: none`

## Rules for every task

- Work in `/Users/mohammadahadpor/Projects/pasarguard/work/node-v0621` on `release/v0.6.21`. Never touch,
  stash, check out or reset the main tree `/Users/mohammadahadpor/Projects/pasarguard/node`.
- No explanatory comments or docstrings in handwritten code; do not add to or remove existing comments
  except by T019.
- Commit with `git -c user.name=Free-Guy-IR -c user.email=free-guy-ir@users.noreply.github.com commit`,
  no Co-Authored-By trailer, no AI tool or model name in any message or file.
- Stage explicit paths only (`git add -- <path> ...`); never `git add -A` or `git add .`. Of the Spec Kit
  files only `.specify/.gitignore`, `.specify/integrations/speckit.manifest.json`, `.specify/memory/`,
  `.specify/scripts/`, `.specify/templates/`, `.specify/workflows/workflow-registry.json` and `specs/`
  are ever committed; every other file `specify init` created (the agent-integration files and the agent
  skills directory) stays untracked because it names the tool vendor.
- No server address, password, token or key in any committed file or commit message; hosts are named by
  node id or role, and their details live in the KG entity "Node fleet map 2026-09-23".
- Versions: `NodeVersion` `0.6.21`, tag `v0.8.21-session-kill`; nothing else.
- Never call the panel's `POST /api/node/{id}/update`. Test node before production.
- No secret on any process argv: the panel admin password goes through a 0600 file into curl
  (`--data @file` or stdin), database reads use a 0600 option file (`--defaults-extra-file`), GitHub API
  calls send the token as `-H @file` from a 0600 file or use `gh` with its stored login. Delete temporary
  secret files when the task ends.
- Log each finished task to the KG entity "node 0.6.21 release 2026-09-23" (what, where, result,
  rollback info) when it finishes.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: confirm the base and the toolchain the image ships.

- [ ] T001 [verify] Confirm the worktree is on `release/v0.6.21`, `git merge-base HEAD 2d8e11f` is `2d8e11f`, `git log --format=%h 2d8e11f..4af18c2` lists the 9 session-kill commits `fdde319 6d56a6d 4b4b576 08a776b 851a871 4aa06d0 485965a ce93e5c 4af18c2`, and `git diff 4af18c2 HEAD --stat` shows only `.specify/` and `specs/`. Files: none
- [ ] T002 [verify] Build the patched xray with `make build_xray` (`GOTOOLCHAIN=local`, Go 1.26.3), run `make test_xray_patches`, and install the patched binary plus the stock v26.3.27 binary as `XRAY_STOCK_EXECUTABLE_PATH` exactly as `.github/workflows/run-tests.yml` does. Files: none (writes untracked `xray-core/`)
- [ ] T003 [verify] Baseline run on the untouched tree: `go vet ./...` and `go test ./... -p 1` with certificates generated as CI does; record pass/fail per package in the KG so later regressions are attributable. Files: none

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: facts the US2 design and the US3 success checks depend on.

- [ ] T004 [P] [verify] Read `experimental/v2rayapi/stats.go` of the pinned sing-box fork `4a87f503` (the Dockerfile's sing-box stage) and confirm `QueryStats` with reset zeroes each counter by an atomic swap; record the file and line in the KG. If it is load-then-store, add a note to research.md R4 and flag it to `[owner]` before T013. Files: none
- [ ] T005 [P] [ops] Capture pre-release baselines with read-only SELECTs on the panel database (credentials from a 0600 option file, never argv): for node 63 and node 33, hourly sums of `node_usages`, `node_inbound_usages`, `node_user_usages` for the last 24 h; store the ratios in the KG (needed by SC-002 and SC-003). Files: none

**Checkpoint**: T001-T005 done; US1, US2 and the version part of US3 can start in parallel.

---

## Phase 3: User Story 1 - A limited Xray user loses service within seconds (Priority: P1) MVP

**Goal**: prove the committed session-kill fix still behaves as its evidence says on the release tree.

**Independent Test**: quickstart.md section 3 on the release tree; the live stopwatch test runs in US3
(T035) on node 56 before any other Xray node.

- [ ] T006 [P] [US1] [verify] Run `go test -race -count=1 -run 'TestRemovedUserEstablishedSessionsAreClosed|TestSyncUser|TestUpdateUsers|TestRemovingUsersTheCoreDoesNotHoldIsSilent|TestRemovedUserStreamsOnAConnectionAuthenticatedBeforeAResyncAreRefused' ./backend/xray/...` against the patched xray, and `TestRemovingUsersTheCoreDoesNotHoldIsSilent` against the stock binary; expect 8/8 transport cases and the fallback to pass (the patch's own `TestResyncThenRevokeRefusesAConnectionThatHadNotDispatchedYet` runs in T002). Files: none
- [ ] T007 [P] [US1] [verify] Confirm the image recipe still asserts the patch: `Dockerfile` xray stage copies `patches/xray-session-revoke.patch`, applies it after `xray-commander-message-size.patch`, and greps `revocable.Wrap(w.dispatcher)` = 2, `retireUser(...)`, `RevokeUserOperation`; `git ls-files patches/` lists exactly those two patches; `grep -c router-rules Dockerfile` is 0. Files: none

**Checkpoint**: US1 verified on the release tree.

---

## Phase 4: User Story 2 - The inbound and node traffic series agree (Priority: P2)

**Goal**: sing-box and OpenVPN node-level and per-inbound reads each see all traffic (FR-005 to FR-009),
per contracts/stat-reset-semantics.md and data-model.md.

**Independent Test**: the two new test files pass on the fix and fail on the unfixed tree
(quickstart.md sections 1 and 2).

### Tests for User Story 2 (write first, run red)

- [X] T008 [P] [US2] [impl] Create `backend/singbox/stats_ledger_test.go` (package `singbox`): an in-process fake `statscmd.StatsServiceServer` with atomic counters that `Swap(0)` on reset and filter by `Patterns` substring (see `backend/singbox/api/stats_test.go` for the listener pattern), and a `&SingBox{client: c}` dialing it. Cases: (a) 1000 B on `inbound>>>Hysteria2>>>traffic>>>downlink`, Outbounds reset then Inbounds reset, both report 1000; (b) reverse order, both 1000; (c) Inbounds reset=false then reset=true both report the same total, the next reset read reports only newer traffic; (d) Inbound(name) and Outbound(name) by tag; (e) counters zeroed between reads (core restart) lose nothing collected; (f) 50 concurrent Outbounds and Inbounds reset reads while traffic is added, each series sums to the exact total under `-race`; (g) a fake drain error returns an error and a later read still reports the full balance; (h) UsersStat reset=true reaches the fake with `Patterns == ["user>>>"]` and `Reset_ == true`, and returns the fake's user counters unchanged; (i) a tag whose counter disappears from the fake after a drain (inbound removed) is reported once more by each consumer's next reset read and never again. Use only API that exists at `4af18c2`. Files: backend/singbox/stats_ledger_test.go
- [X] T009 [P] [US2] [impl] Create `backend/openvpn/stats_trackers_test.go` (package `openvpn`): a `&Backend{instances: map[string]*instanceProcess{...}, statsTracker: stats.New()}` with one `newInstanceProcess(&InstanceConfig{Tag: "ovpn"}, ...)` whose `mgmt` is `newManagementClient("ovpn", "", nil)`; set and grow `closedTotals` under `stateMu`. Cases: first reads set baselines; add 1000/2000 B; Outbounds reset then Inbounds reset both report 1000 down / 2000 up; reverse order likewise; Inbound("ovpn") and Outbound("ovpn") likewise; unknown instance name still errors; UsersStat output equals the 0.6.20 behaviour for the same samples. Use only API that exists at `4af18c2`. Files: backend/openvpn/stats_trackers_test.go
- [ ] T010 [US2] [verify] Red run on the unfixed tree: `go test -count=1 ./backend/singbox/ ./backend/openvpn/ -run 'Ledger|Trackers'`; cases (a)(b)(d)(f) of T008 and the order cases of T009 MUST fail on reported bytes, not on compilation; save the failing output to the KG. If any fails to compile or passes, return it to `[impl]`. Files: none
- [X] T011 [US2] [impl] Commit the red tests: `git add -- backend/singbox/stats_ledger_test.go backend/openvpn/stats_trackers_test.go`, message `test: node-level and per-inbound reads must not drain each other`. Files: backend/singbox/stats_ledger_test.go, backend/openvpn/stats_trackers_test.go

### Implementation for User Story 2

- [X] T012 [US2] [impl] In `backend/singbox/api/stats.go` add `func (c *Client) DrainInboundCounters(ctx context.Context) ([]*statscmd.Stat, error)` returning `QueryStats(ctx, "inbound>>>", true)`'s stats, and a new exported `func BuildStatResponse(stats []*statscmd.Stat) *common.StatResponse` that returns `buildStatResponse(stats)`. The existing `buildStatResponse` and all its call sites, and the `GetUsersStats`, `GetUserStats` and `QueryStats` bodies, stay byte-identical (additions only). Files: backend/singbox/api/stats.go
- [X] T013 [US2] [impl] Create `backend/singbox/inbound_ledger.go`: `type ledgerConsumer int` with `consumerOutbounds`, `consumerInbounds`; `type inboundLedger struct { mu sync.Mutex; pending [2]map[string]int64 }` whose zero value works (maps created lazily); `func (l *inboundLedger) collect(ctx context.Context, client *api.Client, consumer ledgerConsumer, tag string, reset bool) (*common.StatResponse, error)`: lock; drain; on error return it with no credit and no clear; add each drained value to both maps; snapshot the consumer's non-zero entries (only names with prefix `inbound>>>` + tag + `>>>` when tag is non-empty); build the response with `api.BuildStatResponse`; when reset delete exactly the snapshotted names from that consumer's map. A nil client returns an error instead of panicking. Files: backend/singbox/inbound_ledger.go
- [X] T014 [US2] [impl] Add field `inbounds inboundLedger` to `SingBox` in `backend/singbox/singbox.go`, and in `backend/singbox/stats.go` route `StatType_Outbounds` and `StatType_Outbound` to `s.inbounds.collect(ctx, s.client, consumerOutbounds, request.GetName(), request.GetReset_())` and `StatType_Inbounds`/`StatType_Inbound` to the same with `consumerInbounds` (empty name for the plural types). Leave the `UsersStat`/`UserStat` cases and every existing comment untouched. Done: the singular `Outbound`/`Inbound` types go through `collectTag`, which keeps the 0.6.20 `tag required` error for an empty name. Files: backend/singbox/singbox.go, backend/singbox/stats.go
- [X] T015 [P] [US2] [impl] In `backend/openvpn/process.go` add `inboundStatsTracker *stats.InterfaceCountersTracker` to `instanceProcess`, initialised with `stats.NewInterfaceCountersTracker()` in `newInstanceProcess`. In `backend/openvpn/stats.go` give `instanceStat` and `instancesStat` a `tracker func(*instanceProcess) *stats.InterfaceCountersTracker` parameter; Inbound/Inbounds pass the inbound tracker, Outbound/Outbounds pass `statsTracker`. `DeltaClamped`, `userStat` and the UsersStat case are unchanged; existing comments untouched. Files: backend/openvpn/process.go, backend/openvpn/stats.go
- [X] T016 [US2] [impl] Green run: `go test -count=1 -race ./backend/singbox/... ./backend/openvpn/... ./pkg/stats/...` passes; `gofmt -l` is empty for the touched files; commit with `git add -- backend/singbox/api/stats.go backend/singbox/inbound_ledger.go backend/singbox/singbox.go backend/singbox/stats.go backend/openvpn/process.go backend/openvpn/stats.go`, message `stats: keep node-level and per-inbound traffic apart on sing-box and OpenVPN`. Files: the six paths above
- [ ] T017 [US2] [verify] Independent check of T011-T016: re-run T010's command on `4af18c2` plus only the test files in a scratch worktree (red) and on HEAD (green); `git diff 4af18c2..HEAD --stat -- . ':!specs' ':!.specify'` lists only the T008-T016 paths; `git diff 4af18c2..HEAD -U0 -- '*.go' | grep -E '^\+\s*(//|/\*)'` is empty; `git diff 4af18c2..HEAD -- pkg/stats backend/xray backend/l2tp backend/wireguard backend/mtproto` is empty; the `GetUsersStats`/`GetUserStats` hunks are absent from the diff. Remove the scratch worktree. Files: none
- [X] T018 [US2] [owner] Answer the stale-comment question from plan.md Complexity Tracking: the pre-existing comment blocks above the Outbounds case in `backend/singbox/stats.go` and above `GetStats` in `backend/openvpn/stats.go` now describe the old shared-counter routing; remove them, or leave them? **Answer (owner, 2026-09-23): leave the existing comment blocks untouched.** Files: none
- [X] T019 [US2] [impl] Only if T018 says remove: delete exactly those two comment blocks, commit `git add -- backend/singbox/stats.go backend/openvpn/stats.go` with message `stats: drop comments that describe the old routing`. If T018 says leave: no change. Done: T018 said leave, so no change was made. Files: backend/singbox/stats.go, backend/openvpn/stats.go
- [ ] T020 [US2] [verify] If T019 changed code: confirm the removal is deletion-only (no replacement text) and `go test ./backend/singbox/... ./backend/openvpn/...` still passes. Either way record the T018 decision in the KG. Files: none

**Checkpoint**: US2 green, reviewed, per-user paths byte-identical.

---

## Phase 5: User Story 3 - The fleet runs 0.6.21 and reports it (Priority: P3)

**Goal**: version 0.6.21, image `v0.8.21-session-kill` from committed code, rolled out in the fleet
map's waves with digest pins and a rollback per node (FR-003, FR-004, FR-010 to FR-019).

**Independent Test**: after each wave every upgraded node is `connected` with `node_version == "0.6.21"`
on the new digest, and usage keeps arriving (quickstart.md section 6).

### Version (tests first)

- [X] T021 [P] [US3] [impl] Add `TestNodeVersionIsPinned` to `controller/base_info_test.go` asserting `NodeVersion == "0.6.21"`; run it and confirm it fails with `0.6.20`. Files: controller/base_info_test.go
- [X] T022 [US3] [impl] Set `const NodeVersion = "0.6.21"` in `controller/controller.go`; `go test ./controller/...` green; commit `git add -- controller/controller.go controller/base_info_test.go`, message `chore: report 0.6.21`. Files: controller/controller.go, controller/base_info_test.go

### Review, CI and image

- [ ] T023 [US3] [verify] Full gate on HEAD: `go vet ./...`; `go test ./... -p 1` against the patched xray with the stock binary configured; `go test -race ./backend/singbox/... ./backend/openvpn/... ./backend/xray/...`. Files: none
- [ ] T024 [US3] [verify] Independent external review of `git diff 4af18c2..HEAD -- . ':!specs' ':!.specify'`, then a whole-release independent external review (session kill + F3 + version + rollout plan), both through the machine's review gate so its approval is recorded for the exact HEAD sha that T028 pushes; fix every real finding through `[impl]` with a new red/green cycle, refute false ones with evidence in the KG; any commit after approval repeats this task. No build before both approve. Files: none
- [ ] T025 [US3] [owner] Confirm: (1) image and git tag `v0.8.21-session-kill` with NodeVersion `0.6.21` and no alias tag; (2) pushing `release/v0.6.21` and opening the PR to `main`; (3) merging by a local no-fast-forward merge commit authored as Free-Guy-IR noreply (T029); (4) publishing as a GitHub pre-release so `latest` does not move. **Answer (owner, 2026-09-23): (1) confirmed, image and git tag `v0.8.21-session-kill` with NodeVersion `0.6.21`; (2) to (4) not yet answered.** Files: none
- [ ] T026 [US3] [release] Clean-tree guard: `git status --porcelain --untracked-files=no` is empty; `git diff --name-only 4af18c2..HEAD` lists only the Spec Kit paths allowed in the rules above and the code paths of T008-T022; `git log -p origin/main..HEAD` contains no IPv4 address other than loopback, no credential and no AI tool or model name; every commit in `origin/main..HEAD` is authored and committed by `Free-Guy-IR <free-guy-ir@users.noreply.github.com>` with no trailer. Files: none
- [ ] T027 [P] [US3] [release] Handoff note (non-blocking) to the owner of the main tree's uncommitted router-rules work: rebase onto the release; put its `COPY patches/xray-router-rules-snapshot.patch` after the session-revoke `COPY` and its `git apply` + `grep -q "rules := r.rules" app/router/router.go` after the session-revoke asserts; run a combined `git apply` of all three patches on xray `d2758a0` (glob order commander-message-size, router-rules-snapshot, session-revoke, as `make build_xray` applies them) in a scratch directory using a read-only copy of their patch. Send by message; nothing is written to the main tree. Files: none
- [ ] T028 [US3] [release] After T025: push `release/v0.6.21` (the machine's pre-push gate must pass; never bypass it), open PR `release/v0.6.21` -> `main` with a body built from `PR_BODY.md` of `4af18c2` plus the F3 and version sections (no AI name), and poll every GitHub Actions run for the head sha (token from a 0600 file via `-H @file`, or `gh`) until all are `completed/success`; on a failure fetch the job log, fix the root cause through `[impl]`, push, re-verify. Files: none
- [ ] T029 [US3] [release] After PR approval by `[owner]`: in a scratch worktree (`git worktree add --detach <scratch> origin/main`, never the main tree) run `git -c user.name=Free-Guy-IR -c user.email=free-guy-ir@users.noreply.github.com merge --no-ff release/v0.6.21` and `git push origin HEAD:main`, so `fdde319..4af18c2` and the release commits keep their shas (no squash, no rebase, no GitHub-generated merge identity) and the PR closes as merged; if branch protection refuses the push, stop and return to `[owner]`. Verify every workflow run for the merge sha. Create annotated tag `v0.8.21-session-kill` on the merge commit as Free-Guy-IR; publish a GitHub pre-release for it; wait for `docker-build.yml` (image, signed) and `release.yml` (binaries) and the `:main` dev-image run to be `completed/success`; record the manifest-list digest and the `latest` digest before and after (they must be equal). Files: none
- [ ] T030 [US3] [verify] `docker buildx imagetools inspect ghcr.io/free-guy-ir/node:v0.8.21-session-kill` shows linux/amd64 and linux/arm64 and the digest recorded in T029; on the node 33 host during T032, `docker run --rm --entrypoint xray <image> version` reports the pinned build and the node log shows `Starting Node: v0.6.21`. Files: none

### Rollout scripts (panel host, outside the repository)

- [ ] T031 [US3] [ops] Create parametrized copies of the panel-host `inner20.sh`, `verify20.sh` and `rc1.sh` per contracts/rollout-scripts.md: inputs `TAG`, `DIGEST`, `EXPECTED_VERSION`, `PASSWORD_FILE`, `NODE_ID`, `COMPOSE_DIR`; both compose files rewritten from `image: ghcr.io/free-guy-ir/node[:@]...` to `ghcr.io/free-guy-ir/node@$DIGEST` (tag and digest pins); backups `*.bak-v821-<ts>`; the admin password read from a 0600 file via stdin or `--data @file`, never on argv; disk guard before pull; rollback path to `sha256:6c5420a4f386140334c1f36020b2741cca6eb684e4d9d5c28217497e0685ccfd` verified with `EXPECTED_VERSION=0.6.20`. Dry-run the image-line rewrite on copies of one tag-pinned and one digest-pinned compose pair; run the verify script once while `ps -eo args` is sampled and confirm the password never appears. `deploy_l2tp_node.sh` is not used (it ignores node 70's override pin). The old `*20.sh` scripts stay untouched. Files: none in the repository (panel host only)

### Rollout (stop at the first failed gate; roll back that node; report)

- [ ] T032 [US3] [ops] Step 1, node 33 on the test/build host (OpenVPN, image smoke): check free disk (6.2 GB free recorded on 2026-09-23) is at least twice the compressed image, keep the rollback image, upgrade with the T031 scripts, pass the FR-016 gate. Files: none
- [ ] T033 [US3] [ops] Nodes 69 (MTProto) then 70 (L2TP) on the same host, one at a time, with the T031 scripts; FR-016 gate each. Files: none
- [ ] T034 [US3] [ops] Step 2, node 56 (lowest-traffic Xray): upgrade and pass the FR-016 gate. Files: none
- [ ] T035 [US3] [verify] Stopwatch cutoff test on node 56 per quickstart.md section 4 (panel actions through the UI or through the API with the password from a 0600 file) (vless-xhttp, trojan-xhttp, vless-tcp, vmess-tcp; victim + bystander; throwaway users deleted afterwards). Pass = SC-001; on fail roll node 56 back and stop the release. Files: none
- [ ] T036 [US3] [ops] W1: nodes 47, 57, 65; FR-016 gate per node. Files: none
- [ ] T037 [US3] [ops] W2: nodes 27, 23, 52, 58; FR-016 gate per node. Files: none
- [ ] T038 [US3] [ops] W3: nodes 64, 21, 10, 30 (node 30's host needs sudo, per the fleet map); FR-016 gate per node. Files: none
- [ ] T039 [US3] [ops] W4, one at a time with the operator watching: node 40 then node 41 (same host), then node 68 (multi-core: Xray and WireGuard both back), then node 71 (multi-core, flaky SSH: keep a second session open); FR-016 gate per node. Files: none
- [ ] T040 [US3] [ops] W5: node 20 alone (highest traffic); FR-016 gate. Files: none
- [ ] T041 [US3] [ops] W6: node 63 last (sole Hysteria2 endpoint, sing-box); FR-016 gate. Disabled nodes 16, 24, 37, 53, 55, 66 are never touched. Files: none
- [ ] T042 [US3] [verify] F3 check one full hour after T041 (quickstart.md section 5): node 63 per-inbound / node-level within 0.95..1.05 (SC-002) and per-user / node-level within 0.02 of the T005 baseline (SC-003); node 33 per-inbound / node-level within 0.95..1.05 over the hour after T032. Read-only SELECTs only, credentials from a 0600 option file. Files: none
- [ ] T043 [US3] [verify] Fleet audit: all 21 previously connected nodes `connected` with `node_version == "0.6.21"` on the T029 digest; the 6 disabled nodes unchanged (SC-004); every rollback, if any, took under 10 minutes (SC-005). Files: none

**Checkpoint**: release complete.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T044 [owner] Decide whether to promote the pre-release to a full release and whether `latest` should move now that the fleet is on the digest. **Answer (owner, 2026-09-23): do NOT move `:latest`.** Files: none
- [ ] T045 [ops] Record in the KG entity "node 0.6.21 release 2026-09-23": image digest, per-node before/after digests and backup paths, gate results, T035 timings, T042 ratios, any rollback; add an observation to "Node fleet map 2026-09-23" that its production nodes now run 0.6.21. Files: none
- [ ] T046 [release] Delete scratch worktrees and local xray build directories created by T002, T010 and T017; leave the main tree untouched. Files: none

---

## Dependencies & Execution Order

- Setup T001 -> T002 -> T003. Foundational T004, T005 after T001 (parallel).
- US1 (T006, T007) after T002; independent of US2.
- US2: T008, T009 parallel -> T010 -> T011 -> T012 -> T013 -> T014; T015 parallel with T012-T014 (other
  files) -> T016 -> T017 -> T018 -> T019 (if remove) -> T020. T013 needs T004's answer.
- US3 version: T021 -> T022, parallel with US2 (other files).
- US3 release: T023 needs T006, T007, T020, T022 -> T024 -> T025 -> T026 -> T028 -> T029 -> T030
  (with T032). T027 any time after T007.
- US3 rollout: T031 before T032; T032 -> T033 -> T034 -> T035 -> T036 -> T037 -> T038 -> T039 -> T040 ->
  T041 -> T042 -> T043.
- Polish after T043.

## Parallel Example

```text
After T003:   T004 [verify]  |  T005 [ops]  |  T006 [verify]  |  T007 [verify]
US2 tests:    T008 [impl] backend/singbox/stats_ledger_test.go  |  T009 [impl] backend/openvpn/stats_trackers_test.go
US2 impl:     T012-T014 (sing-box files)  |  T015 (OpenVPN files)  |  T021-T022 (controller files)
Release prep: T027 handoff  |  T031 scripts (panel host)  while T024 review runs
```

## Implementation Strategy

1. MVP = US1: the session kill is already implemented; T006-T007 prove it on the release tree, and it
   reaches users through US3's rollout (node 56 stopwatch first).
2. US2 lands before the build so the fleet is touched once; if US2 slips, the owner may decide to ship
   US1 + version alone, which needs a new spec amendment (not the default).
3. US3 rolls out wave by wave; every node has a digest rollback and the release stops at the first
   failed gate.

## Task owners and files (summary)

| Owner | Tasks | Repository files touched |
|---|---|---|
| impl | T008, T009, T011-T016, T019, T021, T022 | backend/singbox/stats_ledger_test.go, backend/openvpn/stats_trackers_test.go, backend/singbox/api/stats.go, backend/singbox/inbound_ledger.go, backend/singbox/singbox.go, backend/singbox/stats.go, backend/openvpn/process.go, backend/openvpn/stats.go, controller/controller.go, controller/base_info_test.go |
| verify | T001-T004, T006, T007, T010, T017, T020, T023, T024, T030, T035, T042, T043 | none |
| release | T026-T029, T046 | none (git refs, PR, tag, pre-release) |
| ops | T005, T031-T034, T036-T041, T045 | none in the repository (panel-host scripts, node compose files) |
| owner | T018, T025, T044 | none |
