# Feature Specification: proxy users cannot reach the xray node's own control ports

**Feature Branch**: `release/v0.6.21-acct-apiguard` (feature directory `specs/005-xray-loopback-guard`)

**Created**: 2026-10-11

**Status**: Draft

**Input**: Finding F-XRAY-API-LOOPBACK (high, billing). `Config.ApplyAPI` opens the xray gRPC API as a
dokodemo-door inbound `API_INBOUND` on `127.0.0.1:<api port>`, routes it with a rule that trusts any loopback
or local-interface source, and opens xray's metrics HTTP listener (expvar and pprof) on
`127.0.0.1:<metric port>`. The node adds no destination rule of its own. A proxy user whose traffic the core
sends to a freedom outbound can ask for the API port on a loopback destination; freedom dials it from the
node itself, the source rule matches, and the user reaches `HandlerService` and `StatsService`, including
`QueryStats(reset=true)`, which zeroes every user's traffic counters before the node collects them. Today
the node relies on the panel's core config carrying a private-address block.

**Base**: `4668d2f` on `release/v0.6.21` (node int).

**Billing paths changed by this spec (constitution, Release Constraints)**: none directly; the change
protects the per-user stats counters the billing path reads.

## Clarifications

### Session 2026-10-11

- Q: Which destinations does the node refuse? → A: Connections from any inbound other than `API_INBOUND`
  whose destination port is one of this instance's control ports (the api port and the metric port) and
  whose destination is a loopback, unspecified or link-local address, or one of the host's own interface
  addresses, or a domain destination that names `localhost`, ends in `.localhost`, or is a numeric host
  form that the system resolver reads as an address. Aligned with NODE-MISC-SB's sing-box guard
  (F-SB-V2RAYAPI-LOOPBACK): own control ports only, written by the node ahead of every panel rule,
  idempotent, refused.
- Q: Why not every name that resolves to loopback, as sing-box does? → A: xray-core v26.3.27 has no
  per-rule resolve; name resolution in routing is the single global `routing.domainStrategy`. Changing it
  would change routing for every user. Under `AsIs` an arbitrary name whose public record points at
  loopback, sent to the exact api or metric port, stays a residual; the ports are picked per start and
  are not exposed. Under `IPIfNonMatch` or `IPOnDemand`, which the panel may set, the address rule also
  catches such names.
- Q: What does the guard send the traffic to? → A: A node-owned blackhole outbound that the node adds to
  the config, so the guard does not depend on the panel carrying one.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A proxy user cannot reach the xray API or metrics (Priority: P1)

**Independent Test**: a real VLESS user of a core config with no private-address rule tunnels to the api
port as `127.0.0.1`, `localhost` and numeric host forms and calls `QueryStats(reset=true)`; every call
fails, and the user's traffic counter, read by the node's own loopback client, still holds its bytes.

**Acceptance Scenarios**:

1. **Given** a panel core config with no route rules, **When** a proxy user connects to the api or metric
   port on any covered form, **Then** the core blackholes the connection.
2. **Given** panel route rules that send everything to a freedom outbound, **When** the same connection is
   made, **Then** the node's rules still decide first.
3. **Given** the node's own gRPC client and metrics reader, **When** they connect on loopback, **Then**
   they keep working.
4. **Given** any other destination, including other loopback ports and remote hosts on the same port
   numbers, **When** a user connects, **Then** routing is what the panel configured.

## Requirements *(mandatory)*

- **FR-001**: After `ApplyAPI`, the routing rules MUST hold the API rule first and the node's guard rules
  ahead of every panel rule, with no duplicates when `ApplyAPI` runs again.
- **FR-002**: The guard MUST be carried by every config the core runs, including the clones that user syncs
  restart with, the config `Restart` replays and the previous config `restorePreviousConfig` restores.
- **FR-003**: The guard MUST target a blackhole outbound the node adds, kept exactly once.
- **FR-004**: The guard MUST NOT change routing of `API_INBOUND`, nor of any destination outside the covered
  forms on the control ports.

## Success Criteria *(mandatory)*

- **SC-001**: The config-level and live tests fail on `4668d2f` and pass on the fix.
- **SC-002**: Under concurrent `SyncUser`, `UpdateUsers`, `SyncUsers`, `Restart`, a failed restart that
  restores the previous config, and stats polls, no attacker attempt reaches the API and every config the
  core runs carries the guard.
