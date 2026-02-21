# dns-server

A minimal authoritative DNS server (`A`, `AAAA`, `TXT`, `NS`, `SOA`) with an HTTP control API and peer-to-peer synchronization across anycast nodes.

## What It Does

- Answers DNS queries over UDP/TCP using `github.com/miekg/dns`.
- Supports DNS over HTTPS (DoH) at `/dns-query`.
- Keeps active `A`/`AAAA`/`TXT` records and zone (`NS`/`SOA`) config in memory.
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
- `scripts/bootstrap-local-db.sh` - starts local server, bootstraps DB, runs smoke checks.
- `scripts/add-domain.sh` - adds a zone via API: `domain [ns1] [ns2]`; auto-generates NS hostnames if omitted.
- `scripts/set-root-ns.sh` - sets zone NS pair and creates glue A records (for example `snail.cloudroof.eu` and `rabbit.cloudroof.eu`).

Example:

```bash
bash scripts/bootstrap-local-db.sh
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
