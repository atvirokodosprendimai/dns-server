#!/usr/bin/env bash
set -euo pipefail

DNS_HOST="${DNS_HOST:-127.0.0.1}"
DNS_PORT="${DNS_PORT:-53}"
ZONE="${ZONE:-cloudroof.eu}"
NS1="${NS1:-love.me.cloudroof.eu}"
NS2="${NS2:-hate.you.cloudroof.eu}"
APP_HOST="${APP_HOST:-app.${ZONE}}"
DOH_URL="${DOH_URL:-http://127.0.0.1:8080/dns-query}"

echo "==> DNS A"
dig @"${DNS_HOST}" -p "${DNS_PORT}" "${APP_HOST}" A +short

echo "==> DNS NS"
dig @"${DNS_HOST}" -p "${DNS_PORT}" "${ZONE}" NS +short

echo "==> DNS SOA"
dig @"${DNS_HOST}" -p "${DNS_PORT}" "${ZONE}" SOA +short

echo "==> validating expected NS hostnames"
ns_output="$(dig @"${DNS_HOST}" -p "${DNS_PORT}" "${ZONE}" NS +short)"
if [[ "${ns_output}" != *"${NS1}."* && "${ns_output}" != *"${NS1}"* ]]; then
  echo "error: expected NS ${NS1} not found" >&2
  exit 1
fi
if [[ "${ns_output}" != *"${NS2}."* && "${ns_output}" != *"${NS2}"* ]]; then
  echo "error: expected NS ${NS2} not found" >&2
  exit 1
fi

echo "==> DoH endpoint reachability"
status_code="$(curl -s -o /dev/null -w "%{http_code}" -X POST "${DOH_URL}" --data-binary "")"
if [[ "${status_code}" != "400" && "${status_code}" != "200" ]]; then
  echo "error: unexpected DoH endpoint status ${status_code}" >&2
  exit 1
fi

echo "smoke test complete"
