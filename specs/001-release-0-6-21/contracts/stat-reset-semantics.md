# Contract: stat reads and reset semantics (node gRPC/REST `GetStats`)

The panel calls `GetStats(StatRequest{type, name, reset})`. This release changes the behaviour only in
the rows marked **changed**.

| Backend | StatType | Source | reset=true clears | Status |
|---|---|---|---|---|
| sing-box | Outbounds / Outbound(name) | node ledger, consumer "outbounds" | only this consumer's reported balances | **changed** |
| sing-box | Inbounds / Inbound(name) | node ledger, consumer "inbounds" | only this consumer's reported balances | **changed** |
| sing-box | UsersStat / UserStat(name) | sing-box `user>>>` counters | sing-box `user>>>` counters | unchanged |
| OpenVPN | Outbounds / Outbound(name) | aggregate, `statsTracker` | `statsTracker` baseline | unchanged |
| OpenVPN | Inbounds / Inbound(name) | aggregate, `inboundStatsTracker` | `inboundStatsTracker` baseline | **changed** |
| OpenVPN | UsersStat / UserStat(name) | `b.statsTracker` | `b.statsTracker` | unchanged |
| Xray, WireGuard, L2TP, MTProto | all | as 0.6.20 | as 0.6.20 | unchanged |

Response shape is unchanged: `StatResponse.stats[]` with `name` = inbound tag, `link` as before,
`type` = `uplink` / `downlink`, `value` = bytes since that consumer's previous reset read.

Guarantees after this release, for sing-box and OpenVPN:

1. For any interleaving of node-level and per-inbound reads, each series sums to all traffic observed.
2. A read with `reset=false` never changes what any later read reports.
3. Per-user reads return exactly what 0.6.20 returned for the same traffic.
4. An unknown OpenVPN instance name still returns the existing error; a sing-box tag with no traffic
   returns an empty list.
