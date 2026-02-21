# DNS Server Specification

## 1. Purpose

This service is an authoritative DNS control plane and data plane for fast failover.

It is designed for:

- Anycast node deployments.
- Fast A-record switching.
- Simple API-based control.
- Peer replication over private networks (for example VPN mesh).

## 2. Design Principles

- Single-process runtime for operational simplicity.
- In-memory read path for low-latency DNS responses.
- Durable state in SQLite (pure Go driver, no CGO).
- Event replication with monotonic version conflict handling.
- Explicit root/authoritative NS hostnames (no hardcoded `ns1/ns2` fallback).

## 3. Runtime Architecture

The process runs three network services:

- DNS UDP listener.
- DNS TCP listener.
- HTTP listener for control API + DoH + sync ingestion.

Modules:

- `main.go`: process boot and lifecycle.
- `config.go`: environment config loading.
- `store.go`: in-memory state and conflict guards.
- `persistence.go`: SQLite persistence via GORM.
- `dns.go`: authoritative DNS resolver logic.
- `http.go`: chi router, API handlers, DoH, sync.
- `util.go`: normalization, JSON I/O, auth helpers.
- `types.go`: internal types and models.

## 4. Data Model

### 4.1 Record (`A`/`AAAA`/`TXT`/`CNAME`/`MX`)

- `name` (FQDN, normalized lower-case)
- `type` (`A`, `AAAA`, `TXT`, `CNAME`, or `MX`)
- `ip` (IPv4 for `A`, IPv6 for `AAAA`)
- `text` (for `TXT`)
- `target` (for `CNAME`)
- `priority` (for `MX`)
- `ttl` (uint32)
- `zone` (FQDN)
- `updated_at` (UTC)
- `version` (int64, event ordering)
- `source` (origin node id)

### 4.2 Zone

- `zone` (FQDN)
- `ns` (list of authoritative nameserver hostnames)
- `soa_ttl` (uint32)
- `serial` (uint32)
- `updated_at` (UTC)

### 4.3 Sync Event

- `origin_node`
- `op` in `{set,delete,zone}`
- `version`
- `event_time`
- optional payload fields depending on `op`

## 5. DNS Behavior Specification

### 5.1 Supported Types

- `A`
- `AAAA`
- `TXT`
- `CNAME`
- `MX`
- `NS`
- `SOA`
- `ANY` (returns available `A`/`AAAA`/`TXT`/`CNAME`/`MX` behavior)

### 5.2 Response Rules

- If matching `A`/`AAAA`/`TXT`/`CNAME`/`MX` record exists: return authoritative answer.
- For multiple `A`/`AAAA` records, answer order is shuffled per response to improve load distribution.
- If the name exists but requested type does not exist, return `NOERROR` with empty answer (NODATA).
- If queried name is inside a managed zone but no matching record: return `NXDOMAIN` and zone SOA in authority section.
- If queried name is outside managed zones: return `REFUSED`.

### 5.3 SOA Construction

- `MNAME` is the first configured NS hostname for zone.
- If zone NS list is empty (misconfiguration edge case), fallback `MNAME` is zone apex FQDN.

## 6. HTTP Control API Specification

### 6.1 Auth

- API endpoints under `/v1/*` require `API_TOKEN` if configured.
- Accepted auth headers:
  - `Authorization: Bearer <token>`
  - `X-API-Token: <token>`

### 6.2 Endpoints

- `GET /healthz`
- `GET /v1/records`
- `PUT /v1/records/{name}`
- `DELETE /v1/records/{name}`
- `GET /v1/zones`
- `PUT /v1/zones/{zone}`

### 6.3 Zone NS Requirement

The service must never silently hardcode default NS names.

Zone NS can come from:

- Explicit `ns` provided via `PUT /v1/zones/{zone}`.
- Global `DEFAULT_NS` configured by operator.
- Existing zone NS already present in memory/storage.

If no NS source exists for zone creation/update, API returns `400`.

## 7. DoH Specification

Endpoint: `/dns-query`

- `GET` with `dns=` base64url wire message.
- `POST` with raw DNS wire message body (`application/dns-message`).

Response:

- `200` with `application/dns-message` on success.
- Proper `4xx/5xx` on invalid input/encoding.

## 8. Persistence Specification

Storage: SQLite file path from `DB_PATH`.

Schema management:

- Versioned SQL migrations in `migrations/*.sql`.
- Applied at startup using `goose`.
- GORM automatic migrations are not used.

Rules:

- On startup, load all zones then records into memory.
- Each accepted state mutation persists immediately.
- Version guards prevent stale writes from overwriting newer data.
- Schema managed with GORM automigration.

## 9. Sync Specification

Ingress endpoint: `POST /v1/sync/event`

Auth:

- `X-Sync-Token` must match `SYNC_TOKEN` when configured.

Conflict behavior:

- Record and zone updates are last-write-wins by version/serial.
- Stale events are ignored.

Egress behavior:

- Local mutating operations may propagate events to all `PEERS`.
- Peer requests are async with short timeout.

## 10. Configuration Specification

Required for production safety:

- `API_TOKEN`
- `SYNC_TOKEN` (or fallback to API token)
- `DEFAULT_NS` set to real authoritative hostnames, for example:
  - `love.me.cloudroof.eu`
  - `hate.you.cloudroof.eu`

Key defaults:

- `HTTP_LISTEN=:8080`
- `DNS_UDP_LISTEN=:53`
- `DNS_TCP_LISTEN=:53`
- `DB_PATH=dns.db`
- `DEFAULT_TTL=20`

## 11. Why It Works This Way

- In-memory lookup provides very low read latency for DNS traffic.
- SQLite durability avoids data loss after restart while keeping operations simple.
- API + event replication gives deterministic control over many anycast nodes.
- No hardcoded NS defaults avoids accidental incorrect authority branding.

## 12. Unit Test Specification

Current test coverage target:

- Config parsing, defaults, and NS behavior.
- Utility helpers (normalization, token handling, JSON strictness).
- Store semantics (version guards, longest-zone matching).
- DNS resolver behavior (`A`, `AAAA`, `TXT`, `NXDOMAIN`, `REFUSED`, NODATA).
- HTTP auth and API flow.
- DoH `GET` and `POST` flow.
- Persistence roundtrip and stale-write protection.

Test files:

- `config_test.go`
- `util_test.go`
- `store_test.go`
- `dns_test.go`
- `http_test.go`
- `persistence_test.go`
- `testhelpers_test.go`

Execution:

```bash
go test ./...
```

## 13. Future Conformance Work

- Add explicit RFC-level conformance tests for wire-format edge cases.
- Add table-driven tests for every API error branch.
- Add fuzz tests for DoH parser and JSON decoder boundaries.
