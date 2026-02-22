# dns-server

A minimal authoritative DNS server (`A`, `AAAA`, `TXT`, `CNAME`, `MX`, `NS`, `SOA`) with an HTTP control API and peer-to-peer synchronization across anycast nodes.

## What It Does

- Answers DNS queries over UDP/TCP using `github.com/miekg/dns`.
- Supports DNS over HTTPS (DoH) at `/dns-query`.
- Keeps active `A`/`AAAA`/`TXT`/`CNAME`/`MX` records and zone (`NS`/`SOA`) config in memory.
- Persists all records and zones in SQLite (pure Go, no CGO).
- Lets you manage records via HTTP API with token authentication.
- Replicates updates to peer nodes through `/v1/sync/event` (for example over VPN).

## Run

```bash
API_TOKEN=supersecret \
DB_PATH=./dns.db \
DEFAULT_ZONE=example.com \
DEFAULT_NS=love.me.cloudroof.eu,hate.you.cloudroof.eu \
PEERS=http://10.1.0.2:8080,http://10.1.0.3:8080 \
go run .
```

Important environment variables:

- `API_TOKEN` - control API token (`Authorization: Bearer <token>` or `X-API-Token`)
- `SYNC_TOKEN` - sync endpoint token (`X-Sync-Token`), if empty it falls back to `API_TOKEN`
- `HTTP_LISTEN` - default is `:8080`
- `DNS_UDP_LISTEN` - default is `:53`
- `DNS_TCP_LISTEN` - default is `:53`
- `DB_PATH` - SQLite file path, default `dns.db`
- `DEBUG_LOG` - enable verbose request logging (`true`/`false`, default `false`)
- `PEERS` - comma-separated peer URLs (without path)
- `DEFAULT_ZONE` - optional default zone
- `DEFAULT_NS` - optional default NS list
- `DEFAULT_TTL` - default is `20`

## API Examples

Create or update an `A` record:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/records/app.example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"ip":"203.0.113.10","ttl":15}'
```

Create or update an `AAAA` record:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/records/app.example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"ip":"2001:db8::10","type":"AAAA","ttl":15}'
```

Create or update a `TXT` record:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/records/meta.example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"type":"TXT","text":"site-verification=abc","ttl":60}'
```

Create or update a `CNAME` record:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/records/www.example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"type":"CNAME","target":"app.example.com","ttl":60}'
```

Create or update an `MX` record:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/records/example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"type":"MX","target":"mail.example.com","priority":10,"ttl":60}'
```

Add/remove records without replacing existing RRset members:

```bash
# add another A value
curl -sS -X POST "http://127.0.0.1:8080/v1/records/pool.example.com/add" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"type":"A","ip":"198.51.100.11"}'

# remove one specific A value
curl -sS -X POST "http://127.0.0.1:8080/v1/records/pool.example.com/remove" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"type":"A","ip":"198.51.100.11"}'
```

Delete a record:

```bash
curl -sS -X DELETE "http://127.0.0.1:8080/v1/records/app.example.com" \
  -H "Authorization: Bearer supersecret"
```

Update zone NS:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/zones/example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"ns":["love.me.cloudroof.eu","hate.you.cloudroof.eu"],"soa_ttl":60}'
```

Verify:

```bash
dig @127.0.0.1 app.example.com A +short
dig @127.0.0.1 example.com NS +short
dig @127.0.0.1 example.com SOA +short
```

## DoH (DNS over HTTPS)

Endpoint:

- `GET /dns-query?dns=<base64url-wire-format>`
- `POST /dns-query` with `application/dns-message` payload

Example with `curl` and `kdig` (from Knot DNS utils):

```bash
kdig @127.0.0.1 +https app.example.com A
```

If you expose this publicly, terminate TLS in front of the app (for example with Caddy, Nginx, or HAProxy), because standard DoH clients expect HTTPS.

## Scripts

- `scripts/bootstrap-db.sh` - seeds zone + sample A records through API into a fresh DB.
- `scripts/smoke-test.sh` - verifies A/NS/SOA responses and DoH endpoint reachability.
- `scripts/smoke-all.sh` - comprehensive smoke tests: RR (`A`/`AAAA` add/remove), TXT, CNAME, MX, NS/SOA, DoH.
- `scripts/smoke-self-contained.sh` - starts local server, bootstraps required data, then runs full smoke suite.
- `scripts/bootstrap-local-db.sh` - starts local server, bootstraps DB, runs smoke checks.
- `scripts/add-domain.sh` - adds a zone via API: `domain [ns1] [ns2]`; auto-generates NS hostnames if omitted.
- `scripts/set-root-ns.sh` - sets zone NS pair and creates glue A records (for example `snail.cloudroof.eu` and `rabbit.cloudroof.eu`).
- `scripts/set-ns-aaaa.sh` - adds or updates AAAA glue for NS1/NS2 (keeps existing A records).
- `scripts/park-domain.sh` - one script for ipv4-only / ipv6-only / dual-stack parking and NS glue management.

Example:

```bash
bash scripts/bootstrap-local-db.sh
```

Run comprehensive smoke suite:

```bash
API_BASE=http://127.0.0.1:8080 API_TOKEN=supersecret DNS_HOST=127.0.0.1 DNS_PORT=53 \
  ZONE=cloudroof.eu NS1=snail.cloudroof.eu NS2=rabbit.cloudroof.eu \
  bash scripts/smoke-all.sh
```

Run self-contained CI-style smoke suite (bootstraps everything itself):

```bash
bash scripts/smoke-self-contained.sh
```

Add a domain quickly:

```bash
API_TOKEN=supersecret NS1_IP=198.51.100.10 NS2_IP=198.51.100.11 \
  bash scripts/add-domain.sh cloudroof.eu
```

With explicit NS names:

```bash
API_TOKEN=supersecret bash scripts/add-domain.sh cloudroof.eu love.me.cloudroof.eu hate.you.cloudroof.eu
```

Set root NS hostnames + glue A records:

```bash
API_TOKEN=supersecret bash scripts/set-root-ns.sh \
  cloudroof.eu snail.cloudroof.eu 198.51.100.10 rabbit.cloudroof.eu 198.51.100.11
```

Add AAAA for existing NS hosts (A stays untouched):

```bash
API_TOKEN=supersecret bash scripts/set-ns-aaaa.sh \
  cloudroof.eu snail.cloudroof.eu 2001:db8::10 rabbit.cloudroof.eu 2001:db8::11
```

Park domain and NS per stack mode:

```bash
# dual-stack domain + dual-stack NS
API_TOKEN=supersecret \
PARK_IPV4=198.51.100.50 PARK_IPV6=2001:db8::50 \
NS1_IPV4=198.51.100.10 NS2_IPV4=198.51.100.11 \
NS1_IPV6=2001:db8::10 NS2_IPV6=2001:db8::11 \
bash scripts/park-domain.sh cloudroof.eu dual dual

# ipv4-only parked domain, ipv6-only NS
API_TOKEN=supersecret \
PARK_IPV4=198.51.100.50 \
NS1_IPV6=2001:db8::10 NS2_IPV6=2001:db8::11 \
bash scripts/park-domain.sh cloudroof.eu ipv4 ipv6

# only NS updates (skip parked domain records)
API_TOKEN=supersecret SKIP_DOMAIN=1 NS1_IPV6=2001:db8::10 NS2_IPV6=2001:db8::11 \
bash scripts/park-domain.sh cloudroof.eu ipv4 ipv6
```

## Dashboard

Basic multi-node sync dashboard is available at `cmd/dashboard`.

It lets you:

- Use cookie auth login with role separation:
  - super admin/cloud admin side
  - user side for parking domains and updating `A`/`AAAA`
- Store dashboard data in SQLite (`users`, sessions, endpoints, domain ownership).
- Add DNS API endpoints (`name`, `base URL`, `token`) and fan-out changes to all nodes.
- Assign domains to users; non-admin users can manage only assigned domains.
- Run admin DNS actions (`zone upsert`, record `set/add/remove/delete`, state query).
- Add/remove RRset members (`A`/`AAAA`) for round-robin pools.
- Query all endpoints to view current zones/records per node.

Run:

```bash
go run ./cmd/dashboard
```

Environment variables:

- `DASHBOARD_LISTEN` (default `:8090`)
- `DASHBOARD_DB` (default `dashboard.db`)
- `DASHBOARD_ADMIN_USER` (default `admin`)
- `DASHBOARD_ADMIN_PASSWORD` (default `admin` - change in production)

## Database migrations

- Schema migrations are versioned SQL files in `migrations/*.sql`.
- Migrations run on startup via `goose` (no GORM auto-migrate).
- Optional env override: `MIGRATIONS_DIR` (default `migrations`, container default `/migrations`).

## Deployment

- Runbook: `deployment.md`
- Edge deployment spec: `specs/edge-deployment-spec.md`
- Edge files/templates: `deploy/docker-compose.yml`, `deploy/.env.example`

## Container Pipelines

- DNS server image workflow: `.github/workflows/docker-build.yml`
- Dashboard image workflow: `.github/workflows/docker-build-dashboard.yml`
- Dashboard container Dockerfile: `Dockerfile.dashboard`
