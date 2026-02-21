# dns-server

A minimal authoritative DNS server (`A`, `NS`, `SOA`) with an HTTP control API and peer-to-peer synchronization across anycast nodes.

## What It Does

- Answers DNS queries over UDP/TCP using `github.com/miekg/dns`.
- Keeps active `A` records and zone (`NS`/`SOA`) config in memory.
- Lets you manage records via HTTP API with token authentication.
- Replicates updates to peer nodes through `/v1/sync/event` (for example over VPN).

## Run

```bash
API_TOKEN=supersecret \
DEFAULT_ZONE=example.com \
DEFAULT_NS=ns1.example.com,ns2.example.com \
PEERS=http://10.1.0.2:8080,http://10.1.0.3:8080 \
go run .
```

Important environment variables:

- `API_TOKEN` - control API token (`Authorization: Bearer <token>` or `X-API-Token`)
- `SYNC_TOKEN` - sync endpoint token (`X-Sync-Token`), if empty it falls back to `API_TOKEN`
- `HTTP_LISTEN` - default is `:8080`
- `DNS_UDP_LISTEN` - default is `:53`
- `DNS_TCP_LISTEN` - default is `:53`
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
  -d '{"ns":["ns1.example.com","ns2.example.com"],"soa_ttl":60}'
```

Verify:

```bash
dig @127.0.0.1 app.example.com A +short
dig @127.0.0.1 example.com NS +short
dig @127.0.0.1 example.com SOA +short
```
