# Data Model: Node release 0.6.21

## Inbound ledger (sing-box backend, in memory)

| Field | Type | Meaning |
|---|---|---|
| `mu` | mutex | serializes drain + credit + snapshot + clear |
| `pending[consumerOutbounds]` | map counter name -> int64 bytes | traffic drained from sing-box and not yet collected by a node-level read with reset |
| `pending[consumerInbounds]` | map counter name -> int64 bytes | same for the per-inbound read |

- **Key**: the full sing-box counter name, `inbound>>><tag>>>traffic>>><uplink|downlink>`.
- **Invariant**: for every key, each consumer's balance = (sum of all drained values for that key) -
  (sum that consumer has collected with reset). The two consumers never affect each other.
- **Transitions**:
  - `drain`: sing-box counters -> +value in both maps (sing-box side becomes 0).
  - `collect(consumer, tag, reset=false)`: drain, then report the consumer's balances; nothing cleared.
  - `collect(consumer, tag, reset=true)`: drain, report, then delete exactly the reported keys from that
    consumer's map.
  - drain error: return the error; no credit, no clear.
  - core restart: nothing happens to the ledger; the next drain sees counters that restarted at zero.
- **Bounds**: keys are bounded by distinct inbound tags x 2 directions; a removed tag's balance is
  reported once more per consumer and then deleted by that consumer's reset read. A consumer that never
  reads keeps at most one int64 per key.
- **Loss model**: unchanged from 0.6.20 (audit F2c, same as upstream): if the gRPC response to the panel
  is lost after a reset read, that consumer's reported bytes are gone; the other consumer is unaffected.

## OpenVPN instance trackers (per `instanceProcess`, in memory)

| Field | Type | Used by |
|---|---|---|
| `statsTracker` | `*stats.InterfaceCountersTracker` | Outbound, Outbounds (node-level), unchanged |
| `inboundStatsTracker` | `*stats.InterfaceCountersTracker` | Inbound, Inbounds (new) |

- Both read the same cumulative aggregate (`aggregateStats()`); each keeps its own forward-only
  baseline via `DeltaClamped`; a reset on one never moves the other's baseline.
- First read of the new tracker after start sets its baseline and reports 0, as the existing one does.

## Panel series (read only, for verification)

| Series | Written from | Checked by |
|---|---|---|
| `node_usages` | node-level read | SC-002 denominator |
| `node_inbound_usages` | per-inbound read | SC-002 numerator |
| `node_user_usages` | per-user read | SC-003 (unchanged billing) |

## Release artefacts

| Entity | Value |
|---|---|
| Reported version | `0.6.21` |
| Git tag / image tag | `v0.8.21-session-kill` |
| Image | `ghcr.io/free-guy-ir/node@sha256:<recorded at T029>` |
| Rollback image | `ghcr.io/free-guy-ir/node@sha256:6c5420a4f386140334c1f36020b2741cca6eb684e4d9d5c28217497e0685ccfd` (0.6.20) |
| Per-node backup | `docker-compose.yml.bak-v821-<ts>`, `docker-compose.override.yml.bak-v821-<ts>` |
