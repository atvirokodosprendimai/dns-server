# Anycast Edge Deployment

This runbook describes how to deploy the same DNS container to all anycast edges using one `docker-compose.yml`, with secrets in `~/wg/.env` on each node.

## Goals

- Same compose definition on every edge.
- Node-specific config only in `~/wg/.env`.
- Safe rollout and rollback across mesh.
- DNS binds to edge/VPN IP on port 53.

## Directory Layout On Every Edge

```text
~/wg/
  docker-compose.yml
  .env
  data/
```

## 1) Prepare edge host

Install Docker + Compose plugin.

Create directory:

```bash
mkdir -p ~/wg/data
```

Copy `docker-compose.yml` from this repo (`deploy/docker-compose.yml`) to `~/wg/docker-compose.yml`.

## 2) Create node-specific env file

Create `~/wg/.env` from `deploy/.env.example`.

Required values per node:

- `NODE_ID` unique per edge.
- `DNS_UDP_LISTEN` and `DNS_TCP_LISTEN` bound to that node edge/VPN IP (not `:53` wildcard in production).
- `API_TOKEN` and `SYNC_TOKEN` strong random secrets.
- `PEERS` all other mesh node API URLs.

Example:

```env
NODE_ID=edge-vilnius-1
HTTP_LISTEN=:8080
DNS_UDP_LISTEN=10.10.0.11:53
DNS_TCP_LISTEN=10.10.0.11:53
DB_PATH=/data/dns.db

API_TOKEN=replace_with_long_random
SYNC_TOKEN=replace_with_long_random

DEFAULT_ZONE=cloudroof.eu
DEFAULT_NS=snail.cloudroof.eu,rabbit.cloudroof.eu
DEFAULT_TTL=60

PEERS=http://10.10.0.12:8080,http://10.10.0.13:8080
```

## 3) Deploy

```bash
cd ~/wg
docker compose pull
docker compose up -d
docker compose ps
```

## 4) Verify edge health

```bash
curl -fsS http://127.0.0.1:8080/healthz
dig @10.10.0.11 cloudroof.eu NS +short
dig @10.10.0.11 snail.cloudroof.eu A +short
dig @10.10.0.11 rabbit.cloudroof.eu AAAA +short
```

## 5) Rollout strategy (all edges)

- Stage 1: deploy to one canary edge.
- Stage 2: validate DNS/API/sync for 5-10 minutes.
- Stage 3: deploy to 20-30% of edges.
- Stage 4: deploy to remaining edges.

Use one immutable image tag for all nodes in one rollout wave.

## 6) Rollback

Edit `~/wg/docker-compose.yml` image tag back to previous known-good tag and redeploy:

```bash
cd ~/wg
docker compose pull
docker compose up -d
```

## 7) Security notes

- Keep `~/wg/.env` owner-only (`chmod 600 ~/wg/.env`).
- Do not commit `.env` to git.
- Restrict API (`8080`) to VPN/management network.
- Rotate `API_TOKEN` and `SYNC_TOKEN` periodically.

## 8) Networking notes

- Open inbound: `53/udp`, `53/tcp`, `8080/tcp` (VPN scope for API).
- If local resolver already uses loopback `:53`, binding to specific edge IP still works.

## 9) Initial zone/bootstrap

After first deploy, run bootstrap scripts from an operator host:

```bash
API_BASE=http://10.10.0.11:8080 API_TOKEN=... bash scripts/set-root-ns.sh \
  cloudroof.eu snail.cloudroof.eu 198.51.100.10 rabbit.cloudroof.eu 198.51.100.11

API_BASE=http://10.10.0.11:8080 API_TOKEN=... bash scripts/set-ns-aaaa.sh \
  cloudroof.eu snail.cloudroof.eu 2001:db8::10 rabbit.cloudroof.eu 2001:db8::11
```

## 10) GitHub SSH deployment pipeline

Workflow: `.github/workflows/deploy-edges.yml`

Deployment job uses GitHub Environment: `test`.

Current workflow does not pin SSH host fingerprints (auto-accept behavior).

It runs:

```bash
cd ~/wg && docker compose pull && docker compose up -d
```

for all edge hosts over SSH.

Required GitHub secrets:

- `DEPLOY_HOSTS` (comma-separated hosts/IPs, e.g. `10.10.0.11,10.10.0.12`)
- `DEPLOY_SSH_USER` (e.g. `root` or deploy user)
- `DEPLOY_SSH_PRIVATE_KEY` (private key matching public key on edge nodes)

Optional secrets:

- `DEPLOY_SSH_PORT` (default `22`)
- `DEPLOY_SSH_PASSPHRASE` (if private key is encrypted)

Manual deploy:

- Trigger `deploy-edges` via GitHub Actions `workflow_dispatch`.
- Optionally provide `hosts` input to override `DEPLOY_HOSTS` for one-off deploy.
