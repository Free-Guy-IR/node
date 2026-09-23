# Feature Specification: Node release 0.6.21

**Feature Branch**: `release/v0.6.21` (feature directory `specs/001-release-0-6-21`)

**Created**: 2026-09-23

**Status**: Draft

**Input**: User description: "Node release 0.6.21 of the Free-Guy-IR PasarGuard node fork: ship the Xray
session-kill fix (A9-b, already on `fix/xray-session-kill` @ `4af18c2`), fix audit item F3 (the inbound
and node traffic series drain each other on sing-box and OpenVPN), and build and roll the release out
test node first, then the fleet in the waves of the KG entity 'Node fleet map 2026-09-23'."

**Base**: `4af18c2` = `main` @ `2d8e11f` (production: NodeVersion 0.6.20, image `v0.8.20-degraded-start`)
plus the 9 session-kill commits `fdde319..4af18c2`.

## Clarifications

### Session 2026-09-23

Answered by the spec author without the owner, choosing the safest option each time; the reason is
recorded with each answer.

- Q: Can the sing-box fix let one read keep resetting the shared counter while the other read reads it
  without reset and subtracts the last value it saw? → A: No. The sing-box counter is itself reset by a
  read with reset, so the non-resetting read would see the counter fall back and could not know how much
  the other read took. The node takes over the reset: it collects the sing-box inbound counters itself
  and keeps one uncollected balance per consumer (node-level, per-inbound), each cleared only by that
  consumer's own read with reset. Safest because a core restart behaves exactly as today and no restart
  guessing is needed.
- Q: On OpenVPN, which read gets the new, separate baseline? → A: The per-inbound read gets a new
  baseline; the node-level read keeps the existing one. The source there is already cumulative, so a
  second baseline is enough, and the node-level series (the one dashboards and node totals use) keeps
  its continuity through the upgrade. Same shape as the L2TP backend's two baselines.
- Q: Should the image be tagged `0.6.21` as the brief says, or continue the fork's `v0.8.N-<slug>`
  series? → A: `v0.8.21-session-kill` for the image and git tag, `0.6.21` for the reported node version.
  The series has always paired `v0.8.N-<slug>` with `0.6.N` (last: `v0.8.20-degraded-start` = 0.6.20);
  an image tag `0.6.21` would sort below `v0.8.20` and start a third numbering. No alias tag. Flagged
  for owner confirmation before the tag is created.
- Q: How does the release avoid the main tree's uncommitted router-rules Dockerfile hunk? → A: The
  release is built only from the pushed release tag (so from committed code), never from a working tree.
  The router-rules work stays with its owner, who rebases onto the release later and appends its patch
  after the session-revoke patch; a combined patch-apply check is handed to that owner and does not block
  this release.
- Q: The release branch name contains a slash; does pushing it trigger a wrongly tagged dev image, and
  where does the CI evidence come from? → A: Neither dev-image nor test workflow runs on a push to
  `release/v0.6.21`: the dev-image workflow's branch filter `'*'` does not match a slash, and the test
  workflow runs only on pull requests and on `dev`/`wg`. CI evidence therefore comes from a pull request
  `release/v0.6.21` → `main`. The image comes from the repository's release workflow run by a GitHub
  **pre-release** for tag `v0.8.21-session-kill`, which pushes only the version tag and does not move
  `latest` (a manual workflow run would move `latest`, and any node still following `latest` would then
  upgrade outside the waves). The 6.2 GB test/build host is not used to build.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A limited Xray user loses service within seconds (Priority: P1)

The operator limits, disables, expires or deletes a user in the panel while that user has an Xray
tunnel open. The user's open tunnel stops carrying traffic within seconds and every new connection
the user attempts is refused. Other users on the same inbound are not disturbed. Today the open
tunnel keeps running at full speed until the client closes it, so over-limit users keep being served.

**Why this priority**: it stops unpaid traffic (about 50 GB across 24 over-limit users at the time of
the A9-b measurement) and is the reason for the release.

**Independent Test**: the stopwatch cutoff test used for the A9-b evidence: a victim and a bystander
each run a sustained download on vless-xhttp, trojan-xhttp, vless-tcp and vmess-tcp; the operator limits
the victim in the panel; the tester records the time until the victim's stream stops, the bytes the
victim receives after removal, the bytes the victim gets on a new connection, and the bystander's
throughput afterwards.

**Acceptance Scenarios**:

1. **Given** a victim streaming on any of the four Xray transports, **When** the panel limits the victim,
   **Then** the victim's stream stops within 5 seconds of the panel action.
2. **Given** the victim has been removed, **When** the victim opens a new connection or a new stream on
   an already open multiplexed connection, **Then** the victim receives 0 bytes.
3. **Given** a bystander streaming on the same inbound, **When** the victim is cut, **Then** the
   bystander keeps streaming without interruption.
4. **Given** an ordinary change to a user who stays on the inbound (for example a data-limit edit),
   **When** the panel syncs that user, **Then** the user's open sessions are not cut.
5. **Given** a node whose Xray core lacks the session-revoke support, **When** a user is removed,
   **Then** the node logs one fallback line and behaves exactly like release 0.6.20.

---

### User Story 2 - The inbound traffic series and the node traffic series agree (Priority: P2)

The operator compares a sing-box or OpenVPN node's per-inbound traffic with its node-level traffic.
Both describe the same traffic, so over any period they must be approximately equal. Today every
collection cycle the two reads take turns draining one shared counter, so the per-inbound series only
sees the residue (node 63: inbound 499,331,095 B vs node 72,592,460,784 B, 145x low). Billing is not
affected and must stay unaffected.

**Why this priority**: it is a reporting defect (audit F3, LOW), not a billing or availability defect;
it rides this release because the release is already going out.

**Independent Test**: automated tests that perform a node-level read and a per-inbound read with reset
in the same cycle against a traffic source that really resets; both reads must report the full traffic.
The tests fail on `4af18c2` and pass on the release.

**Acceptance Scenarios**:

1. **Given** 1000 B of traffic on a sing-box inbound, **When** the panel reads node-level traffic and then
   per-inbound traffic, both with reset, **Then** each read reports 1000 B.
2. **Given** the same traffic, **When** the reads happen in the opposite order, **Then** each reports 1000 B.
3. **Given** a read without reset followed by a read with reset of the same kind, **Then** the second
   read reports the same total, and the next read with reset reports only newer traffic.
4. **Given** an OpenVPN instance with traffic, **When** node-level and per-inbound traffic are both read
   with reset in one cycle, **Then** each reports the full traffic since its own previous reset.
5. **Given** any of the above, **When** per-user traffic is read, **Then** results and reset behaviour
   are exactly as in release 0.6.20.
6. **Given** the sing-box core restarts between two collection cycles, **Then** traffic already
   collected by the node is neither lost nor counted twice.

---

### User Story 3 - The fleet runs 0.6.21 and reports it (Priority: P3)

Every connected production node runs the new image and reports version 0.6.21, so the panel and
fleet audits can tell the new release from 0.6.20. The rollout reaches a test node first, then the
fleet in small waves, and any node can be put back on 0.6.20 in minutes.

**Why this priority**: it is the delivery vehicle for Stories 1 and 2; it has value only once they
pass.

**Independent Test**: after each wave, the panel lists every upgraded node as connected with version
0.6.21, its container runs the new image digest, and usage rows keep arriving.

**Acceptance Scenarios**:

1. **Given** the new image, **When** it is installed on the test node (node 33), **Then** the node
   reconnects, reports 0.6.21 and its usage keeps arriving.
2. **Given** step 2 on node 56 (the lowest-traffic Xray node), **When** the stopwatch cutoff test runs,
   **Then** User Story 1's acceptance scenarios pass there before any other Xray node is touched.
3. **Given** a node whose compose override pins the image by tag or by digest, **When** it is upgraded,
   **Then** the override is updated too and the container runs the new digest.
4. **Given** a failed gate on any node, **When** the operator rolls it back, **Then** the node runs
   the previous digest, reports 0.6.20 and is connected, and the rollout stops.

---

### Edge Cases

- The panel reads node-level and per-inbound traffic concurrently.
- A traffic consumer never reads (a panel without the fork's per-inbound job): what it has not
  collected stays bounded by the number of inbounds, not by time.
- An inbound is removed from the configuration while it still holds traffic nobody has collected:
  that traffic is reported once on the next read with reset, then forgotten.
- The OpenVPN aggregate dips for one poll when a user reconnects: each series keeps the existing
  clamp-to-zero behaviour on its own.
- The main tree's uncommitted router-rules work (its xray patch and Dockerfile hunk) exists on the
  build machine: it must not reach the release image.
- The test/build host has 6.2 GB of free disk: an image pull there must fit, and old images are
  pruned only after the rollback digest is confirmed to stay.
- A node's SSH is flaky (node 71) or the host is shared by two panel nodes (nodes 40 and 41): those
  nodes are upgraded one at a time with the operator watching.
- A multi-core node (68, 71) runs Xray and WireGuard together: both cores must come back.
- The version tag the brief names (`0.6.21`) differs from the fork's image-tag series (`v0.8.N-<slug>`);
  resolved in Clarifications.
- A node still follows the floating `latest` tag: publishing as a pre-release keeps `latest` where it is,
  so that node cannot upgrade outside its wave.

## Requirements *(mandatory)*

### Functional Requirements

**Session kill (A9-b)**

- **FR-001**: The release MUST contain the commits `fdde319..4af18c2` of `fix/xray-session-kill`
  unchanged.
- **FR-002**: The session-kill behaviour MUST be re-verified on the release commit by the existing
  automated suite and by the stopwatch cutoff test on node 56 before W1.

**Version**

- **FR-003**: The node MUST report version `0.6.21`, and an automated test MUST pin that exact value.
- **FR-004**: The release image and git tag MUST be `v0.8.21-session-kill`, the next step after
  `v0.8.20-degraded-start` in the fork's series, with no alias tag; the owner confirms the tag before it
  is created.

**F3 traffic series**

- **FR-005**: On the sing-box backend, the node-level read and the per-inbound read MUST each report all
  inbound traffic, independently of the other's resets: the node collects the sing-box inbound counters
  itself and keeps one uncollected balance per consumer, cleared only by that consumer's read with reset.
- **FR-006**: On the OpenVPN backend, the per-inbound read MUST get its own reset baseline per instance;
  the node-level read MUST keep its existing baseline.
- **FR-007**: Per-user traffic reads (the billing path) on every backend MUST be unchanged.
- **FR-008**: The Xray, WireGuard, L2TP and MTProto traffic reads MUST be unchanged; they already keep
  separate sources or baselines.
- **FR-009**: Each F3 behaviour MUST be covered by an automated test that fails on `4af18c2` and passes
  on the release.

**Build**

- **FR-010**: The release image MUST be built only from committed code on the release commit; the main
  tree's uncommitted router-rules patch and Dockerfile hunk MUST NOT be in it. The router-rules owner
  receives a handoff note with the patch order and a combined patch-apply check.
- **FR-011**: The published image MUST be multi-architecture (amd64, arm64), produced by the
  repository's release workflow from a GitHub pre-release of tag `v0.8.21-session-kill` (so `latest` does
  not move), and identified by digest in every rollout step.
- **FR-011a**: CI evidence MUST come from a pull request `release/v0.6.21` → `main` whose test workflow
  passes on the release commit; every workflow run for every pushed sha MUST end `completed/success`.

**Rollout**

- **FR-012**: The rollout MUST follow the waves of the KG entity "Node fleet map 2026-09-23" in this
  order, stopping on the first failed gate: step 1 node 33 (image smoke on the test/build host), then
  nodes 69 and 70 on the same host; step 2 node 56 with the stopwatch test; W1 nodes 47, 57, 65; W2 nodes
  27, 23, 52, 58; W3 nodes 64, 21, 10, 30; W4 one at a time nodes 40 and 41 (shared host), node 68, node 71;
  W5 node 20 alone; W6 node 63 last, followed by the F3 check.
- **FR-013**: Disabled nodes 16, 24, 37, 53, 55 and 66 MUST NOT be touched.
- **FR-014**: The panel's node update endpoint (`POST /api/node/{id}/update`) MUST NOT be used.
- **FR-015**: Every node step MUST back up both compose files first, handle tag and digest pins in both,
  and pin the new image by digest.
- **FR-016**: Every node step MUST pass this gate before the next node: the container runs the new digest;
  after a reconnect the panel shows exactly `connected` and version `0.6.21`; the node log shows the
  0.6.21 start line and a successful user sync with no error or panic; usage rows keep arriving.
- **FR-017**: Rollback MUST restore the backed-up compose files, which point at digest
  `sha256:6c5420a4f386140334c1f36020b2741cca6eb684e4d9d5c28217497e0685ccfd` (`v0.8.20-degraded-start`,
  0.6.20), and pass the same gate with version `0.6.20`.
- **FR-018**: Rollout scripts MUST take the target tag, digest and expected version as parameters (the
  panel-host `inner20.sh` and `verify20.sh` hardcode 0.8.20/0.6.20 today) and MUST read the admin
  password from a file with mode 0600, never from a process argument (`verify20.sh` and `rc1.sh` put it on
  the curl command line today).
- **FR-019**: `deploy_l2tp_node.sh` MUST NOT be used for node 70 as it is, because it ignores the
  override pin; node 70 uses the same parametrized path as the other nodes.

**Process**

- **FR-020**: Handwritten code MUST carry no explanatory comments; commits MUST be authored as
  Free-Guy-IR with no co-author trailer and no AI tool name; no server address or credential may be
  committed.

### Key Entities

- **Node-level traffic series**: what the panel stores per node per interval (`node_usages`); read with
  reset once per collection cycle.
- **Per-inbound traffic series**: what the panel stores per node per inbound per interval
  (`node_inbound_usages`); read with reset once per cycle by the fork's per-inbound job.
- **Per-user traffic series**: the billing series (`node_user_usages`); out of scope and unchanged.
- **Release image**: `ghcr.io/free-guy-ir/node:v0.8.21-session-kill`, identified by its digest.
- **Rollout wave**: an ordered group of panel node ids from the fleet map, each with a gate and a
  rollback.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On node 56, a limited user's open tunnel stops within 5 seconds on all four tested
  transports, the removed user gets 0 bytes on new connections, and the bystander keeps flowing.
- **SC-002**: One hour after node 63 runs 0.6.21, its per-inbound traffic divided by its node-level
  traffic for that hour is between 0.95 and 1.05 (today about 0.007).
- **SC-003**: Over the same hour, node 63's per-user traffic divided by its node-level traffic stays
  within 0.02 of its pre-release value (about 1.005): billing is unchanged.
- **SC-004**: At the end of the rollout, every one of the 21 connected nodes reports version 0.6.21 and
  is connected, and none of the 6 disabled nodes changed.
- **SC-005**: Every node rolled back during the rollout was connected again on 0.6.20 within 10 minutes.
- **SC-006**: The automated suite, including the new F3 tests, passes on the release commit, and the new
  F3 tests fail on `4af18c2`.

## Assumptions

- The panel keeps reading node-level traffic (`record_usages.py`) and per-inbound traffic
  (`app/fork/jobs/inbound_usage.py`) with reset once per cycle; no panel change is part of this release.
- Node ids, compose directories, override and digest pins, host roles and access paths come from the KG
  entity "Node fleet map 2026-09-23" and are not repeated here.
- The stopwatch test uses a dedicated test user created for it and removed afterwards.
- Hosts not inspected on 2026-09-23 are assumed to match the 2026-09-18 v0.8.20 rollout map; each wave
  re-checks its hosts read-only before it changes anything.
- Node 63 is not affected by the Xray fix; it goes last because it is the sole Hysteria2 endpoint and
  the place where F3 is measured.
