#!/usr/bin/env bash
set -euo pipefail

# Usage:
#   API_TOKEN=... bash scripts/set-root-ns.sh [zone] [ns1] [ns1_ip] [ns2] [ns2_ip]
#
# Example:
#   API_TOKEN=supersecret bash scripts/set-root-ns.sh cloudroof.eu snail.cloudroof.eu 198.51.100.10 rabbit.cloudroof.eu 198.51.100.11

API_BASE="${API_BASE:-http://127.0.0.1:8080}"
API_TOKEN="${API_TOKEN:-}"

ZONE="${1:-${ZONE:-cloudroof.eu}}"
NS1="${2:-${NS1:-snail.cloudroof.eu}}"
NS1_IP="${3:-${NS1_IP:-}}"
NS2="${4:-${NS2:-rabbit.cloudroof.eu}}"
NS2_IP="${5:-${NS2_IP:-}}"

SOA_TTL="${SOA_TTL:-60}"
NS_TTL="${NS_TTL:-60}"

if [[ -z "${API_TOKEN}" ]]; then
  echo "error: API_TOKEN is required" >&2
  exit 1
fi

if [[ -z "${NS1_IP}" || -z "${NS2_IP}" ]]; then
  echo "error: NS1_IP and NS2_IP are required (args 3 and 5, or env vars)" >&2
  exit 1
fi

auth_header=("-H" "Authorization: Bearer ${API_TOKEN}")

echo "==> set authoritative NS for zone ${ZONE}"
curl -fsS -X PUT "${API_BASE}/v1/zones/${ZONE}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"ns\":[\"${NS1}\",\"${NS2}\"],\"soa_ttl\":${SOA_TTL}}"
echo

echo "==> set glue A record ${NS1} -> ${NS1_IP}"
curl -fsS -X PUT "${API_BASE}/v1/records/${NS1}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"type\":\"A\",\"ip\":\"${NS1_IP}\",\"ttl\":${NS_TTL},\"zone\":\"${ZONE}\"}"
echo

echo "==> set glue A record ${NS2} -> ${NS2_IP}"
curl -fsS -X PUT "${API_BASE}/v1/records/${NS2}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"type\":\"A\",\"ip\":\"${NS2_IP}\",\"ttl\":${NS_TTL},\"zone\":\"${ZONE}\"}"
echo

echo "==> done"
