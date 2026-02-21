#!/usr/bin/env bash
set -euo pipefail

# Add/update AAAA glue records for NS hosts.
# Keeps existing A records untouched.
#
# Usage:
#   API_TOKEN=... bash scripts/set-ns-aaaa.sh [zone] [ns1] [ns1_ipv6] [ns2] [ns2_ipv6]
#
# Example:
#   API_TOKEN=supersecret bash scripts/set-ns-aaaa.sh \
#     cloudroof.eu snail.cloudroof.eu 2001:db8::10 rabbit.cloudroof.eu 2001:db8::11

API_BASE="${API_BASE:-http://127.0.0.1:8080}"
API_TOKEN="${API_TOKEN:-}"

ZONE="${1:-${ZONE:-cloudroof.eu}}"
NS1="${2:-${NS1:-snail.cloudroof.eu}}"
NS1_IPV6="${3:-${NS1_IPV6:-}}"
NS2="${4:-${NS2:-rabbit.cloudroof.eu}}"
NS2_IPV6="${5:-${NS2_IPV6:-}}"

TTL="${TTL:-60}"

if [[ -z "${API_TOKEN}" ]]; then
  echo "error: API_TOKEN is required" >&2
  exit 1
fi
if [[ -z "${NS1_IPV6}" || -z "${NS2_IPV6}" ]]; then
  echo "error: NS1_IPV6 and NS2_IPV6 are required" >&2
  exit 1
fi

auth_header=("-H" "Authorization: Bearer ${API_TOKEN}")

echo "==> set AAAA glue ${NS1} -> ${NS1_IPV6}"
curl -fsS -X PUT "${API_BASE}/v1/records/${NS1}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"type\":\"AAAA\",\"ip\":\"${NS1_IPV6}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
echo

echo "==> set AAAA glue ${NS2} -> ${NS2_IPV6}"
curl -fsS -X PUT "${API_BASE}/v1/records/${NS2}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"type\":\"AAAA\",\"ip\":\"${NS2_IPV6}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
echo

echo "done"
