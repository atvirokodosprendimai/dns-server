#!/usr/bin/env bash
set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:8080}"
API_TOKEN="${API_TOKEN:-}"

ZONE="${ZONE:-cloudroof.eu}"
NS1="${NS1:-love.me.cloudroof.eu}"
NS2="${NS2:-hate.you.cloudroof.eu}"
SOA_TTL="${SOA_TTL:-60}"

APP_HOST="${APP_HOST:-app.${ZONE}}"
APP_IP="${APP_IP:-203.0.113.10}"
API_HOSTNAME="${API_HOSTNAME:-api.${ZONE}}"
API_IP_ADDR="${API_IP_ADDR:-203.0.113.20}"
RECORD_TTL="${RECORD_TTL:-20}"

if [[ -z "${API_TOKEN}" ]]; then
  echo "error: API_TOKEN is required" >&2
  exit 1
fi

auth_header=("-H" "Authorization: Bearer ${API_TOKEN}")

echo "==> creating/updating zone ${ZONE}"
curl -fsS -X PUT "${API_BASE}/v1/zones/${ZONE}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"ns\":[\"${NS1}\",\"${NS2}\"],\"soa_ttl\":${SOA_TTL}}"
echo

echo "==> creating/updating A record ${APP_HOST} -> ${APP_IP}"
curl -fsS -X PUT "${API_BASE}/v1/records/${APP_HOST}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"ip\":\"${APP_IP}\",\"ttl\":${RECORD_TTL},\"zone\":\"${ZONE}\"}"
echo

echo "==> creating/updating A record ${API_HOSTNAME} -> ${API_IP_ADDR}"
curl -fsS -X PUT "${API_BASE}/v1/records/${API_HOSTNAME}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"ip\":\"${API_IP_ADDR}\",\"ttl\":${RECORD_TTL},\"zone\":\"${ZONE}\"}"
echo

echo "==> zone list"
curl -fsS "${API_BASE}/v1/zones" "${auth_header[@]}"
echo

echo "==> record list"
curl -fsS "${API_BASE}/v1/records" "${auth_header[@]}"
echo

echo "bootstrap complete"
