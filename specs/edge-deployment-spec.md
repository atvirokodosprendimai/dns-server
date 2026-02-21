# Edge Deployment Spec

This document defines required deployment behavior for anycast edge nodes.

## Scope

- Applies to all production/staging anycast edges.
- Uses one shared compose definition for all nodes.
- Uses per-node `~/wg/.env` for secrets and node-specific values.

## Required layout

Each edge host MUST have:

- `~/wg/docker-compose.yml`
- `~/wg/.env`
- `~/wg/data/`

## Image policy

- Runtime image MUST be pulled from GHCR for this repository.
- Rollout wave MUST pin one immutable image tag across all nodes.
- `latest` MAY be used only in non-production environments.

## Binding policy

- DNS listeners MUST bind to explicit edge/VPN IP on port 53.
- Wildcard `:53` SHOULD be avoided in production.
- API listener SHOULD be restricted to management/VPN networks.

## Secret policy

- `API_TOKEN` and `SYNC_TOKEN` MUST be set.
- Tokens MUST be high entropy random values.
- `~/wg/.env` permissions SHOULD be `0600`.
- Secrets MUST NOT be committed to repository.

## Sync mesh policy

- `PEERS` MUST include all intended replication targets.
- Peer URLs MUST be reachable over private trusted network.
- `SYNC_TOKEN` MUST match across nodes that replicate with each other.

## Persistence policy

- `DB_PATH` MUST point to mounted persistent storage (`/data/dns.db`).
- Edge restart MUST preserve DNS records and zones.

## Health and verification

After deploy/update, each edge MUST pass:

- `GET /healthz` returns success.
- DNS `NS` and glue (`A`/`AAAA`) queries return expected values.
- At least one replicated write is visible on peer nodes.

## Rollout policy

- Rollout sequence MUST be canary -> partial -> full fleet.
- Proceed to next wave only after health and functional verification.

## Rollback policy

- Rollback MUST restore previous known-good image tag.
- Rollback execution MUST use same compose file and `.env`.

## Change control

- Compose changes SHOULD be reviewed before production rollout.
- `.env` changes MUST be tracked in operator change logs.
