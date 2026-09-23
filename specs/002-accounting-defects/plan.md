# Implementation Plan: Node accounting defects

**Branch**: `release/v0.6.21` | **Date**: 2026-09-23 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/002-accounting-defects/spec.md`

## Summary

Four accounting fixes ship in the unreleased 0.6.21 (NodeVersion unchanged):

1. **MTProto** – `New` registers the configured instance tags before it seeds the initial users, so
   the scoping check (`wantsUser`) sees them and the first `ProxyOpts.Secrets` (and the middle-proxy
   secrets closure) already carry every user of the node's MTProto inbounds.
2. **L2TP** – a per-tag *counted state* (session identity -> rx/tx already counted) persisted in the
   L2TP run directory, loaded by `New`, and matched against live sessions and final records; the
   ip-down script writes the session identity into the final record; a live session whose interface
   also has a final record in the same poll is skipped; a Start hands the per-user tracker and the
   node-level totals to the next instance of the same tag.
3. **WireGuard** – the sampler reads every peer every tick; a new `Sample.Quiet` flag keeps the
   handshake-based online/endpoint semantics; flushes run before peer removal, full replacement,
   restart and interface deletion; `Tracker.RestartCounters` restarts re-added peers at zero while
   keeping pending deltas; sampling is serialised with sync operations; a Start hands the tracker to
   the next backend of the same interface.
4. **Direction** – `pkg/stats` labels `Rx` (bytes the node received from the client) as uplink and
   `Tx` as downlink; MTProto passes its client-to-Telegram bytes as `Rx` so its labels stay as today.

Design decisions and rejected alternatives are in [research.md](research.md); entities in
[data-model.md](data-model.md); interface contracts in [contracts/](contracts/).

## Technical Context

**Language/Version**: Go 1.26.x, module `github.com/pasarguard/node`; `GOTOOLCHAIN=local`

**Primary Dependencies**: `pkg/stats` (`Tracker`, `InterfaceCountersTracker`, `buildDeltaStats`);
`golang.zx2c4.com/wireguard/wgctrl`; the forked `mtglib`; xl2tpd/pppd ip-up/ip-down scripts

**Storage**: L2TP counted state as one small text file per inbound tag in the L2TP run directory
(same lifetime as the session and final-record directories); nothing else persisted

**Testing**: unit tests with fake devices and temporary session directories; RED on `eab2601`, GREEN
after; full gate on the shared test host in a throwaway `golang:1.26.6-alpine` container
(`--memory 1500m --init`): `make build_xray`, `make test_xray_patches`,
`TEST_INTEGRATION=true go test ./... -p 1`, `go test -race` on the changed packages, `go vet ./...`,
`gofmt -l`

**Target Platform**: Linux node containers (`linux/amd64`, `linux/arm64`)

**Project Type**: network daemon controlled by the PasarGuard panel

**Performance Goals**: WireGuard sampling cost grows from "peers with a fresh handshake" to "all peers"
(one `wgctrl` device read already returns all peers; only the tracker update grows, O(peers) per tick);
L2TP writes one small file per poll only when the counted state changed

**Constraints**: per-user billing paths of Xray and sing-box untouched; OpenVPN per-user path touched
only by the shared label function; no explanatory comments; no IP addresses in committed files;
NodeVersion stays 0.6.21; the shared test host has little free RAM and is used by other suites

**Scale/Scope**: largest WireGuard interface 212 peers; L2TP a handful of concurrent sessions per node

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Check | Status |
|-----------|-------|--------|
| I Committed Code Only | Tests run from committed worktrees; the gate runs on an exported committed tree | PASS |
| II Fail Before, Pass After | Every fix has a test run RED on `eab2601` and GREEN after, both outputs kept; vet, suite and race in the final gate | PASS |
| III No Explanatory Comments | Rule passed to every sub-agent; `git diff` scanned for added comment lines before each merge | PASS |
| IV Strict Patch Versioning | NodeVersion stays 0.6.21 (same unreleased release) | PASS |
| V Safe Rollout | No rollout in this feature; release rollout stays with spec 001 | N/A |
| VI Identity and Secrecy | Free-Guy-IR author, no trailer, no AI/vendor names, no IPs, explicit paths only, no push | PASS |
| VII Fork Seam | Changes stay in the owning packages; new behaviour in new files where possible (`backend/l2tp/counted.go`, `backend/l2tp/handover.go`, `backend/wireguard/handover.go`); failures are loud (one log line, never a silent zero) | PASS |
| Release Constraints: billing paths named | Spec names the L2TP, WireGuard, MTProto and label paths | PASS |

Post-design re-check: PASS (no new violation introduced by the data model or contracts).

## Project Structure

### Documentation (this feature)

```text
specs/002-accounting-defects/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── l2tp-counted-state.md
│   ├── accounting-handover.md
│   └── direction-labels.md
├── checklists/requirements.md
└── tasks.md
```

### Source Code (repository root)

```text
backend/mtproto/     mtproto.go, user.go (+ new test file)                      owner: MTProto agent
                     stats.go (label inputs only)                               owner: Direction agent
backend/l2tp/        l2tp.go, stats.go, sessions.go, serverconf.go, new files   owner: L2TP agent
                     l2tp_inbound_test.go (label assertions)                    owner: Direction agent
backend/wireguard/   jobs.go, stats.go, wireguard.go, user_*.go, new files      owner: WireGuard agent
                     stats_outbound_test.go (label assertions)                  owner: Direction agent
pkg/stats/           tracker.go + new tracker test file                         owner: WireGuard agent
                     interface_counters.go, interface_counters_test.go,
                     tracker_test.go (label assertions)                         owner: Direction agent
backend/openvpn/     *_test.go label assertions only                            owner: Direction agent
*/direction_test.go  new label tests in pkg/stats, wireguard, l2tp, openvpn, mtproto owner: Direction agent
controller/          test fixtures only if a label assertion breaks             owner: Direction agent
```

**Structure Decision**: four parallel worktrees `work/node-acct-{l2tp,wg,mtproto,dir}` on branches
`release/v0.6.21-acct-{l2tp,wg,mtproto,dir}` from `eab2601`, with the disjoint file ownership above.
New tests written by the L2TP, WireGuard and MTProto agents assert per-user totals or value sets, never
which label carries which value, so the Direction change cannot break them. The lead merges the four
branches into `release/v0.6.21` with `--no-ff`, runs the full gate, and an independent verifier
re-derives RED/GREEN for every fix and checks the Xray and sing-box per-user paths are unchanged.

## Complexity Tracking

| Item | Why Needed | Simpler Alternative Rejected Because |
|------|------------|-------------------------------------|
| In-process accounting hand-over (L2TP, WireGuard) | A Start drops the backend object and, for WireGuard, deletes the interface; flushed or unread deltas would die with it | Baselining alone (L2TP) or flushing into the dying tracker (WireGuard) still loses the unread deltas, which the spec forbids |
| Persisted L2TP counted state | The node process can restart inside the same container while the session and final-record files survive | An in-memory baseline only covers the in-process Start path |
