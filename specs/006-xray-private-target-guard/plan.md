# Implementation Plan: proxy users cannot reach private networks through the node's freedom outbounds

**Branch**: `ci-v0826-private-guard` | **Date**: 2026-11-22 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/006-xray-private-target-guard/spec.md`

**Base**: `92a8aa57` (v0.8.25: `19863c87` plus the canary rewrite on `ci-v0825-private-pin`).

## Summary

A new Xray patch, `patches/xray-freedom-private-guard.patch`, applied right after `xray-freedom-local-host.patch`,
gives freedom a private-target guard for the user inbounds the local-host refusal already guards, reads upstream's
`finalRules` shape and a node-owned `privateAllow` list, and checks the address actually connected to: before the
dial for literal and freedom-resolved addresses; at connect time inside the default system dialer on the path every
production freedom takes (no lookup of its own there); off that path after one lookup whose checked answers are the
only addresses dialed; after the dial on the remote address; per UDP datagram; and per UDP-over-TCP datagram. The node derives `privateAllow` for every freedom outbound from the core's own routing rules and
writes it into the config it renders in `ToBytes`, so core 1's `100.64.0.0/10` route to `DIRECT` keeps working.

## Technical Context

**Language/Version**: Go 1.27.4 (node module and the Xray build, `GOTOOLCHAIN=local`); Xray source `d2758a02`
(v26.3.27, module `go 1.26.0`) plus the 28 fork patches.

**Primary Dependencies**: the patched xray-core tree (`proxy/freedom`, `infra/conf`, `app/router` GeoIP matcher,
`transport/internet`), protobuf generated with protoc-gen-go v1.36.11 and protoc 36.2.

**Storage**: N/A.

**Testing**: `go test` in the Xray tree (`proxy/freedom`, `infra/conf`, `app/router`, `transport/internet`,
`app/proxyman/outbound`) through `make test_xray_patches`; node `go test ./backend/xray` (config-level and live against
a real `xray run`); `make test_xray_guards` (Dockerfile guard replay); the `private-target-harness` job
(`scripts/private_target_harness.py`, root on the runner: rebinding DNS, a dummy tunnel, iptables counters, a latency
table against the same core built without the guard); GitHub Actions `run-tests.yml` on the pushed commit.

**Target Platform**: Linux (image for linux/amd64 and linux/arm64); development on darwin/arm64.

**Project Type**: network daemon (node agent driving an Xray core).

**Performance Goals**: no change for public destinations and non-user inbounds: no extra DNS lookup on the default
system dialer path; one lookup per attempt off it, in place of the dialer's own.

**Constraints**: no code comments; the patch must apply in both the Makefile glob order and the Dockerfile order;
every non-test file the patch touches is caught by a Dockerfile grep or by the build when dropped; the pre-push guard
rejects lines with unlisted dotted quads, so IPv4 prefixes in code are built from bytes; the rendered config must stay
loadable by stock v26.3.27.

**Scale/Scope**: production cores 1, 16, 17, 19 and 21; node v0.8.26 (version bump done separately).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- I Committed Code Only: Linux evidence comes from GitHub CI on the pushed commit; local runs use the worktree, and no
  image is built here. PASS
- II Fail Before, Pass After: patch tests RED on the 28 patches and GREEN with the patch; node tests RED on the base
  build and GREEN on the change; mutants for the dial-time check, the connect check, the zone strip, the
  fail-closed lookup, the checked addresses on chains and the allow derivation;
  `-race` for the patch tests and the backend tests. PASS (planned, recorded in tasks.md)
- III No Explanatory Comments: none added; the patch header is the patch description, as in every other patch. PASS
- IV Strict Patch Versioning: `NodeVersion` and tags are not touched here. PASS
- V Safe Rollout: no node, server or panel is touched; the owner's test-link rollout follows. PASS
- VI Identity and Secrecy: commits by Free-Guy-IR without trailers; tests use only private, CGNAT, reserved and
  documentation addresses, plus the one allowlisted public test address. PASS
- VII Fork Seam: new behaviour lives in new files (`proxy/freedom/private_target.go`,
  `infra/conf/freedom_private_target.go`, `backend/xray/private_target_allow.go`); upstream-shaped files get call
  sites only; the Dockerfile greps each call site; a malformed `privateAllow` or `finalRules` fails the config check
  loudly. PASS
- Release Constraints: the patch is asserted by greps after `git apply`; billing paths untouched. PASS

## Design

### Xray patch (`patches/xray-freedom-private-guard.patch`)

- `proxy/freedom/config.proto` and `config.pb.go`: message `PrivateTargetRule` (`block`, `networks`, `port_list`,
  `ip` as `xray.app.router.GeoIP`, `inbound_tags`, `rule_tag`) and `Config` fields `final_rules = 100`,
  `private_allow = 101` (high numbers so a later port of upstream's `final_rules = 8` cannot collide).
- `app/router/private_target_match.go` (new): `(*Router).MatchingRule(routing.Context)` returns the first matching
  rule's `ruleTag` and target tag through `pickRouteInternal`, without the webhook `PickRoute` fires or a balancer
  pick.
- `proxy/freedom/private_target.go` (new): the 18-prefix private set built from bytes, the rule matcher (network,
  port, inbound tag, router GeoIP matcher), `initWithFeatures` (takes the router through `RequireFeatures`),
  `initTargetRules`, `refusesPrivateTarget` (guarded inbound, private address, `finalRules` first match, then a
  `privateAllow` entry whose rule the router's first match for the dialed address confirms on this outbound, else
  refuse), `connectCheck` (judges a connect by the network its caller names, parses the connect address with
  `netip.ParseAddrPort`, strips the zone, unmaps IPv4-mapped addresses, refuses the local host, a refused private
  address, an unknown network or anything unparsable, and records the first refusal of the dial), `resolveOnce` (one system lookup off the system path, every answer checked, a failed or empty lookup
  refused) and `dialFirst` (dials the checked addresses in order).
- `proxy/freedom/freedom.go`: `Handler` gets the two rule lists; `Init` ends with `return h.initTargetRules(config)`;
  `Process` refuses before the dial (literal or freedom-resolved address); when the dialer reports
  `AppliesConnectCheck` it puts `connectCheck` into the dial context and resolves nothing, otherwise a name goes
  through `resolveOnce`; a dial that failed after a connect refusal is refused without retries; after the dial the
  remote address is checked (skipping the unspecified address chained dials report); a refusal is logged at warning
  with the inbound's protocol and tag; `PacketWriter` gets a `RefusesTarget` callback checked after per-packet
  resolution, built when `Process` starts so that it keeps the session context through the TimeoutOnly swap of an
  XUDP flow; a name freedom resolves itself takes its random answer among the dialable ones (`pickAnswer`), for the
  connection and per datagram. The local-host checks and their messages stay first and unchanged, so the local host
  is refused whatever an allow says.
- `transport/internet/connect_check.go` (new): `ConnectCheck`, `ContextWithConnectCheck`, `ConnectCheckFromContext`,
  `SystemDialerAppliesConnectChecks` (the effective system dialer is the default one) and `CheckedPacketConn` (drops
  a datagram whose destination the check refuses). `system_dialer.go`: `DefaultSystemDialer` installs
  `net.Dialer.Control` when a check is in the context and runs the check first there, before any controller or socket
  option, and checks a UDP destination after `net.ResolveUDPAddr`.
- `app/proxyman/outbound/connect_check.go` (new): `(*Handler).AppliesConnectCheck(dest)` is false under
  `proxySettings`, under `dialerProxy`, for TCP over a transport other than raw TCP and with an alternative system
  dialer, and true for the UDP-over-TCP magic names; `uot.go` wraps the UDP-over-TCP socket in `CheckedPacketConn`
  when a check is in the context.
- `infra/conf/freedom.go`: `FreedomConfig` gets `FinalRules []*FreedomFinalRule` (`finalRules`) and
  `PrivateAllow []*FreedomPrivateAllow` (`privateAllow`); `Build` calls `c.buildPrivateTargetRules(config)`.
- `infra/conf/freedom_private_target.go` (new): `finalRules` entries need `action` `allow` or `block`; `privateAllow`
  entries need a `ruleTag` and a non-empty `ip`; `ip` lists go through `ToCidrList`, so they accept what routing
  accepts.
- Tests: `proxy/freedom/private_target_refusal_test.go`, `private_target_rules_test.go`,
  `private_target_rebind_test.go`, `private_target_paths_test.go`, `private_target_handler_test.go`,
  `private_target_connect_test.go`, `infra/conf/freedom_private_target_test.go`,
  `app/router/private_target_match_test.go`, `transport/internet/connect_check_test.go`,
  `app/proxyman/outbound/connect_check_test.go`.

### Node (`backend/xray`)

- `private_target_allow.go` (new): the same 18 prefixes; the ranges the node's sibling guard keeps user inbounds off
  (`apiGuardDestinationCIDRs`); `(*Config).outboundsWithPrivateAllow()` returns a copy of the outbounds in which every
  freedom (`freedom` or `direct`, any case) outbound's settings carry the derived `privateAllow`, every key that folds
  to `privateAllow` removed first, and the field dropped when nothing is derived.
- Derivation: for each routing rule whose `outboundTag` names a freedom outbound, the literal addresses and prefixes
  of `ip` that lie inside one private prefix and outside the node-guarded ranges become one entry `{ruleTag, ip}`;
  the rule keeps its own `ruleTag`, or gets `PG_NODE_PRIVATE_ALLOW_<n>` in the rendered routing. A rule with a
  `balancerTag` only, a non-empty domain condition, a repeated key or a `ruleTag` another rule shares is skipped and
  logged. Outbound keys (`protocol`, `tag`, `settings`) are read in any letter case, as Xray reads them; an outbound
  that repeats one is left without an allow.
- `config.go`: `ToBytes` marshals the snapshot with the tagged routing and the outbounds of
  `privateTargetRendering()`, so
  every config the core runs (start, restart, sync clones, the degraded restart without content-filter rules,
  `restorePreviousConfig`) carries an allow derived from its own rules; `Clone` keeps the panel's outbounds.

### Tests

- Patch level, package `freedom`, real sockets: a listener on a non-loopback private IPv4 of the host and on an IPv6
  link-local address of it; a recording dialer for IPv6 and CGNAT targets the host does not have; a scripted DNS
  server behind the shared system resolver (per-type answer sequences: public then private, a failed first lookup,
  query counts); a stub `dns.Client` through `internet.InitSystemDialer` for freedom-resolved and socket-option
  names; test dialers that report each dial path, a dialerProxy chain that resolves names itself, an alternative
  system dialer, and a real `app/proxyman/outbound` freedom handler for the system path and UDP-over-TCP.
- CI harness: `scripts/private_target_harness.py` in its own job runs a guarded and a base core behind a real VLESS
  client, a scripted DNS server on 127.0.0.1:53 behind `/etc/resolv.conf`, lo aliases, a dummy tunnel `pgtun0` with
  per-port iptables counters, and checks A1 to A5, H2 and UDP-over-TCP (guarded refuses, base exposes), the CGNAT
  allow and a public name, and a latency table at 0 and 20 ms DNS delay and concurrency 1 and 32.
- Node level, package `xray`: config-level checks of the rendered JSON; live tests through `New` (so `ApplyAPI` and
  `ToBytes` run) on a core 1 shape and a core 16 shape, with xray `dns.hosts` names for freedom-resolved outbounds and
  system names for AsIs outbounds. The installed binary (or `XRAY_EXECUTABLE_PATH`) must refuse; a stock binary that
  the probe finds unguarded must show the exposure on the same scenarios, so one CI run proves every scenario can
  fail; a control core routes CGNAT to `DIRECT` through a rule that also matches on source, which the node cannot
  turn into an allow, and the guarded build must refuse it.
- Canary: `freedom_private_target_nodeverify_test.go` learns, by the `privateAllow` probe, that a guarded build
  blocks; the stock paths are unchanged.

### CI

- `run-tests.yml` `test` job: a step before the backend tests adds `100.64.0.1/32`, `192.168.233.1/32` and the
  allowlisted public test address to `lo`, maps two test names in `/etc/hosts`, and exports
  `PG_NODE_PRIVATE_GUARD_REQUIRED=1`, which turns the skips of the address-dependent cases into failures.
  `scripts/workflow_gates.lock` is relocked.
- `Makefile` `test_xray_patches`: runs the new patch tests under `-race`, with the shipped geoip.dat passed when it is
  installed so the built-in set is compared with `geoip:private`.
- `Dockerfile`: copies the patch and asserts every call site after `git apply`.

## Project Structure

### Documentation (this feature)

```text
specs/006-xray-private-target-guard/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   └── freedom-settings.md
├── checklists/
│   └── requirements.md
└── tasks.md
```

### Source Code (repository root)

```text
patches/
└── xray-freedom-private-guard.patch
backend/xray/
├── config.go
├── private_target_allow.go
├── private_target_allow_test.go
├── private_target_live_test.go
└── freedom_private_target_nodeverify_test.go
scripts/
├── private_target_harness.py
└── workflow_gates.lock
.github/workflows/run-tests.yml
Dockerfile
Makefile
```

The patch adds `app/router/private_target_match.go`, `app/proxyman/outbound/connect_check.go`,
`transport/internet/connect_check.go`, `infra/conf/freedom_private_target.go`, `proxy/freedom/private_target.go` and
their tests to the Xray tree, and hooks into `proxy/freedom/freedom.go`, `config.proto`, `config.pb.go`,
`infra/conf/freedom.go`, `transport/internet/system_dialer.go` and `app/proxyman/outbound/uot.go`.

**Structure Decision**: the node repository's existing layout; Xray changes ship as one patch file.

## Complexity Tracking

| Item | Why Needed | Simpler Alternative Rejected Because |
|------|------------|-------------------------------------|
| CI step that adds test addresses and names to the runner | the CGNAT allow, a public target and the AsIs-by-name case need real addresses and system names | skipping those cases would leave the CGNAT route and the AsIs path unproven on Linux |
| Connect check inside `DefaultSystemDialer` (`transport/internet`) | only the dialer sees the address it connects to; a check beside it resolves separately (hostile round 1: rebinding, failed lookups, zoned names, the port oracle, the extra lookup) | a pre-dial lookup in freedom (the first two pushes) |
| One checked lookup off the system path | a chain, a transport or an alternative dialer would resolve the name again out of freedom's sight | refusing those paths outright would break chained freedoms that production does not use but the config allows |
| UDP-over-TCP socket wrapped in `app/proxyman/outbound` | that socket is the handler's own, freedom never sees its datagrams | refusing the magic names would break UDP for clients that use UDP-over-TCP |
| Root-only harness job | a dummy tunnel, iptables counters, port 53 and `/etc/resolv.conf` need root | unit tests alone cannot show the SYNs into a tunnel or the latency of the real binaries |
