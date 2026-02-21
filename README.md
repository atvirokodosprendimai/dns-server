# dns-server

Minimalus autoritetingas DNS (`A`, `NS`, `SOA`) su valdymu per HTTP API ir peer-to-peer sinchronizacija tarp anycast mazgu.

## Ka daro

- Atsako i DNS uzklausas per UDP/TCP su `github.com/miekg/dns`.
- Laiko aktyvius `A` ir zonu (`NS`/`SOA`) nustatymus RAM.
- Leidzia keisti irasus per HTTP API su token autentifikacija.
- Replikuoja pakeitimus i kitus mazgus per `/v1/sync/event` (pvz., per VPN).

## Paleidimas

```bash
API_TOKEN=supersecret \
DEFAULT_ZONE=example.com \
DEFAULT_NS=ns1.example.com,ns2.example.com \
PEERS=http://10.1.0.2:8080,http://10.1.0.3:8080 \
go run .
```

Svarbiausi ENV:

- `API_TOKEN` - valdymo API tokenas (`Authorization: Bearer <token>` arba `X-API-Token`)
- `SYNC_TOKEN` - sync endpoint tokenas (`X-Sync-Token`), jei tuscias -> ima `API_TOKEN`
- `HTTP_LISTEN` - numatytas `:8080`
- `DNS_UDP_LISTEN` - numatytas `:53`
- `DNS_TCP_LISTEN` - numatytas `:53`
- `PEERS` - kableliais atskirti peer URL (be kelio)
- `DEFAULT_ZONE` - neprivaloma default zona
- `DEFAULT_NS` - neprivalomas default NS sarasas
- `DEFAULT_TTL` - numatytas `20`

## API pavyzdziai

Sukurti/atnaujinti `A` irasa:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/records/app.example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"ip":"203.0.113.10","ttl":15}'
```

Istrinti irasa:

```bash
curl -sS -X DELETE "http://127.0.0.1:8080/v1/records/app.example.com" \
  -H "Authorization: Bearer supersecret"
```

Atnaujinti zonos NS:

```bash
curl -sS -X PUT "http://127.0.0.1:8080/v1/zones/example.com" \
  -H "Authorization: Bearer supersecret" \
  -H "Content-Type: application/json" \
  -d '{"ns":["ns1.example.com","ns2.example.com"],"soa_ttl":60}'
```

Patikra:

```bash
dig @127.0.0.1 app.example.com A +short
dig @127.0.0.1 example.com NS +short
dig @127.0.0.1 example.com SOA +short
```
