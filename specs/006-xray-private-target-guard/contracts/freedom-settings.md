# Contract: freedom outbound settings on the patched v26.3.27 build

```json
{
  "tag": "DIRECT",
  "protocol": "freedom",
  "settings": {
    "domainStrategy": "UseIPv4",
    "finalRules": [
      {"action": "block", "ip": ["10.0.0.0/8"]},
      {"action": "allow", "network": "tcp", "port": "443", "ip": ["192.168.0.0/16"]}
    ],
    "privateAllow": [
      {"ip": ["100.64.0.0/10"]},
      {"ip": ["10.8.0.0/16"], "inboundTag": ["relay-in"], "network": "tcp", "port": "22,443"}
    ]
  }
}
```

- The node owns `privateAllow`: it is replaced on every render by the entries derived from the core's routing.
- A refused connection ends with a warning
  `refused <target> from the <inbound> inbound: it is the private address <address>`; a local-host refusal keeps its
  existing `... it is the local host address <address>` text.
- Config check failures: `finalRules` with an unknown `action`; `privateAllow` entry without `ip`; an `ip` entry that
  routing would also reject.
- A stock v26.3.27 binary ignores both fields, so the same config loads there and is not guarded.
