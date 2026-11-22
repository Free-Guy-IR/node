# Research: proxy users cannot reach private networks through the node's freedom outbounds

## R1. The private set

- Decision: the 18 prefixes of `geoip:private` in the shipped geoip.dat (tag 202610202206, sha256 `4a1b1edd...`),
  read with a stdlib protobuf parser on 2026-11-22, fixed in the patch and in the node.
- Rationale: the brief names that set; production cores already block it in routing (rule#3), so operators reason in
  it; a fixed list cannot fail to load at start.
- Alternatives: upstream v26.11.9's `GetPrivateIPMatcher` (wider: NAT64, 6to4, Teredo, ORCHID and others; blocking
  NAT64 would break every IPv4-only name on an IPv6-only node with DNS64); the node's own `privateCIDRs` (15 prefixes,
  the upstream PasarGuard normalisation of `geoip:private` in routing, missing the documentation ranges, 6to4 relay
  anycast and `ff00::/8`); loading geoip.dat at start (a missing asset would stop freedom from starting).

## R2. Where and when to check

- Decision: in freedom, on the address actually dialed. Before the dial: a literal address, a freedom-resolved name,
  and a TCP name under AsIs after one system lookup (every answer is checked). After the dial: the remote address,
  except the unspecified address that `dialerProxy` and `proxySettings` connections report. Per UDP datagram after
  per-packet resolution.
- Rationale: routing cannot see the resolved address of a name matched in the domain pass, so only the dial sees it.
  At the pin an AsIs TCP dial resolves and completes the handshake inside `net.Dialer.DialContext`
  (`transport/internet/system_dialer.go`), so an after-dial check alone lets the SYN reach the private host, through
  the WireGuard tunnel on production's interface-bound outbounds. UDP needs no pre-dial lookup: the UDP dial resolves
  with `net.ResolveUDPAddr` and sends nothing, and the after-dial check sees the resolved address first.
- Alternatives: the brief's after-dial-only check (rejected for TCP names, see above); dialing the pre-resolved address
  directly (removes the second lookup but changes the dialer's address order and fallback for dual-stack names);
  a routing-level guard rule (cannot see names matched in the domain pass).

## R3. What may still be reached

- Decision: the local host never; any other private address only through the outbound's `finalRules` (first match,
  `allow` or `block`, on network, port and address, upstream's JSON shape) or its `privateAllow` entries. `finalRules`
  decide private addresses of guarded inbounds only.
- Rationale: upstream's shape keeps configs portable and keeps the live test harness, which already writes
  `finalRules` allows for its private traffic host, working; deciding private addresses only keeps every public
  destination exactly as v26.3.27 handles it today.
- Alternatives: allow-only `finalRules` (diverges from upstream on `[block H, allow H]`); full upstream semantics for
  every inbound and every destination (new behaviour for public destinations and non-user inbounds).

## R4. Where the node derives `privateAllow`

- Decision: in `ToBytes`, on a copy of the outbounds, from the rules of the config being rendered.
- Rationale: the degraded start strips content-filter rules after `ApplyAPI`; deriving at render keeps every config the
  core runs consistent with its own rules, and `Clone` keeps the panel's outbounds untouched.
- Alternatives: in `ApplyAPI` (would keep allows from rules the degraded start removed).

## R5. Which routing rules give an allow

- Decision: a rule whose `outboundTag` is a freedom outbound and whose keys are only `type`, `ruleTag`, `outboundTag`,
  `domainMatcher`, `webhook`, `ip`, `inboundTag`, `network` and `port`; only literal addresses or prefixes inside one
  private prefix count, minus the node-guarded ranges; the entry keeps the rule's `inboundTag`, `network` and `port`.
- Rationale: freedom can evaluate exactly those conditions, so the allow is never wider than the rule; catch-alls and
  negations are not private intent; the node's sibling guard already keeps user inbounds off the local host and
  link-local by address, so an allow there would contradict it.
- Alternatives: intersecting broad prefixes with the private set (a catch-all would reopen everything); ignoring the
  other conditions (a rule for one user or source would open the range for everyone); balancer targets (no production
  rule uses one; skipped and logged).

## R6. How the canary tells a guarded build from stock

- Decision: probe the binary. `xray run -test` on a config whose freedom outbound has `"privateAllow": [{}]` exits
  non-zero only on a build that reads `privateAllow`; the same config without it must pass on every build.
- Rationale: the patched build still reports 26.3.27; a probe of the patch's own loader cannot drift from the build,
  and no version, path or message constant is compared.
- Alternatives: a version suffix (changes what `xray version` reports to the node and the panel); the binary path
  (the stock and installed names are environment-dependent).

## R7. Literals in code and the pre-push guard

- Decision: IPv4 prefixes in code are built with `netip.AddrFrom4`; test addresses are private, CGNAT, documentation
  or the allowlisted `1.2.3.4`.
- Rationale: the guard rejects a line with a dotted quad outside its allowlist, and several private prefixes are not on
  it.

## R8. Residuals

- Translation and tunnelling prefixes outside `geoip:private` (NAT64 `64:ff9b::/96`, 6to4, Teredo) are not refused;
  reaching a private IPv4 through them needs a translator on the path, which no fleet host is known to have
  (unproven).
- A DNS answer that changes between the pre-dial lookup and the dial reaches the after-dial check: closed after the
  handshake, before any payload.
- A chained freedom outbound (`dialerProxy`, `proxySettings`) is checked on its literal or locally resolved address
  only; none exists in production.
- A rule shadowed by an earlier unconditional private block still gives an allow; production's order (CGNAT before
  the private block) is not affected.
- Datagrams that private hosts send back on a full-cone UDP flow are not filtered (upstream filters them); a guarded
  user cannot open such a flow to a private host in the first place.
