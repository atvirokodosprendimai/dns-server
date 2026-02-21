#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

HTTP_LISTEN="${HTTP_LISTEN:-:18180}"
DNS_UDP_LISTEN="${DNS_UDP_LISTEN:-:15453}"
DNS_TCP_LISTEN="${DNS_TCP_LISTEN:-:15453}"
DB_PATH="${DB_PATH:-${ROOT_DIR}/dns-bootstrap.db}"

API_TOKEN="${API_TOKEN:-bootstrap-token}"
ZONE="${ZONE:-cloudroof.eu}"
DEFAULT_NS="${DEFAULT_NS:-love.me.cloudroof.eu,hate.you.cloudroof.eu}"

API_BASE="http://127.0.0.1:${HTTP_LISTEN#:}"
DNS_PORT="${DNS_UDP_LISTEN#:}"

echo "==> starting dns-server"
(
  cd "${ROOT_DIR}"
  HTTP_LISTEN="${HTTP_LISTEN}" \
  DNS_UDP_LISTEN="${DNS_UDP_LISTEN}" \
  DNS_TCP_LISTEN="${DNS_TCP_LISTEN}" \
  DB_PATH="${DB_PATH}" \
  API_TOKEN="${API_TOKEN}" \
  DEFAULT_ZONE="${ZONE}" \
  DEFAULT_NS="${DEFAULT_NS}" \
  go run .
) >/tmp/dns-server-bootstrap.log 2>&1 &
PID=$!

cleanup() {
  kill "${PID}" >/dev/null 2>&1 || true
  wait "${PID}" 2>/dev/null || true
}
trap cleanup EXIT

sleep 2

for _ in {1..20}; do
  if curl -fsS "${API_BASE}/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "${PID}" >/dev/null 2>&1; then
    echo "error: dns-server exited early; see /tmp/dns-server-bootstrap.log" >&2
    exit 1
  fi
  sleep 0.2
done

echo "==> bootstrapping DB through API"
API_BASE="${API_BASE}" API_TOKEN="${API_TOKEN}" ZONE="${ZONE}" \
  NS1="love.me.cloudroof.eu" NS2="hate.you.cloudroof.eu" \
  bash "${ROOT_DIR}/scripts/bootstrap-db.sh"

echo "==> running smoke checks"
DNS_HOST="127.0.0.1" DNS_PORT="${DNS_PORT}" ZONE="${ZONE}" \
  NS1="love.me.cloudroof.eu" NS2="hate.you.cloudroof.eu" \
  DOH_URL="${API_BASE}/dns-query" \
  bash "${ROOT_DIR}/scripts/smoke-test.sh"

echo "done. sqlite db at ${DB_PATH}"
