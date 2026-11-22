# Feature Specification: proxy users cannot reach private networks through the node's freedom outbounds

**Feature Branch**: `ci-v0826-private-guard` (feature directory `specs/006-xray-private-target-guard`)

**Created**: 2026-11-22

**Status**: Draft

**Input**: Finding F-XRAY-FREEDOM-PRIVATE (high, automated security review of 2026-11-22, "Security control
regression (SSRF to private networks)"). The node holds Xray at v26.3.27 (`d2758a02` plus the fork patches) because
stock v26.11.9 refuses the plaintext VLESS outbounds of production cores 1, 16 and 17. Upstream added a default
private block for user-inbound connections in two steps: v26.4.15 (`310b764811aa`) added `ipsBlocked` with a default
that closes the stream after the VLESS header, and v26.5.3 (`4e87f59628ff`) replaced it with `finalRules` plus a
default rule that blackholes the stream unless a `finalRules` allow matches. v26.3.27 has neither and ignores
`finalRules` without an error (`xray run -test` exits 0 even for an invalid action), and the fork's freedom patch
refuses only the local host. A proxy user can therefore reach private, CGNAT,
link-local and unique-local destinations through freedom, including cloud metadata, unless the core's own routing
blocks them. Production cores 1, 17, 19 and 21 block `geoip:private` in routing, but only for IP destinations: a
domain request from an inbound that has its own routing rule matches that rule in the domain pass, before any name is
resolved, so a public name that resolves to a private address reaches that inbound's freedom outbound. Core 16 has no
private block at all. Production runs node v0.8.22 on the same Xray line, so the exposure exists in production today.

**Base**: `19863c87` (`rc/v0.8.25`).

**Billing paths changed by this spec (constitution, Release Constraints)**: none.

## Clarifications

### Session 2026-11-22

- Q: Which destinations are private? → A: exactly the `geoip:private` list of the geoip.dat the image ships (tag
  202610202206, sha256 `4a1b1edd...`), written into the patch as explicit prefixes: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/3`, `::/127`, `fc00::/7`, `fe80::/10`, `ff00::/8`.
  Upstream's v26.11.9 default set is wider (it also covers translation and tunnelling prefixes such as NAT64
  `64:ff9b::/96`, 6to4 and Teredo); those stay reachable, as they are today, and the residual is recorded.
- Q: Which connections are guarded? → A: the ones the existing local-host refusal guards: connections whose inbound is
  `vless`, `vmess`, `trojan`, `hysteria`, `wireguard` or any `shadowsocks` inbound. SOCKS, HTTP and dokodemo-door
  inbounds (the node's `API_INBOUND` among them) and connections with no inbound stay as they are. The pin has no
  MASQUE inbound.
- Q: When is the destination checked? → A: at freedom's dial, on the address that is actually connected to: a
  literal address and a freedom-resolved name before the dial. On the path every production freedom outbound takes
  (the default system dialer directly: no `proxySettings`, no `dialerProxy`, the raw TCP transport for TCP, no
  alternative system dialer), freedom resolves nothing itself and hands the dial a connect check that the default
  system dialer runs inside `net.Dialer.Control` before every connect attempt, on the exact address Go is about to
  connect to, and on a UDP destination after its one resolution. On every other path freedom resolves a name once
  with the system resolver, refuses when any answer is the local host or a refused private address, refuses without
  dialing when the lookup fails, and dials only the checked addresses. The remote address of every dialed connection
  is checked again before any payload, and UDP datagrams are checked per packet after resolution.
- Q: The brief accepted the local-host patch's AsIs handshake caveat. Does it hold for private ranges? → A: No.
  Production's interface-bound outbounds (`india_wireguard` on core 16, the `*_wireguard` outbounds on core 1) are
  AsIs freedoms with `sockopt.interface`. At the pin an AsIs TCP dial resolves the name and completes the handshake
  inside one call, so an after-dial check alone sends the SYN to the private host through the WireGuard tunnel before
  it can refuse. The connect check runs before the socket connects, so a refused private address never gets a SYN,
  through a tunnel or otherwise.
- Q: Why not resolve the name before the dial, as the first two pushes did? (hostile review round 1 on `6597d6e1`,
  2026-11-22, BLOCK) → A: that check and the dial resolved the name separately. A rebinding name (public on the
  first answer, private on the next), a name whose first lookup fails (the check then let the dial go on), and a
  zoned link-local name in brackets that the resolver rejects but the dialer parses (a sniffed `Host:
  [[[fe80::…%eth0]]]`, H2) all reached private listeners; the SYNs went into an interface-bound tunnel; a closed
  private port answered a second later than an open one, through freedom's retries; and every new connection paid a
  second A and AAAA lookup. Checking the connect address itself removes the gap and the extra lookup; off the
  system path, one lookup whose answers are the only addresses dialed does the same. A refusal at connect time ends
  the dial without freedom's retries, so open and closed private ports look the same. The refusal is logged at
  warning by freedom itself: `6597d6e1` returned a warning-level error, which the outbound handler logged at info
  only (L4).
- Q: Does UDP-over-TCP bypass the guard? (found while porting the round-1 tests, 2026-11-22) → A: it did. The
  outbound handler answers the sing `uot` magic names (`sp.v2.udp-over-tcp.arpa`, `sp.udp-over-tcp.arpa`) with its
  own UDP socket, so a user inbound could send datagrams to any address, private or the local host, past every
  freedom check, on the 28-patch build and on `6597d6e1` alike. That socket now writes each datagram through the same
  connect check, which drops a refused one.
- Q: What may still be reached? → A: the local host, never. Any other private destination only when the outbound's
  `finalRules` allow it (first matching rule decides, `allow` or `block`, on network, port and address, as upstream
  writes them), or when a `privateAllow` entry of that outbound names the routing rule that decides for it: the
  router's own first match for the connection, with the address being dialed as its target, must be that rule, and
  the rule must send to this same outbound. `finalRules` decide private destinations only; public destinations stay
  as they are today.
- Q: Where does `privateAllow` come from? → A: the node writes it into every config it renders, from the core's own
  routing: each rule that sends literal private addresses to a freedom outbound is tagged (its own `ruleTag`, or a
  node tag `PG_NODE_PRIVATE_ALLOW_<n>` in the rendered routing only), and that outbound lists `{ruleTag, ip}` with
  exactly those addresses. A value the panel sends in that field is replaced. Rules that send private ranges to
  BLOCK, blackhole or any other non-freedom outbound never create an entry, nor do rules whose tag another rule
  shares or that also match on a domain. The ranges the node's own guard already keeps user inbounds off (the local
  host and link-local) are never listed.
- Q: Why ask the router at dial time instead of copying the rule's conditions? (automated security review, MEDIUM,
  2026-11-22: "authorization scope mismatch", "allowlist broader than routing semantics") → A: a list derived from
  the rules alone cannot see rule order: a private route that an earlier rule blocks would still open its addresses
  to any name that reaches the outbound through the domain pass. Asking xray's own router (all conditions, first
  match, the address being dialed) keeps the allow exactly as wide as routing. The router is asked through
  `MatchingRule`, which runs the rules without firing webhooks or picking balancer members.
- Q: How does the canary in `backend/xray/freedom_private_target_nodeverify_test.go` (v0.8.25, `92a8aa57`) know that
  a build reporting 26.3.27 blocks? → A: by probing the binary, not by a version or path constant: the patched loader
  reads `privateAllow` and refuses an entry without an address list, which a stock v26.3.27 binary ignores, so
  `xray run -test` exits non-zero on that probe only when the guard is built in, while the same config without the
  entry passes on both. A guarded build is expected to behave like v26.5.3 and later on all three canary paths; the
  stock paths keep every assertion they have.
- Q: Do interface-bound freedom outbounds need the guard? → A: Yes. A refused private destination must not be sent
  into a tunnel, so they are guarded like every other freedom outbound, and the connect check keeps the SYN out of
  the tunnel.
- Q: Which outbound keys does the node read? (hostile review round 1, L5 and L6) → A: Xray reads JSON keys in any
  letter case, so the node finds `protocol`, `tag` and `settings` in any letter case too, and leaves an outbound that
  repeats one of them without `privateAllow`. A rule's `domain` or `domains` key is a condition only when its list is
  not empty, as in Xray, so `"domain": []` still gives its addresses an allow.
- Q: What did hostile review round 2 on `d6da520b` find? (BLOCK, 2026-11-22) → A: no bypass, one breakage (R2-M1,
  medium): the mux server marks every XUDP flow's context timeout-only, and freedom then swaps its context for a
  background one before it copies the datagrams, so the per-packet check, which read that variable, lost the session
  outbounds and refused every datagram an allow covers, silently: XUDP to `100.64.0.0/10` through rule#2 and `DIRECT`
  on cores 1, 17, 19 and 21 delivered nothing. Freedom detaches such a flow because an XUDP flow can be taken over
  by a later mux connection and must outlive the one that opened it, ending only on inactivity; it now detaches
  with `context.WithoutCancel` instead of a fresh background context, which drops the opener's cancellation and
  keeps the session values, so the per-packet check, the log lines and anything else in the flow see the session.
  Lows fixed in the same round: UDP-over-TCP datagrams are judged as UDP, not by the TCP name that opened the
  stream (R2-L1); a freedom that resolves a name itself (`domainStrategy`, `DIRECT` is `ForceIPv4`) takes its random
  answer among those it would dial, so a public answer is used instead of refusing a private pick (R2-L3), for the
  connection and for each datagram; an outbound whose protocol key in any letter case names freedom loses every
  panel-written `privateAllow`, also when it repeats `protocol`, `tag` or `settings` and therefore gets no derived
  allow (R1-L5); the refusal line names the inbound's tag next to its protocol (R2-L6).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A proxy user cannot reach private networks through freedom (Priority: P1)

A paying user of any user inbound asks the node to connect to a private address, by literal IPv4 or IPv6 address or
by a public name that resolves to one, through a freedom outbound with or without an interface binding.

**Why this priority**: it is the exploitable exposure (cloud metadata, the node's LAN and docker networks, the far
side of the WireGuard egress tunnels).

**Independent Test**: a real VLESS user of a production-shaped core (core 1's rule layout, and core 16's) asks for a
listener on a private address of the test host, literally and through a name that the core's own DNS resolves to it,
in the domain pass of an inbound-specific rule; nothing reaches the listener and the user receives no bytes.

**Acceptance Scenarios**:

1. **Given** core 1's layout with the private block after the CGNAT rule and inbound-specific rules after it,
   **When** a user of an inbound with its own rule asks for a name that resolves to a private address, **Then** the
   connection is refused and the listener receives nothing.
2. **Given** core 16's layout (AsIs, no private block, every request to an interface-bound freedom), **When** the
   user asks for a private address or a name resolving to one, **Then** the connection is refused before the dial.
3. **Given** any guarded inbound, **When** it addresses UDP datagrams to a private address, **Then** they are dropped.

---

### User Story 2 - Private routes the core configures on purpose keep working (Priority: P1)

Core 1 routes `100.64.0.0/10` to the freedom outbound `DIRECT` on purpose, ahead of its private block.

**Why this priority**: a guard that breaks deliberate production routes cannot ship.

**Independent Test**: on the same core shape, a user's connection to a CGNAT listener goes through `DIRECT` and
carries data, while a name resolving to the same CGNAT address through an inbound whose rule picks a different
freedom outbound, and every other private range through `DIRECT`, stay refused.

**Acceptance Scenarios**:

1. **Given** a rule sending `100.64.0.0/10` to `DIRECT`, **When** a user connects to a CGNAT address, **Then** data
   flows through `DIRECT`.
2. **Given** the same rule, **When** a user reaches `DIRECT` for a `10.0.0.0/8` address by name, **Then** it is
   refused: the allow does not leak to other ranges.
3. **Given** an allow derived from a rule limited to some inbound tags, **When** a user of another inbound reaches the
   same outbound for that range by name, **Then** it is refused.

---

### User Story 3 - Everything else keeps working (Priority: P2)

Public destinations of user inbounds, and every destination of SOCKS, HTTP and dokodemo-door inbounds (panel
tunnels, the node's API), behave as before.

**Independent Test**: on the same cores, public targets carry data for user inbounds, and a dokodemo-door tunnel and
a SOCKS inbound still reach private and loopback listeners.

**Acceptance Scenarios**:

1. **Given** a guarded inbound, **When** it asks for a public address, **Then** data flows as before.
2. **Given** a dokodemo-door or SOCKS inbound, **When** it targets a private address, **Then** data flows as before.

---

### User Story 4 - The canary records what the shipped core does (Priority: P3)

`backend/xray/freedom_private_target_nodeverify_test.go` states, per core under test, whether a user inbound reaches a
private target through freedom.

**Independent Test**: the held-pin branch expects the block again on the patched build, and still expects the old
behaviour on a stock v26.3.27 binary.

## Edge Cases

- A name with both public and private addresses: on the system path every connect attempt is checked, so no private
  answer is ever connected to and the dial may still use a public answer; off the system path the name is refused.
- The resolver fails: on the system path the dial fails as it does today; off it the name is refused without a dial.
- A name whose answer changes between lookups: on the system path the check sees the address of each connect; off it
  only the addresses of the one lookup are dialed.
- A freedom outbound chained through `sockopt.dialerProxy` or `proxySettings`: freedom resolves the name once and
  hands the chain a checked address, so the chain never resolves the name itself; its remote end reports the
  unspecified address and is not checked again after the dial.
- IPv4-mapped IPv6 destinations are checked as IPv4; an IPv6 zone is stripped before the check; an address the check
  cannot parse is refused.
- UDP-over-TCP through freedom: each datagram's destination is checked as UDP; a refused one is dropped without a log
  line.
- An XUDP flow: freedom detaches it from the context that opened it with `context.WithoutCancel`, so it still
  outlives that context and ends only on inactivity, and its datagrams are checked with the session's routing
  context, so an allow covers them as it covers a plain UDP flow.
- A name a freedom resolves itself (`domainStrategy`) with a public and a private answer: the random answer is taken
  among the ones the guard would dial; only when every answer is refused is one of them taken, and refused.
- A routing rule that also matches on a domain, targets a balancer or shares its `ruleTag` with another rule gives no
  allow; the node logs it. Other conditions (inbound tag, user, source, network, port, protocol, local address) are
  the router's to evaluate.
- A routing rule that an earlier rule shadows (a block, or another outbound) gives no allow in practice: the router's
  first match for the address is the earlier rule.
- Rules added at run time through the RoutingService are not listed until the next render; a new earlier block still
  wins, because the router is asked at dial time.
- A stock v26.3.27 binary ignores the new fields and does not guard.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: For guarded inbounds, freedom MUST NOT open a connection to, or send a datagram to, an address in the
  private set unless FR-004 allows it.
- **FR-002**: The check MUST apply to the address actually connected to: a literal address and a name freedom
  resolves before the dial; on the default system dialer path every connect attempt, in `net.Dialer.Control`, and a
  UDP destination after its resolution; off that path the answers of one system lookup, the only addresses dialed;
  the remote address of the dialed connection; each UDP datagram, with the routing context the connection started
  with; and each UDP-over-TCP datagram, judged as UDP.
- **FR-003**: The local host MUST stay refused whatever an allow says.
- **FR-004**: The only allows are the outbound's `finalRules` (first match decides, `allow` or `block`) and its
  `privateAllow` entries, each naming a routing rule and its explicit private addresses; an entry allows only when
  the router's first match for the connection with the dialed address as target is that rule, sending to this
  outbound.
- **FR-005**: The node MUST write `privateAllow` into every freedom outbound of every config it renders, derived from
  that config's routing rules as clarified above, tag the rules it names in the rendered routing, and MUST replace a
  panel-supplied value; every outbound whose protocol key, in any letter case, names freedom loses the panel's value,
  and one that repeats `protocol`, `tag` or `settings` gets none of its own.
- **FR-006**: SOCKS, HTTP and dokodemo-door inbounds, and connections without an inbound, MUST be unaffected.
- **FR-007**: Interface-bound freedom outbounds MUST be guarded like any freedom outbound.
- **FR-008**: A refused connection MUST be logged at warning with the inbound and the refused address, and no payload
  is sent; a refused UDP datagram is dropped without a log line, as the local-host refusal drops them.
- **FR-009**: The image build MUST fail if the patch stops applying or loses any part (Dockerfile guards; the guard
  replay catches every dropped file).
- **FR-010**: The configs the node renders MUST stay loadable by a stock v26.3.27 binary.
- **FR-011**: On the default system dialer path the guard MUST NOT add a DNS lookup; off it, one lookup per attempt,
  and a failed or empty lookup MUST refuse without a dial.
- **FR-012**: A refusal at connect time MUST end the dial without retries, so an open and a closed private port look
  the same to the user.
- **FR-013**: A freedom that resolves a name itself MUST NOT pick a refused answer while another answer is dialable.

### Key Entities

- **Private set**: the 18 prefixes above.
- **Guarded inbound**: an inbound whose name is one of the user inbound names above.
- **privateAllow entry**: the `ruleTag` of the routing rule it trusts and that rule's explicit private addresses,
  node-owned, per outbound.
- **finalRules entry**: `action`, `network`, `port`, `ip`, as upstream writes them.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Patch-level tests fail on the current 28 patches (a private target is reached) and pass with the patch:
  IPv4 and IPv6, a name resolving to a private address (freedom-resolved and AsIs), the CGNAT allow, and an allow that
  does not leak to other ranges or inbounds.
- **SC-002**: Node-level tests against a real `xray run` of production-shaped cores fail on the base and pass on the
  change: private refused for user inbounds including by name in the domain pass, CGNAT through `DIRECT` carries data,
  public targets and SOCKS and dokodemo-door inbounds unaffected.
- **SC-003**: Mutants are killed: without the dial-time check the name case fails; without the allow derivation the
  CGNAT case fails; without the connect check, without the zone strip, with a failed lookup dialed anyway, or with
  the name handed to a chain, a private listener sees a handshake (26 mutants, all killed, `tasks.md` T049).
- **SC-006**: In the CI harness (`scripts/private_target_harness.py`) the guarded core sends as many DNS queries per
  request as the same core without the guard, and its median time to the first byte with a 20 ms resolver is within
  10 ms of it.
- **SC-004**: The full node test suite passes on Linux in GitHub CI for the pushed commit.
- **SC-005**: The fleet-impact statement names every production flow whose behaviour changes.

## Assumptions

- The fleet facts come from the main session's read-only reads of the production panel on 2026-11-22: no core has a
  MASQUE inbound or a freedom outbound with `finalRules`, and the deliberate private allow is rule#2 of cores 1, 17,
  19 and 21 (`100.64.0.0/10` to `DIRECT`). The local core snapshots of 2026-11-10 to 2026-11-16 also show no
  `dialerProxy`, `proxySettings`, balancer rules or freedom `redirect`.
- The patched build now reads `finalRules`, so a `finalRules` entry with an unknown action fails the config check
  there, as it does on v26.5.3 and later; no production core carries `finalRules`.
- The node ships the geoip.dat named above; the private set is fixed in code and does not follow later geo files.
- `NodeVersion` and the image tag move in a separate release change.
- The sing-box (Hysteria2), kernel WireGuard and L2TP backends do not route through xray freedom and are out of scope.

## Fleet impact

From the main session's read-only reads of the production panel on 2026-11-22 (9 cores, no freedom `redirect`, no
MASQUE inbound, no freedom `finalRules`; VLESS and VMess outbounds are not under the freedom guard):

- Cores 1, 17, 19 and 21 (`IPIfNonMatch`, rule#2 `100.64.0.0/10` to `DIRECT`, rule#3 `geoip:private` to `BLOCK`,
  inbound rules after): a user's request by name that an inbound rule matches in the domain pass and that resolves
  to a private address is refused, through `DIRECT` and through the interface-bound `*_wireguard` outbounds alike;
  it reached the address before. A CGNAT literal or name through `DIRECT` keeps working, because the router's first
  match for the address is rule#2, which the node tags and lists. A CGNAT name through any other freedom outbound is
  refused, since rule#2 sends that address to `DIRECT`.
- Core 16 (`AsIs`, no private block, `india` to `india_wireguard`): every private destination of a user is refused,
  by literal or by name, and nothing is sent into the WireGuard tunnel for it. No allow is derived there.
- XUDP: a user's datagrams to `100.64.0.0/10` through rule#2 and `DIRECT` keep flowing, like plain UDP; datagrams to
  other private addresses inside a full-cone flow are dropped.
- UDP-over-TCP: through the AsIs freedom outbounds (the `*_wireguard` outbounds of cores 1, 17, 19 and 21, and core
  16) a user's datagrams to private addresses and the local host are dropped; they were delivered. Through `DIRECT`
  UDP-over-TCP never worked: `DIRECT` is `ForceIPv4`, and the magic names do not resolve.
- `DIRECT` (`ForceIPv4`) with a name that has a public and a private answer: the public answer is used every time;
  before, the random pick sometimes reached the private address.
- Public destinations are dialed as before, with no extra DNS lookup on the default system dialer path, which every
  production freedom outbound takes. Each refused connection writes one warning line to the core log, naming the
  inbound's protocol and tag.
- Unchanged: the dokodemo-door, SOCKS and HTTP inbounds (the node's `API_INBOUND` among them), connections without
  an inbound, public destinations, and the sing-box, kernel WireGuard and L2TP backends.
