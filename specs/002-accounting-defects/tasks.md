# Tasks: Node accounting defects

**Input**: Design documents from `specs/002-accounting-defects/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: REQUIRED (constitution II and FR-013): every fix starts with tests run RED on `eab2601`.

**RED tree**: `eab2601` plus the new test files plus, only where a test needs it, behaviour-neutral
seams or API stubs (recorded in the KG with the diff). Every behavioural test must fail on behaviour in
that tree; a compile failure alone is not RED evidence.

**Rules for every task**: no explanatory comments in code or tests; no IP address in any committed
file; commits by `git -c user.name=Free-Guy-IR -c user.email=free-guy-ir@users.noreply.github.com`
with no trailer and no AI or vendor name; stage explicit paths only; never touch the node main tree or
any production container or host; nothing is pushed. New tests in the MTProto, L2TP and WireGuard
phases assert per-user totals or value sets, never which label carries which value.

## Format: `[ID] [P?] [Story] Description`

## Phase 1: Setup

- [X] T001 Create worktrees `work/node-acct-{mtproto,l2tp,wg,dir}` on branches `release/v0.6.21-acct-{mtproto,l2tp,wg,dir}` from `eab2601` via `git -C <node main repo> worktree add`
- [X] T002 Record the base test state of `./pkg/stats ./backend/mtproto ./backend/wireguard` (local) and `./backend/l2tp ./backend/openvpn` (Linux test binaries) on `eab2601` in the KG entity "node 0.6.21 release 2026-09-23"

## Phase 2: Foundational

No shared prerequisite: each story owns disjoint files (see plan.md, Source Code).

## Phase 3: User Story 1 - MTProto users authorised right after a Start (P1) - owner: MTProto agent

**Goal**: FR-005. **Independent test**: real `New()` with instances and users; no later sync call.

- [X] T003 [US1] Write RED tests in a new file `backend/mtproto/new_users_test.go`: through the real `New()` with one faketls instance (free 127.0.0.1 port) and a user whose inbounds carry that instance tag, the user's secret is in `secretsByID` and in `secretsForInstance(domain)` right after `New()`; a user with MTProto credentials but only foreign inbounds is not; a middle-proxy (ad_tag) instance's secrets closure lists the user right after `New()`; zero users gives an empty set; `Shutdown()` after each test
- [X] T004 [US1] Run T003 on `eab2601` and keep the failing output (expected: user missing after `New()`)
- [X] T005 [US1] Fix `backend/mtproto/mtproto.go` / `backend/mtproto/user.go` so the configured instance tags are known before the initial users are seeded and the first `ProxyOpts.Secrets` carries them; keep the scoping of `wantsUser` for `SyncUser`, `UpdateUsers`, `SyncUsers`
- [X] T006 [US1] Run T003 and the whole `./backend/mtproto` package GREEN, then with `-race`; commit tests and fix separately

## Phase 4: User Story 2 - L2TP billed once across restarts (P1) - owner: L2TP agent

**Goal**: FR-001..FR-004. **Independent test**: temporary session/final directories and fake counters.

- [X] T007 [US2] Make the run directories and the interface counter reader overridable from tests if they are not already (package variables in `backend/l2tp/sessions.go`), without changing production values
- [X] T008 [US2] Write RED tests in a new file `backend/l2tp/counted_test.go`: restart re-count of a live session (scenario 1), final record written after the old instance's last poll (scenario 2), never-seen session (scenario 3), final record and still-listed live session in the same poll (scenario 4), interface name reused after a session vanished without a final record (scenario 5), process restart with a persisted counted-state file, unreadable counted-state file, old-format final record without identity, Start hand-over of counted-but-unread per-user deltas
- [X] T009 [US2] Run T008 on `eab2601` (with only the test seam of T007 applied if needed, recorded) and keep the failing output
- [X] T010 [US2] Add the session identity (`pid`, `started`) to the final record written by the ip-down script in `backend/l2tp/serverconf.go`
- [X] T011 [US2] Implement the counted state (`(ifname, pid, started)` -> rx/tx; file format of contracts/l2tp-counted-state.md (shipped as `v2`, see T033); atomic write, mode `0600`; missing file = nothing counted; unreadable = everything present counted up to its current value, one log line; a failed write logs once per failure streak, keeps the in-memory state and retries at the next changing poll) in a new file `backend/l2tp/counted.go`
- [X] T012 [US2] Rewrite `poll` in `backend/l2tp/stats.go` to the contract's five steps; make it callable with the lock held for the final poll in `Shutdown`
- [X] T013 [US2] Implement the hand-over registry (deposit, take, put back; occupied key logs an error) in a new file `backend/l2tp/handover.go` and wire it into `New` and `Shutdown` in `backend/l2tp/l2tp.go`; forget users absent from the new user list
- [X] T014 [US2] Run T008 and the whole `./backend/l2tp` package GREEN (Linux test binary), then with `-race`; commit tests and fix separately

## Phase 5: User Story 3 - WireGuard billed for every byte (P2) - owner: WireGuard agent

**Goal**: FR-006..FR-009b. **Independent test**: fake device, fake manager.

- [X] T015 [US3] Write RED tests in new files `backend/wireguard/accounting_test.go` and `pkg/stats/tracker_accounting_test.go`: growth on a peer with a handshake older than 45 s is reported; online status and IP list unchanged for such a peer; unsampled growth survives a partial-sync removal; a full sync (replace) and a restart flush first and count later growth from zero (including growth larger than the old counter); Shutdown then New for the same interface reports flushed plus unread deltas once and releases peers no longer configured; `Sample.Quiet` and `RestartCounters` semantics in `pkg/stats`
- [X] T016 [US3] Run T015 on `eab2601` and keep the failing output (compile failures of not-yet-existing API are recorded as such; each behavioural test must also fail behaviourally once the API exists without the behaviour)
- [X] T017 [US3] Add `Sample.Quiet` and `(*Tracker).RestartCounters` to `pkg/stats/tracker.go` (zero value keeps today's behaviour); a sample for an entry marked removed comes from a new source whose counters started at zero, so it keeps the pending delta and counts the sample from zero (today the re-add path counts only the pending delta and drops the first sample's bytes)
- [X] T018 [US3] Sample every peer every tick in `backend/wireguard/jobs.go` with `Quiet` for zero or older-than-45 s handshakes; serialise the sampler with `syncMu`
- [X] T019 [US3] Flush before removal (`backend/wireguard/user_partial_sync.go`), before replace plus `RestartCounters` only after a successful replace (`backend/wireguard/user_full_sync.go`, `restartLocked` in `backend/wireguard/wireguard.go`), and before `Close` in `Shutdown`; keep the documented lock order `syncMu` -> `wg.mu` -> `m.mu` (a Shutdown that serialises with syncs takes `syncMu` before `wg.mu`); a failed kernel call restarts nothing
- [X] T020 [US3] Implement the hand-over registry in a new file `backend/wireguard/handover.go`; adopt in `newWithManagerFactory` after the interface exists (restart counters, remove entries for unconfigured keys), put back on failure
- [X] T021 [US3] Update `backend/wireguard/jobs_test.go` only where it pins the old 45 s gate; run T015 and the packages `./backend/wireguard ./pkg/stats` GREEN, then with `-race`; commit tests and fix separately

## Phase 6: User Story 4 - Uplink means client to server (P3) - owner: Direction agent

**Goal**: FR-010..FR-012. **Independent test**: known rx/tx per backend.

- [X] T022 [US4] Write RED tests in new files `pkg/stats/direction_test.go`, `backend/wireguard/direction_test.go`, `backend/l2tp/direction_test.go`, `backend/openvpn/direction_test.go`: uplink equals client-to-server bytes per user, per interface/instance and node level; for every case the per-name sum equals `rx+tx` and the value set equals `{rx, tx}`; an MTProto case in `backend/mtproto/direction_test.go` pins today's (correct) labels
- [X] T023 [US4] Run T022 on `eab2601` and keep the failing output (MTProto case passes on both)
- [X] T024 [US4] Swap the labels in `buildDeltaStats` in `pkg/stats/interface_counters.go`; swap the values MTProto passes in `backend/mtproto/stats.go` (per-user sample, per-inbound, node-level)
- [X] T025 [US4] Update label assertions that pinned the old mapping in `pkg/stats/interface_counters_test.go`, `pkg/stats/tracker_test.go`, `backend/wireguard/stats_outbound_test.go`, `backend/l2tp/l2tp_inbound_test.go`, `backend/openvpn/stats_trackers_test.go` (and any other failing label assertion outside the other agents' files); leave `backend/mtproto/inbound_stats_test.go` unchanged and green
- [X] T026 [US4] Run the affected packages GREEN and with `-race`; commit tests and fix separately

## Phase 7: Integration, verification, hand-back - owner: lead

- [X] T027 Scan each agent branch diff for added comment lines, IPv4 literals other than 127.0.0.1 and AI/vendor names, and check `controller.NodeVersion` is still 0.6.21; reject and return on any hit
- [X] T028 Merge `release/v0.6.21-acct-{mtproto,l2tp,wg,dir}` into `release/v0.6.21` with `--no-ff` as Free-Guy-IR; resolve nothing by hand (disjoint files); if a combined test breaks, fix it on `release/v0.6.21` with a test
- [ ] T029 Full gate on the shared test host in one throwaway `golang:1.26.6-alpine` container (`--memory 1500m --init`, `free -m` first, one heavy container at a time): `make build_xray`, `make test_xray_patches`, `TEST_INTEGRATION=true go test ./... -p 1`, `-race` on the changed packages, `go vet ./...`, `gofmt -l`
- [ ] T030 Independent verifier (did not write the code) re-derives RED on `eab2601` and GREEN on the merged head for every fix, and checks that the Xray and sing-box per-user paths are byte-identical to `eab2601` (`git diff eab2601..HEAD -- backend/xray backend/singbox` empty) and OpenVPN's per-user path changed only by the label function and the Start hand-over (T036)
- [ ] T031 Clean the shared test host (containers, `/root/dev/node-acct-*`), remove the extra worktrees after merging, record everything in the KG entity "node 0.6.21 release 2026-09-23", and hand back: per defect root cause (file:line), fix, RED/GREEN evidence, merged HEAD, gate and race results, verifier verdict, cleanup state, residuals (inverted historical charts for WireGuard, L2TP, OpenVPN; carry-over for Xray and sing-box out of scope)

## Phase 8: Findings added during implementation and review (all test-first, RED recorded)

- [X] T032 Tracker re-add dropped the first sample of a re-added removed entry (`pkg/stats/tracker.go`, fixed with T017); L2TP samples applied and users forgotten outside the instance lock could re-bill a stale sample (`backend/l2tp/stats.go`, d17ce22); a stopped L2TP/WireGuard instance kept pointers to handed-over trackers (01e3e52, 054d4ac)
- [X] T033 L2TP watermark grace for sessions reaped before their final record, final records billed only after a successful remove (b9d0b64; grace raised from 30 to 360 polls after the second review round); consumed finals keep their values as watermarks and ip-up writes session files atomically (0571426, 529b669); old-format finals never match consumed watermarks, counted state `v2` with `consumed` and `final` lines (e4bfe3e)
- [X] T034 Tracker rollback rebases only the dipped direction (17dfdf5)
- [X] T035 MTProto lifecycle: Serve/Shutdown WaitGroup race contained node-side, Shutdown no longer holds the backend lock while proxies drain (4e43484, 0e84d61); goroutine leak per Start from the mtg allowlist, bind-failure proxy leak, Started/Restart listener race (a6a8237); bounded Serve wait logged (0aff517); test port helper probes loopback (685dcfa)
- [X] T036 Scope extension (owner rule: no known bug): MTProto and OpenVPN hand unread accounting to the next backend on every Start, exactly once across consecutive Starts (1bc4766 tests, e55fd11 `CountFromZero`, 472d2d7, 143c253)
- [X] T037 Real-kernel check on the shared test host (NET_ADMIN-only container): `wg setconf` (ReplacePeers) restarts peer counters and handshake to zero; the four netns end-to-end WireGuard tests need CAP_SYS_ADMIN and run in the release pull request CI

- [X] T038 Second review round, residuals closed by owner decision: MTProto middle-proxy relays still running after the bounded Shutdown drain are carried as retired sources and drained into the next backend's ledger exactly once (c1ab389 tests, 217ea7c); OpenVPN live check with a real openvpn 2.6.20 server and client (udp and tcp) on the shared test host: SIGTERM emits CLIENT:DISCONNECT with bytes_sent/bytes_received immediately (server exit 22/42 ms), and those totals exceed the last BYTECOUNT_CLI, so the bounded management drain captures the exact final tail and no status snapshot is needed

- [X] T039 Panel keepalive (u4-2): the gRPC server accepts pings every 5 s without a stream and pings an idle panel after 30 s, closing it after 10 s without an ack (02a017f test, 7192af8)
- [X] T040 Pre-auth hardening and fork fixes (u4-5, u4-3, u4-1, fix/node-critical): API key checked in the gRPC tap before any message is read and MaxRecvMsgSize 48 MiB (b5845a2, d489663); fail fast on an invalid or nil API_KEY (c807ed0, 88162e2); keep-alive watchdog serialised with Start and re-checked, Start no longer holds the controller lock during the build (990de31, 40248f2 with Upstream trailers a69d7ed and d44ca8e); backend management requires the current client (7bbdf3a, de3493f); queued SyncUser fans out to every backend (19fe5a6, ee20728); a down primary no longer hides healthy extras (7124a6e, 673524b); MTProto Restart refused after Shutdown (cf96ea2, 3a3157b), which makes the unshipped fork commits 120fb49 and 71a54eb superseded (their tests pass)
- [X] T041 Dependencies and toolchain (u4-4): grpc 1.83.2, gopsutil 4.26.8, x/sys 0.48 with Upstream trailers (f53ffa2), Go 1.26.6 in every builder stage, go.mod and CI (345febd), govulncheck CI job (47ef6ee): 11 reachable vulnerabilities on 7192af8, none after
- [X] T042 Upstream parity: docs/upstream-parity.md records the rejection of StartOrAttach and tracks upstream PR #78; scripts/upstream_drift.sh with .github/workflows/upstream-drift.yml and .github/upstream-waivers.txt fails on any upstream main or dev commit that is neither ported nor waived (4c1b0ba, 7ef54ca, 12f0fc7); on the merged head it reports 5 ported, 1 empty, 2 waived, 0 unported

## Phase 9: Findings left open at the 2026-09-29 pause (all test-first: RED on the parent, GREEN on the fix, -race and an interleaving test for each concurrency item, mutants of the fix killed, verified by an agent that did not write it)

- [ ] T043 Drift suite never stops silently: `sha_of` in `scripts/upstream_drift_test.sh` must not end its pipeline early under `pipefail`, and the suite fails when it ran fewer cases than it declares (F-R2C2-HARNESS) - branch `release/v0.6.21-acct-drift6`
- [ ] T044 Drift check refuses a pagination link outside the GitHub API before the token reaches it, pinned by a case (845e23c) - branch `release/v0.6.21-acct-drift6`
- [ ] T045 Drift Dockerfile reading fails closed: a shell comment, a heredoc body, a SHELL instruction, a later reversal of the patch and a patch file replaced before its apply never count as an applied port (F-R2C2-D11, RED 0c38c9b) - branch `release/v0.6.21-acct-drift6`
- [ ] T046 Waivers that rest on an upstream branch state name that branch and lapse once the commit is reachable from any other upstream ref (F-R2C2-D10) - branch `release/v0.6.21-acct-drift6`
- [ ] T047 Upstream Xray 08cb6e6 (WireGuard proxy startup races) ported with a test or waived with its reachability evidence - branch `release/v0.6.21-acct-drift6`
- [ ] T048 Every drift mutant (loose_until, no_calendar, no_issue_check, patch_scope_off, applied_check_off and those of T043-T046) killed or proven equivalent on the final head with the full case count (F-D6-gaps) - branch `release/v0.6.21-acct-drift6`
- [ ] T049 OpenVPN: a failed client listing after a management reconnect is retried until every carried session is reconciled, so a capped user is never locked out, and a status 3 without a Client ID column still settles them (F-R2C2-OVM1, F-OVM-R2, RED 42d0457) - branch `release/v0.6.21-acct-ovm`
- [ ] T050 OpenVPN: real-time notifications that arrive while a command reply is awaited are dispatched in order, and an unterminated tail is never taken as a reply (F-OVM-R1) - branch `release/v0.6.21-acct-ovm`
- [ ] T051 OpenVPN: `Backend.Restart` is serialised with `Shutdown` and refused after it (F-OVM-R4) - branch `release/v0.6.21-acct-ovm`
- [ ] T052 sing-box: the health restart takes the sync lock, restarts with the active config and is refused after `Shutdown` (F-SB-stale) - branch `release/v0.6.21-acct-sbstale`
- [ ] T053 Restart sweep: WireGuard, L2TP and MTProto restarts pinned against stale configs, concurrent syncs and revival after `Shutdown` - branch `release/v0.6.21-acct-sbstale`
- [ ] T054 MTProto: after a give-up a run counts as serving only once it accepted a client, and an idle run is not a healthy run (F-VMN3-1, RED 8861bcf) - branch `release/v0.6.21-acct-mtgnode3`
- [ ] T055 Controller tests: `controller/rest` and `controller/rpc` listen on their own ephemeral ports with a Start deadline that holds under load (F-RPC-5s) - branch `release/v0.6.21-acct-flake`
- [ ] T056 Controller tests: the Start-build user sync test picks a port no other process can take between its Start rounds (F-C1-flake) - branch `release/v0.6.21-acct-flake`
- [ ] T057 Non-author verification of `release/v0.6.21-acct-l2tpfix2` 5207e59: every RED on its parent, GREEN, -race under dash and bash, fresh mutants, seeded interleaving stress, hostile review
- [X] T058 Real-kernel WireGuard MTU tests in a Linux gate: own network namespace, NET_ADMIN only, the wireguard module loaded only if absent and unloaded after, `lsmod`, `ip link` and `ip addr` identical before and after, and mutants of the MTU code fail (GATE-WGK) - verification only, no code branch: run against the node integration head 33d30fd on the shared test host on 2026-10-07; 3/3 runs 2 PASS 0 FAIL, mutants M1 (no MTU apply), M2 (no keep-kernel guard) and M3 (Restart skips the MTU) each fail, module set and links unchanged afterwards; evidence in `acct-lead-state/evidence/node-misc-wgk/`

## Dependencies & Execution Order

- T001-T002 first. Phases 3, 4, 5 and 6 run in parallel (disjoint files) once T001 is done.
- Within each phase: tests -> RED run -> implementation -> GREEN run -> commit.
- Phase 7 after all four phases; T030 after T029.

## Parallel Example

```text
MTProto agent: T003-T006   L2TP agent: T007-T014   WireGuard agent: T015-T021   Direction agent: T022-T026
```

## Implementation Strategy

MVP = User Story 1 (live outage). All four stories ship together in 0.6.21; none is released alone.
