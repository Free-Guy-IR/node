# Implementation Plan: proxy users cannot reach the xray node's own control ports

**Branch**: `release/v0.6.21-acct-apiguard` | **Date**: 2026-10-11 | **Spec**: [spec.md](./spec.md)

## Summary

A new file `backend/xray/api_guard.go` builds two routing rules for the instance's api and metric ports,
one on destination addresses (loopback, unspecified, link-local and the host's interface addresses) and one
on domain destinations (`localhost`, `*.localhost`, numeric host forms), both sent to a blackhole outbound
the node adds. `ApplyAPI` strips earlier copies of the rules and the outbound and writes them again after
the API rule and the malformed-domain guard, ahead of every panel rule. The rules live in the config the
node keeps, so every clone, `Restart` and `restorePreviousConfig` carries them.

## Alignment

Agreed with NODE-MISC-SB (sing-box sibling F-SB-V2RAYAPI-LOOPBACK, `specs/004-singbox-loopback-guard`): own
control ports only, written by the node ahead of every panel rule, idempotent, refused. xray-core v26.3.27 has
no per-rule resolve (only the global `routing.domainStrategy`), so the name rule covers `localhost`,
`*.localhost` and numeric host forms, and an arbitrary name pointing at loopback stays a residual under
`AsIs`. xray also lists the host's interface addresses, as its API rule already trusts them as sources.

## Design choices

- Unix-socket API: rejected. The metrics listener of this xray pin is TCP only, so a routing guard is needed
  anyway, and stock release binaries installed by `make install_xray` lack the dokodemo unix-socket fix.
- Freedom-level block: not available in this pin.
- Port-only rule: rejected; it would also refuse remote hosts on the same port numbers.

## Constitution Check

- I Committed Code Only: Linux runs use `git archive` of a commit. PASS
- II Fail Before, Pass After: config-level and live RED on `4668d2f`; GREEN on the fix. PASS
- III No Explanatory Comments: none added. PASS
- VI Identity and Secrecy: Free-Guy-IR, no trailers; only loopback and documentation addresses in tests. PASS
- VII Fork Seam: new behaviour in a new file plus calls in `ApplyAPI` and `filterRules`. PASS
