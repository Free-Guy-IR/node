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

- Decision: on the address actually connected to. Before the dial: a literal address and a freedom-resolved name. On
  the default system dialer path (no `proxySettings`, no `dialerProxy`, the raw TCP transport for TCP, no alternative
  system dialer; `app/proxyman/outbound`'s `AppliesConnectCheck`): a connect check in the dial context, which
  `DefaultSystemDialer` runs first in `net.Dialer.Control` before every connect attempt on the exact address, and on
  a UDP destination after `net.ResolveUDPAddr`; freedom resolves nothing itself there. Off that path: one system
  lookup, every answer checked, a failed or empty lookup refused, and only the checked addresses dialed. After the
  dial: the remote address, except the unspecified address that `dialerProxy` and `proxySettings` connections report.
  Per UDP datagram after per-packet resolution, and per UDP-over-TCP datagram through `CheckedPacketConn`.
- Rationale: routing cannot see the resolved address of a name matched in the domain pass, so only the dial sees it.
  At the pin an AsIs TCP dial resolves and completes the handshake inside `net.Dialer.DialContext`
  (`transport/internet/system_dialer.go`), so an after-dial check alone lets the SYN reach the private host, through
  the WireGuard tunnel on production's interface-bound outbounds. The first two pushes resolved the name in freedom
  and let the dialer resolve it again; hostile review round 1 on `6597d6e1` showed what that gap allows (rebinding
  names, a failed first lookup that let the dial go on, a zoned link-local name the resolver rejects but the dialer
  parses, SYNs into the tunnel, a port oracle through freedom's retries, a second lookup per connection). Only the
  dialer sees the address it connects to, and `Control` runs before `connect(2)` for every attempt Go makes
  (`TcpRaceDial` and a `sockopt.domainStrategy` retarget included). Off the system path freedom cannot see the
  connect, so it dials what it checked.
- Alternatives: the brief's after-dial-only check (rejected for TCP names, see above); the pre-dial lookup of the first
  two pushes (rejected, see above); resolving once and dialing the checked addresses on every path (it would bypass
  `sockopt.domainStrategy`, Go's dual-stack fallback and the UDP-over-TCP names on the path production takes); a
  routing-level guard rule (cannot see names matched in the domain pass).

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

## R5. Which routing rules give an allow, and who decides

- Decision: a rule whose `outboundTag` is a freedom outbound lists `{ruleTag, ip}` on it, with only the literal
  addresses or prefixes inside one private prefix, minus the node-guarded ranges; at dial time freedom asks the
  router which rule decides for the connection with the dialed address as target (`MatchingRule`), and allows only
  that rule's listed addresses on the outbound the rule sends to.
- Rationale: two automated reviews (MEDIUM) found the first form, which copied a rule's inbound, network and port
  into the entry, broader than routing: it ignored rule order, so a private route shadowed by an earlier block still
  opened its addresses to names reaching the outbound through the domain pass (reproduced live on `7c1e0cab`). The
  router's own first match carries every condition and the order; `MatchingRule` skips the webhook and balancer side
  effects `PickRoute` has.
- Alternatives: carrying the rule identity from the original routing decision (the decision for a name in the domain
  pass is the catch-all inbound rule, so CGNAT by name would break while saying nothing about the address);
  re-implementing rule matching in the node (every condition and shadowing by hand); catch-alls, negations and broad
  prefixes (not private intent); balancer targets (skipped and logged).

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
- A chained freedom outbound (`dialerProxy`, `proxySettings`), another TCP transport or an alternative system dialer
  gets a checked address instead of the name, so the far end no longer resolves it itself; none exists in
  production. UDP-over-TCP under `proxySettings` is refused, since the magic name does not resolve.
- On the system path a name with a public and a private answer may still connect to the public one; the private one
  never gets a SYN.
- An allowing rule that also matches on a domain never applies, because the router is asked with the address as
  target; the node skips such rules and logs them.
- Rules added at run time through the RoutingService are not listed until the next render.
- Datagrams that private hosts send back on a full-cone UDP flow are not filtered (upstream filters them); a guarded
  user cannot open such a flow to a private host in the first place.
- `finalRules` decide private destinations only and ignore upstream's `delay` and `logLevel` fields (hostile round 2,
  R2-L4, info).
- Address classes the guard does not refuse (R2-L5, info): the node's own public addresses, IPv4-compatible
  `::a.b.c.d`, NAT64 `64:ff9b::/96`, 6to4 and Teredo, the deprecated site-local `fec0::/10`; and UDP replies that come
  from private sources on a full-cone flow (above). Round 2 found no build that reaches a private host through the
  IPv4-compatible or NAT64 forms on a runner without a translator.
- One warning line per refused connection (R2-L6, info): a user who probes private ranges fills the core log at that
  rate; the line names the inbound's protocol and tag, never the user.
- Go's dual-stack fallback can run the connect check from two goroutines at once (R2-L7, found by reading): both read
  the session outbounds and copy the last one, nothing writes them during the dial, and the `-race` runs of the
  dual-stack and rebinding tests report no race.
- Off the system path the checked addresses are dialed one after another (accepted, round 1 low): only an alternative
  system dialer or another transport waits there, for that path's own dial timeout per address, and in the resolver's
  RFC 6724 order, which puts the address most likely to work first; no production freedom outbound takes that path,
  and dialerProxy and proxySettings chains return at once.
