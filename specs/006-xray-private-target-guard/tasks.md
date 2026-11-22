---

description: "Task list for the freedom private-target guard (node v0.8.26)"
---

# Tasks: proxy users cannot reach private networks through the node's freedom outbounds

**Input**: Design documents from `specs/006-xray-private-target-guard/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/freedom-settings.md, quickstart.md

**Tests**: required (constitution II and the brief): every behaviour test runs RED on the base before the fix and
GREEN after it, and every mutant listed in Phase 7 is killed. `S` below is the session scratchpad; `XS` is the Xray
source at `S/pguard/xray` (`d2758a02` plus the 28 patches, commit tag `patched28`).

**Organization**: tasks are grouped by user story (spec.md US1 to US4).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: the user story the task belongs to

## Phase 1: Setup

- [X] T001 Fetch Xray `d2758a02` into `XS`, apply every `patches/xray-*.patch` in glob order and commit the result as tag `patched28` in `S/pguard/xray`
- [X] T002 Build the RED binary from `patched28` (`go build ./main` in `XS`, `GOTOOLCHAIN=local`) to `S/pguard/bin/xray-patched28`, and keep the read-only stock binaries in `S/pinfix/mac/bin` for the canary

## Phase 2: Foundational (blocks every story)

- [X] T003 Add message `PrivateTargetRule` (`block`, `networks`, `port_list`, `ip` as `xray.app.router.GeoIP`, `inbound_tags`) and `Config` fields `final_rules = 100`, `private_allow = 101` in `XS/proxy/freedom/config.proto`, and regenerate `XS/proxy/freedom/config.pb.go` with protoc-gen-go v1.36.11
- [X] T004 Add `FreedomFinalRule` (`action`: "`allow` or `block`, case-insensitive; anything else fails the config check"; `network`; `port`; `ip`) and `FreedomPrivateAllow` (`ip`: "required, non-empty"; `inboundTag`; `network`; `port`) with `buildPrivateTargetRules` in the new `XS/infra/conf/freedom_private_target.go`, the two fields on `FreedomConfig` and the call before `return config, nil` in `XS/infra/conf/freedom.go`
- [X] T005 Add the 18-prefix private set (IPv4 built with `netip.AddrFrom4`), the rule matcher (network, port, inbound tag, router GeoIP matcher), `initTargetRules`, `refusesPrivateTarget` and `refusedResolvedTarget` in the new `XS/proxy/freedom/private_target.go`; add the two rule lists to `Handler` and end `Init` with `return h.initTargetRules(config)` in `XS/proxy/freedom/freedom.go`

## Phase 3: User Story 1 - a proxy user cannot reach private networks through freedom (P1)

**Goal**: guarded inbounds never connect or send to a private address that no allow covers.

**Independent Test**: a VLESS user of a core 1 shape and a core 16 shape asks for a private listener literally and by
name; nothing reaches it.

### Tests for User Story 1 (RED first)

- [X] T006 [P] [US1] In `XS/proxy/freedom/private_target_refusal_test.go` (existing API only, so it compiles and runs RED on `patched28`): a user inbound to a TCP listener on a non-loopback private IPv4 of the host is refused, the error names the inbound and `private address`, and the listener accepts nothing; an IPv6 unique-local and link-local literal is refused before the dialer is called (recording dialer)
- [X] T007 [P] [US1] In the same file: a name resolved by freedom (`domainStrategy` `UseIPv4`, stub `dns.Client` through `internet.InitSystemDialer`) to the private listener is refused before the dial; an AsIs TCP name answered by an in-process DNS server behind the shared system resolver is refused before the handshake (listener accepts nothing); an AsIs UDP name answered the same way is refused after the dial and before any datagram (the after-dial check); a UDP flow whose datagrams address a private listener per packet delivers nothing
- [X] T008 [P] [US1] In `XS/proxy/freedom/private_target_rules_test.go` (new API, runs with the patch only, RED through the Phase 7 mutants): every private prefix boundary and a public address are classified as the data model says, and the built-in set equals `geoip:private` of the geoip.dat named by `XRAY_FREEDOM_GEOIP_DAT` (skipped when unset, failed when set and unreadable)
- [X] T009 [P] [US1] In `backend/xray/private_target_live_test.go`: through `New` and a real `xray run` of each binary under test (the installed slot, or `XRAY_EXECUTABLE_PATH` when set, must refuse; the stock slot must refuse when the probe of T023 finds the guard and must show the exposure when it does not), on a core 1 shape (IPIfNonMatch, port 25 block, CGNAT to `DIRECT`, `geoip:private` to `BLOCK`, inbound-specific rules to `DIRECT` and to an interface-bound AsIs freedom) and a core 16 shape (AsIs, every `india` request to an interface-bound freedom): a name that xray's `dns.hosts` resolves to a private listener is refused through `DIRECT` in the domain pass; a system name (CI `/etc/hosts`) is refused through the AsIs freedom with no handshake; a literal private target of the core 16 shape is refused
- [X] T010 [US1] Run T006 to T009 against `patched28` and record the RED logs in `S/pguard/evidence/`

### Implementation for User Story 1

- [X] T011 [US1] In `XS/proxy/freedom/freedom.go` `Process`: refuse after the local-host check before the dial (`h.refusesPrivateTarget(inbound, dialDest)`), then for a TCP name call `h.refusedResolvedTarget`, check the remote address after the dial except the unspecified address, end with `it is the private address` in the refusal; set `pw.RefusesTarget` and check it in `PacketWriter.WriteMultiBuffer` after the local-host drop
- [X] T012 [US1] Run T006 to T009 GREEN with `-race` and record the logs

## Phase 4: User Story 2 - private routes the core configures on purpose keep working (P1)

**Goal**: the routing intent survives: core 1's CGNAT route through `DIRECT` carries data and leaks nowhere else.

**Independent Test**: on the core 1 shape, CGNAT through `DIRECT` carries data; other ranges, other outbounds and
other inbounds stay refused.

### Tests for User Story 2 (RED first)

- [X] T013 [P] [US2] In `XS/proxy/freedom/private_target_rules_test.go`: an allow (`finalRules` or `privateAllow`) covering the local host still refuses it; `finalRules` `[block H, allow H]` refuses, `[allow H]` carries data to the private listener; a `privateAllow` for the listener carries data, one for `100.64.0.0/10` carries data to a CGNAT target (recording dialer) and still refuses the private listener, one scoped to inbound tag `A` refuses tag `B`, network and port conditions hold
- [X] T014 [P] [US2] In `XS/infra/conf/freedom_private_target_test.go`: `finalRules` actions parse case-insensitively and an unknown action fails; a `privateAllow` entry without `ip` fails; `inboundTag`, `network`, `port` and `ip` reach the proto
- [X] T015 [P] [US2] In `backend/xray/private_target_allow_test.go` (config level, `NewConfig` + `ApplyAPI` + `ToBytes`): the core 1 shape renders `privateAllow` `[{"ip": ["100.64.0.0/10"]}]` on `DIRECT` only; an inbound-scoped rule renders a scoped entry; rules to `BLOCK`, to a non-freedom outbound or to a balancer, catch-alls, negations, geo lists, partial overlaps, node-guarded ranges and rules with other conditions render none; a panel value in any letter case is replaced; `Clone` keeps the panel's outbounds while `ToBytes` of a clone and of a config after `stripContentFilterRules` carries the allow derived from its own rules; a stock v26.3.27 `xray run -test` accepts the rendered config
- [X] T016 [P] [US2] In `backend/xray/private_target_live_test.go`: on the core 1 shape a CGNAT literal reaches the CI CGNAT listener through rule#2 and `DIRECT`; a `dns.hosts` name for it through an inbound routed to `DIRECT` carries data; the same name through the interface-bound freedom is refused; a control core whose CGNAT rule also matches on source (no derivable allow) is refused on a guarded build
- [X] T017 [US2] Run T013 to T016 against the base (patched28 and the node at `92a8aa57`) and record RED

### Implementation for User Story 2

- [X] T018 [US2] Implement the derivation in the new `backend/xray/private_target_allow.go` and marshal `ToBytes` with `outboundsWithPrivateAllow()` in `backend/xray/config.go`
- [X] T019 [US2] Run T013 to T016 GREEN with `-race` and record the logs

## Phase 5: User Story 3 - everything else keeps working (P2)

**Goal**: public destinations of guarded inbounds and every destination of other inbounds are unchanged.

- [X] T020 [P] [US3] In `XS/proxy/freedom/private_target_refusal_test.go`: `socks`, `http`, `dokodemo-door` and a connection without an inbound reach the private listener; in `XS/proxy/freedom/private_target_rules_test.go`: `finalRules` never refuse a public destination
- [X] T021 [P] [US3] In `backend/xray/private_target_live_test.go`: a public target (the CI public test address on `lo`) carries data for guarded inbounds on both core shapes; a dokodemo-door tunnel and a SOCKS inbound reach the private listener
- [X] T022 [US3] Run the whole `./backend/xray/` package with `-race` and `TEST_INTEGRATION=true` against the patched build and confirm every existing live test still passes

## Phase 6: User Story 4 - the canary records what the shipped core does (P3)

- [X] T023 [US4] Add the guard probe (`xray run -test` on a freedom `privateAllow` entry without `ip`, plus the same config without it as the control) in `backend/xray/private_target_live_test.go`, and fail when the installed binary exists and is not guarded
- [X] T024 [US4] Make `privateTargetPinOf` in `backend/xray/freedom_private_target_nodeverify_test.go` expect the block on a guarded build before v26.4.15, keep every stock-path assertion, and describe the guarded pin in `String()`
- [X] T025 [US4] Run the canary against the guarded build and against stock v26.3.27, v26.4.13, v26.5.3 and v26.11.9 (`S/pinfix/mac/bin`), and record which branch each takes

## Phase 7: Polish and cross-cutting

- [X] T026 Write `patches/xray-freedom-private-guard.patch` from `git -C XS diff patched28` with the fork deviation header, and check that it applies after the 28 patches in both the Makefile glob order and the Dockerfile order
- [X] T027 Copy and apply the patch in the `Dockerfile` right after `xray-freedom-local-host.patch` with greps for every call site, and add the new patch tests to `test_xray_patches` in the `Makefile`
- [X] T028 Run `make test_xray_guards` and confirm every dropped non-test file is caught
- [X] T029 Add the CI address step to `.github/workflows/run-tests.yml`, relock `scripts/workflow_gates.lock` with `workflowgate fingerprint`, and run `scripts/workflow_policy.sh` checks that can run locally
- [X] T030 Mutants, each RED: freedom without the pre-dial private check (the name case through `DIRECT`), without the pre-dial lookup (the AsIs listener sees a handshake), without the after-dial check, and the node without the derivation (CGNAT through `DIRECT`); record the logs
- [X] T031 Check the diff for code comments, author, trailers and the pre-push guard rules; run `go vet` on the touched Xray packages and on `./backend/xray/`
- [ ] T032 Commit as Free-Guy-IR, push `ci-v0826-private-guard` with `ALLOW_UNREVIEWED_PUSH=1`, and verify every GitHub Actions run of the exact commit with `python3 -I S/pgupd/ci_status.py Free-Guy-IR/node@<sha>`
- [ ] T033 Write the fleet-impact statement and log every step to the knowledge graph
- [ ] T034 Hand the diff to the main session for the independent hostile review the constitution's Development Workflow requires (the owner waived the external model gate for PasarGuard on 2026-11-04; the author does not grade its own fix)

## Phase 8: Review round (two automated security reviews, MEDIUM, 2026-11-22)

**Findings**: "authorization scope mismatch" and "allowlist broader than routing semantics" on
`backend/xray/private_target_allow.go`: an allow derived from the rules alone ignores rule order and match semantics.

- [X] T035 RED against `7c1e0cab` (the first push), real xray: a private route that an earlier `geoip:private` block shadows opens its address to a `direct-in` name through `DIRECT` (about 1.5 GB arrives); the reviewers' inbound example (an `egress-in`-only route reached by `direct-in` by name) does not leak there, because the first form scoped entries by inbound tag (`S/pguard/evidence/live-v3-red-on-7c1e0cab-mac.log`)
- [X] T036 Add `(*Router).MatchingRule` in `XS/app/router/private_target_match.go` (first match through `pickRouteInternal`, no webhook, no balancer pick) and its test `XS/app/router/private_target_match_test.go` (PickRoute fires the webhook, MatchingRule does not)
- [X] T037 In `XS/proxy/freedom/private_target.go`: take the router through `RequireFeatures`, and allow a `privateAllow` entry only when the router's first match for the connection with the dialed address as target is the entry's `ruleTag` on this outbound; `XS/proxy/freedom/config.proto` gets `rule_tag = 6`; `XS/infra/conf/freedom_private_target.go` reads `{ruleTag, ip}`
- [X] T038 In `backend/xray/private_target_allow.go`: tag each allowing rule (its own `ruleTag`, or `PG_NODE_PRIVATE_ALLOW_<n>` in the rendered routing), list `{ruleTag, ip}`, skip balancer, domain-conditioned, repeated-key and shared-tag rules
- [X] T039 Tests: `TestPrivateAllowFollowsTheRoutersFirstMatchForTheResolvedAddress` (shadowing, inbound restriction, another outbound, a mixed rule, CGNAT for two inbounds); live core 1 shadow, inbound-restricted and shared-tag cases; CGNAT literal from every inbound
- [X] T040 Mutants: allowing by an entry's address without asking the router (patch test and both live cases RED), no outbound check (RED), the earlier freedom and node mutants on the new code
- [ ] T041 Push the second commit to `ci-v0826-private-guard` and verify every run of the exact commit

## Dependencies & Execution Order

- Phase 1 then Phase 2; Phase 2 blocks every story.
- US1 and US2 tests can be written in parallel after Phase 2; US2's node derivation (T018) is independent of US1's
  freedom changes (T011), but the live CGNAT case (T016) needs both.
- US3 needs US1 and US2 implemented; US4 needs the patched build (T011) and the probe (T023).
- Phase 7 T026 to T029 follow the implementation; T030 needs everything GREEN; T032 is last before T033.

## Parallel Example

```text
After Phase 2: T006, T007, T008 (Xray test file sections), T009 (node live file), T014 (infra/conf test) and T015
(node config-level test) are written in parallel; T011 and T018 touch different trees.
```

## Implementation Strategy

1. MVP: Phase 2 plus US1 closes the exposure for every private range.
2. US2 restores the deliberate CGNAT route; nothing ships without it, because core 1, 17, 19 and 21 rely on it.
3. US3 and US4 prove nothing else moved and teach the canary the new build.
4. Phase 7 makes the build, the guard replay and CI enforce it.
