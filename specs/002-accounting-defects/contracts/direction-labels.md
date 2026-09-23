# Contract: uplink/downlink labels

`pkg/stats` receives `Rx` and `Tx` from the node's point of view towards the client:

| Field | Meaning | Label |
|-------|---------|-------|
| `Rx` | bytes the node received from the client (client to server) | `uplink` |
| `Tx` | bytes the node sent to the client (server to client) | `downlink` |

This holds for per-user stats (`Tracker`), per-interface/instance stats and node-level stats
(`BuildInterfaceStats`).

| Backend | `Rx` source | `Tx` source |
|---------|-------------|-------------|
| WireGuard | peer `ReceiveBytes`; interface `rx_bytes` | peer `TransmitBytes`; interface `tx_bytes` |
| L2TP | ppp `rx_bytes` | ppp `tx_bytes` |
| OpenVPN | `bytes_received` / `BYTES_IN` | `bytes_sent` / `BYTES_OUT` |
| MTProto | bytes written towards Telegram or the front (client to server) | bytes read from Telegram or the front |

Invariant: for every stat name, `uplink + downlink` equals `Rx + Tx` and equals the eab2601 value for
the same input. Zero values are omitted as today.
