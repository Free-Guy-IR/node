# Data Model: proxy users cannot reach private networks through the node's freedom outbounds

## Private set

The 18 prefixes listed in spec.md, IPv4 and IPv6. An IPv4-mapped IPv6 address is compared as IPv4. The local host
(loopback and the unspecified addresses) is the part of the set that no allow can open.

## Guarded inbound

`session.Inbound.Name` is `vless`, `vmess`, `trojan`, `hysteria`, `wireguard`, or starts with `shadowsocks`.

## `finalRules` entry (freedom settings, upstream shape)

| Field | Type | Rule |
|-------|------|------|
| `action` | string | `allow` or `block`, case-insensitive; anything else fails the config check |
| `network` | string or list | `tcp`, `udp`; absent matches both |
| `port` | number, string or range | absent matches every port |
| `ip` | list | what routing's `ip` accepts (`geoip:`, `ext:`, prefixes, negation); absent matches every address |

Entries are evaluated in order for a private destination of a guarded inbound; the first match decides. Fields
upstream also accepts (`delay`, `logLevel`, `blockDelay`) are ignored.

## `privateAllow` entry (freedom settings, node-owned)

| Field | Type | Rule |
|-------|------|------|
| `ip` | list | required, non-empty; same syntax as `finalRules` |
| `inboundTag` | string or list | absent matches every inbound tag |
| `network` | string or list | absent matches both |
| `port` | number, string or range | absent matches every port |

Any matching entry allows. The node writes this field into every freedom outbound it renders and removes any value it
did not derive.

## Derived allow (node)

One entry per routing rule that passes R5 of research.md: `ip` holds the rule's literal private addresses and
prefixes, and `inboundTag`, `network` and `port` are copied from the rule verbatim.

## Decision order for a destination D of a connection from inbound I through freedom F

1. I is not guarded: connect.
2. D is the local host: refuse.
3. D is not private: connect.
4. The first `finalRules` entry of F that matches D decides.
5. A `privateAllow` entry of F that matches D allows.
6. Refuse.
