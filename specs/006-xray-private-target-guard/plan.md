# Implementation Plan: proxy users cannot reach private networks through the node's freedom outbounds

**Branch**: `ci-v0826-private-guard` | **Date**: 2026-11-22 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/006-xray-private-target-guard/spec.md`

**Base**: `92a8aa57` (v0.8.25: `19863c87` plus the canary rewrite on `ci-v0825-private-pin`).

## Summary

A new Xray patch, `patches/xray-freedom-private-guard.patch`, applied right after `xray-freedom-local-host.patch`,
gives freedom a private-target guard for the user inbounds the local-host refusal already guards, reads upstream's
`finalRules` shape and a node-owned `privateAllow` list, and checks the address actually dialed: before the dial for
literal and resolved addresses (TCP names under AsIs are resolved first), after the dial on the remote address, and
per UDP datagram. The node derives `privateAllow` for every freedom outbound from the core's own routing rules and
writes it into the config it renders in `ToBytes`, so core 1's `100.64.0.0/10` route to `DIRECT` keeps working.

## Technical Context

**Language/Version**: Go 1.27.4 (node module and the Xray build, `GOTOOLCHAIN=local`); Xray source `d2758a02`
(v26.3.27, module `go 1.26.0`) plus the 28 fork patches.

**Primary Dependencies**: the patched xray-core tree (`proxy/freedom`, `infra/conf`, `app/router` GeoIP matcher,
`transport/internet`), protobuf generated with protoc-gen-go v1.36.11 and protoc 36.2.

**Storage**: N/A.

**Testing**: `go test` in the Xray tree (`proxy/freedom`, `infra/conf`) through `make test_xray_patches`; node
`go test ./backend/xray` (config-level and live against a real `xray run`); `make test_xray_guards` (Dockerfile guard
replay); GitHub Actions `run-tests.yml` on the pushed commit.

**Target Platform**: Linux (image for linux/amd64 and linux/arm64); development on darwin/arm64.

**Project Type**: network daemon (node agent driving an Xray core).

**Performance Goals**: no change for public destinations and non-user inbounds; one extra system DNS lookup per new
TCP connection that a guarded inbound sends by name through an AsIs freedom outbound.

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
  build and GREEN on the change; mutants for the dial-time check, the pre-dial resolution and the allow derivation;
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
  `ip` as `xray.app.router.GeoIP`, `inbound_tags`) and `Config` fields `final_rules = 100`, `private_allow = 101`
  (high numbers so a later port of upstream's `final_rules = 8` cannot collide).
- `proxy/freedom/private_target.go` (new): the 18-prefix private set built from bytes, the rule matcher (network,
  port, inbound tag, router GeoIP matcher), `initTargetRules`, `refusesPrivateTarget` (guarded inbound, private
  address, `finalRules` first match, then `privateAllow`, else refuse) and `refusedResolvedTarget` (system resolver
  lookup before an AsIs TCP dial; any local-host or refused private answer refuses).
- `proxy/freedom/freedom.go`: `Handler` gets the two rule lists; `Init` ends with `return h.initTargetRules(config)`;
  `Process` refuses before the dial (literal or freedom-resolved address, then the pre-dial lookup for TCP names) and
  after the dial (remote address, skipping the unspecified address chained dials report); `PacketWriter` gets a
  `RefusesTarget` callback checked after per-packet resolution. The local-host checks and their messages stay first
  and unchanged, so the local host is refused whatever an allow says.
- `infra/conf/freedom.go`: `FreedomConfig` gets `FinalRules []*FreedomFinalRule` (`finalRules`) and
  `PrivateAllow []*FreedomPrivateAllow` (`privateAllow`); `Build` calls `c.buildPrivateTargetRules(config)`.
- `infra/conf/freedom_private_target.go` (new): `finalRules` entries need `action` `allow` or `block`; `privateAllow`
  entries need a non-empty `ip`; `ip` lists go through `ToCidrList`, so they accept what routing accepts.
- Tests: `proxy/freedom/private_target_refusal_test.go`, `infra/conf/freedom_private_target_test.go`.

### Node (`backend/xray`)

- `private_target_allow.go` (new): the same 18 prefixes; the ranges the node's sibling guard keeps user inbounds off
  (`apiGuardDestinationCIDRs`); `(*Config).outboundsWithPrivateAllow()` returns a copy of the outbounds in which every
  freedom (`freedom` or `direct`, any case) outbound's settings carry the derived `privateAllow`, every key that folds
  to `privateAllow` removed first, and the field dropped when nothing is derived.
- Derivation: for each routing rule, in order, whose `outboundTag` names a freedom outbound and whose keys (compared
  case-insensitively) are only `type`, `ruleTag`, `outboundTag`, `domainMatcher`, `webhook`, `ip`, `inboundTag`,
  `network` and `port`, each literal address or prefix of `ip` that lies inside one private prefix and outside the
  node-guarded ranges becomes part of one entry `{ip, inboundTag, network, port}` with the rule's own values. A rule
  that would give an allow but also carries another condition, a `balancerTag` only, or duplicate keys is skipped and
  logged.
- `config.go`: `ToBytes` marshals the snapshot with `OutboundConfigs` replaced by `outboundsWithPrivateAllow()`, so
  every config the core runs (start, restart, sync clones, the degraded restart without content-filter rules,
  `restorePreviousConfig`) carries an allow derived from its own rules; `Clone` keeps the panel's outbounds.

### Tests

- Patch level, package `freedom`, real sockets: a listener on a non-loopback private IPv4 of the host; a recording
  dialer for IPv6 and CGNAT targets the host does not have; an in-process DNS server behind the shared system resolver
  for AsIs names; a stub `dns.Client` through `internet.InitSystemDialer` for freedom-resolved names.
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
.github/workflows/run-tests.yml
scripts/workflow_gates.lock
Dockerfile
Makefile
```

**Structure Decision**: the node repository's existing layout; Xray changes ship as one patch file.

## Complexity Tracking

| Item | Why Needed | Simpler Alternative Rejected Because |
|------|------------|-------------------------------------|
| CI step that adds test addresses and names to the runner | the CGNAT allow, a public target and the AsIs-by-name case need real addresses and system names | skipping those cases would leave the CGNAT route and the AsIs path unproven on Linux |
| Pre-dial system lookup for AsIs TCP names | without it the SYN reaches the private host, through the WireGuard tunnel on interface-bound outbounds | the brief's after-dial-only check contradicts its own tunnel requirement |
