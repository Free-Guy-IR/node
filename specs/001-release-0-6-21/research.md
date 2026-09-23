# Research: Node release 0.6.21

All Technical Context items are resolved; no NEEDS CLARIFICATION remains.

## R1. Why the two traffic series drain each other (F3)

- **Finding**: at `4af18c2`, `backend/singbox/stats.go` answers `Outbounds` with
  `s.client.GetInboundsStats(ctx, reset)` and `Inbounds` with the same call; both are
  `QueryStats("inbound>>>", reset)` against sing-box's v2ray_api, whose reset zeroes the counter at the
  source. `backend/openvpn/stats.go` answers `Outbound(s)` and `Inbound(s)` with
  `instance.statsTracker.DeltaClamped(rx, tx, reset)`, one baseline per instance. The panel reads
  `Outbounds` (`record_usages.py`) and `Inbounds` (`app/fork/jobs/inbound_usage.py`) with reset every
  cycle, so whichever read comes second sees only what arrived in between.
- **Evidence**: KG "Bug status on production v5.34.24 - sections F and G" (F3): node 63 inbound
  499,331,095 B vs node 72,592,460,784 B over 7 buckets; per-user/node = 1.005.
- **Not affected**: Xray (`outbound>>>` and `inbound>>>` are separate counters), WireGuard (no inbound
  read), L2TP (`inboundStats` and `interfaceStats` baselines), MTProto (`inboundStat`/`outboundStat`).

## R2. sing-box fix shape

- **Decision**: the node owns the reset. Each node-level or per-inbound read drains `inbound>>>` with
  reset under one mutex and credits every drained value to both consumers' balances; a consumer's read
  returns its balance and, only when its own request resets, clears the names it returned.
- **Rationale**: correct under any read order, under concurrency (serialized), and across core
  restarts (a restart zeroes the source, which only means smaller future drains; nothing held by the
  node is lost). Uses the one RPC the node already makes. No restart detection needed.
- **Alternatives considered**:
  - One read keeps resetting, the other reads without reset and subtracts its last seen value (the
    brief's wording): wrong, the resetting read rolls the counter back underneath the tracker and the
    amount it took is unknowable.
  - Both reads without reset, each subtracting its last seen value: works until the core restarts; then
    `current < last` must be guessed as a restart and traffic between the last read and the restart is
    lost or double counted. Also leaves sing-box counters growing forever.
  - Track real `outbound>>>` counters for the node-level read (list the direct outbound in
    `v2ray_api.stats.outbounds`): changes the generated config and the meaning of the node-level series
    (egress vs ingress) on the sole Hysteria2 endpoint; larger blast radius for a LOW item.

## R3. OpenVPN fix shape

- **Decision**: a second `InterfaceCountersTracker` per `instanceProcess` for Inbound(s); Outbound(s)
  keeps the existing tracker.
- **Rationale**: the source (`mgmt.AllUserStats()` summed) is cumulative and never reset by a read, so a
  separate baseline is the complete fix; the node-level series keeps continuity; same shape as L2TP.
- **Alternatives considered**: a ledger like sing-box (unnecessary, the source is already cumulative);
  moving the node-level read to the new tracker (would restart the series the dashboards read).

## R4. sing-box reset atomicity

- **Decision**: verify, do not assume. The ledger serializes the node's own drains, so correctness
  within the node does not depend on it; the verify task reads `experimental/v2rayapi/stats.go` in the
  pinned fork `4a87f503` to confirm reset is a per-counter swap, so traffic arriving during a drain is
  never dropped.

## R5. Version and tag

- **Decision**: `NodeVersion` `0.6.21`; image and git tag `v0.8.21-session-kill`; no alias.
- **Rationale**: git tags `v0.8.19-filter-assets` -> `b27b6da` (0.6.19) and `v0.8.20-degraded-start` ->
  `2d8e11f` (0.6.20) prove the paired series; fleet audits map 0.6.N to v0.8.N. Slug follows the
  `v0.8.N-<feature>` habit.

## R6. CI and image path

- **Finding**: `run-tests.yml` runs on `pull_request` and on pushes to `dev`/`wg`; `docker-build-dev.yml`
  runs on pushes to branches matching `'*'`, which excludes names with `/`; `docker-build.yml` runs on
  `release: published` (pre-release -> version tag only, else also `latest`) or `workflow_dispatch`
  (always adds `latest`); `release.yml` builds binaries on `release: published`.
- **Decision**: CI evidence from PR `release/v0.6.21` -> `main`; image from a GitHub pre-release of
  `v0.8.21-session-kill`; no local build on the 6.2 GB test/build host.
- **Alternatives considered**: `workflow_dispatch` (moves `latest`, which any node still following
  `latest` would pick up outside its wave); local `buildx` (needs a working GHCR write token on a build
  machine and a clean export; kept only as a fallback with `git archive` of the tag).

## R7. Rollout mechanics

- **Decision**: reuse the v0.8.20 panel-host scripts only as parametrized copies: `TAG`, `DIGEST`,
  `EXPECTED_VERSION`, `PASSWORD_FILE`; both compose files rewritten to `@<digest>`; `.bak-v821-<ts>`
  backups; password never on argv; `deploy_l2tp_node.sh` retired for this release.
- **Rationale**: the fleet map found `inner20.sh`/`verify20.sh` hardcode v0.8.20/0.6.20, `verify20.sh`
  and `rc1.sh` put the password on the curl argv, and `deploy_l2tp_node.sh` ignores node 70's override.
  A digest pin cannot be moved by a later tag push, which is what the owner wants for a staged rollout.
