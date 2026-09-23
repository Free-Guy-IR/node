# Contract: parametrized rollout scripts (panel host, outside the repository)

Replaces the hardcoded `inner20.sh`, `verify20.sh` and `rc1.sh` for this and later releases.
`deploy_l2tp_node.sh` is not used.

## Inputs (environment or flags, all required)

| Name | Example | Rule |
|---|---|---|
| `TAG` | `v0.8.21-session-kill` | informational; written to the log |
| `DIGEST` | `sha256:<64 hex>` | the compose files are pinned to `ghcr.io/free-guy-ir/node@$DIGEST` |
| `EXPECTED_VERSION` | `0.6.21` | exact match against the panel's `node_version` |
| `PASSWORD_FILE` | path to a file | must exist, owner-only, mode `0600`, else abort |
| `NODE_ID` / `COMPOSE_DIR` | from the fleet map | one node per invocation |

## Per-node update (inner)

1. Abort if `DIGEST` is not 71 characters starting `sha256:`.
2. Copy both compose files to `*.bak-v821-<ts>`; abort if either copy fails.
3. In both files, replace `image: ghcr.io/free-guy-ir/node[:@]<anything>` with the digest form; abort
   if neither file contains the image line afterwards.
4. `docker compose pull` then `docker compose up -d`.
5. Assert the running container's image id equals `DIGEST`'s image; else roll back (step R).

## Verify (outer)

1. Authenticate to the panel API with the password read from `PASSWORD_FILE` via stdin or
   `--data @file`; the password never appears on any argv, in `ps`, or in logs.
2. Reconnect the node through the panel's reconnect endpoint (never `POST /api/node/{id}/update`).
3. Poll until `status == "connected"` and `node_version == EXPECTED_VERSION`, or time out and roll back.
4. Check the container log for `Starting Node: v$EXPECTED_VERSION`, a successful user sync, and no
   `panic` or `error` lines since the restart.
5. Print one result line per node: node id, digest, status, version, PASS/FAIL, and the UTC timestamps
   of the change start and the gate result (SC-005 is measured from these).

## Rollback (R)

Restore both `*.bak-v821-<ts>` files, `docker compose up -d`, reconnect, and verify with
`EXPECTED_VERSION=0.6.20`.

## Disk guard

Before `pull`, require free space on the Docker root >= 2 x the compressed image size; never prune the
rollback image until the node has passed its gate.
