# Copilot Review Instructions

When reviewing code in this repository, prioritize reliability and correctness for an authoritative failover DNS service.

## Project Context

- This service is an authoritative DNS server with HTTP control API, DoH endpoint, peer sync, and SQLite persistence.
- Main goals are: fast failover updates, deterministic behavior across anycast nodes, and safe operations.

## Review Priorities

1. DNS correctness
- Verify authoritative behavior for `A`, `NS`, `SOA`, and `ANY` handling.
- Confirm expected response codes:
  - `NXDOMAIN` for missing names inside managed zones.
  - `REFUSED` for names outside managed zones.
- Ensure FQDN normalization is preserved and comparisons are case-insensitive.
- Ensure SOA fields are coherent with zone data and NS values.

2. Failover safety
- Flag any change that can cause stale data to overwrite newer data.
- Preserve version/serial conflict rules (last-write-wins with stale-write rejection).
- Ensure API writes, sync writes, and persistence writes remain consistent.
- Avoid introducing race conditions in concurrent read/write paths.

3. Auth and security
- Verify control API endpoints remain protected when `API_TOKEN` is set.
- Verify sync endpoint remains protected by `SYNC_TOKEN` (`X-Sync-Token`).
- Flag accidental auth bypass, weak token checks, or missing validation.
- Confirm request parsing enforces strict JSON and reasonable body limits.

4. Persistence and data integrity
- Ensure state mutations are persisted and startup reload restores full state.
- Flag paths where memory may update but persistence can be skipped silently.
- Preserve schema compatibility and migration safety.

5. Operational behavior
- Prefer predictable defaults, explicit errors, and clear logs.
- No hardcoded authority hostnames (`ns1/ns2`) as hidden fallback behavior.
- Keep behavior deterministic across nodes and restarts.

## Testing Expectations

When code changes behavior, request or suggest tests for:

- DNS resolver outcomes (`A`, `NS`, `SOA`, NXDOMAIN, REFUSED).
- Auth checks for API and sync endpoints.
- DoH GET/POST request handling.
- Version conflict handling in store and persistence.
- Persistence roundtrip and restart safety.

Favor table-driven tests where practical.

## Go Standards

- Follow Effective Go conventions and idiomatic Go patterns.
- Keep functions small and focused.
- Return errors instead of panics for recoverable failures.
- Prefer clarity over cleverness.

## Review Style

- Focus comments on correctness, safety, and concrete risk.
- Include actionable fixes, not only problem statements.
- Call out severity clearly (critical, high, medium, low).
