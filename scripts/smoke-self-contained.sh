#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

HTTP_PORT="${HTTP_PORT:-18080}"
DNS_PORT="${DNS_PORT:-15353}"
ZONE="${ZONE:-cloudroof.eu}"
NS1="${NS1:-snail.cloudroof.eu}"
NS2="${NS2:-rabbit.cloudroof.eu}"

API_TOKEN="${API_TOKEN:-smoke-token}"
DB_PATH="${DB_PATH:-${ROOT_DIR}/.tmp-smoke.db}"

A_NS1="${A_NS1:-198.51.100.10}"
A_NS2="${A_NS2:-198.51.100.11}"

cleanup() {
  if [[ -n "${PID:-}" ]]; then
    kill "${PID}" >/dev/null 2>&1 || true
    wait "${PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

rm -f "${DB_PATH}"

echo "==> starting dns-server for self-contained smoke"
(
  cd "${ROOT_DIR}"
  API_TOKEN="${API_TOKEN}" \
  SYNC_TOKEN="${API_TOKEN}" \
  HTTP_LISTEN=":${HTTP_PORT}" \
  DNS_UDP_LISTEN="127.0.0.1:${DNS_PORT}" \
  DNS_TCP_LISTEN="127.0.0.1:${DNS_PORT}" \
  DB_PATH="${DB_PATH}" \
  MIGRATIONS_DIR="migrations" \
  go run .
) >/tmp/dns-server-smoke.log 2>&1 &
PID=$!

ready=0
for _ in {1..60}; do
  if curl -fsS "http://127.0.0.1:${HTTP_PORT}/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "${PID}" >/dev/null 2>&1; then
    echo "error: dns-server exited early; see /tmp/dns-server-smoke.log" >&2
    sed -n '1,200p' /tmp/dns-server-smoke.log >&2 || true
    exit 1
  fi
  sleep 0.25
done

if [[ "${ready}" != "1" ]]; then
  echo "error: dns-server did not become ready on 127.0.0.1:${HTTP_PORT}" >&2
  sed -n '1,200p' /tmp/dns-server-smoke.log >&2 || true
  exit 1
fi

echo "==> bootstrap zone and NS records"
API_BASE="http://127.0.0.1:${HTTP_PORT}" \
API_TOKEN="${API_TOKEN}" \
bash "${ROOT_DIR}/scripts/set-root-ns.sh" "${ZONE}" "${NS1}" "${A_NS1}" "${NS2}" "${A_NS2}"

echo "==> run full smoke suite"
API_BASE="http://127.0.0.1:${HTTP_PORT}" \
API_TOKEN="${API_TOKEN}" \
DNS_HOST="127.0.0.1" \
DNS_PORT="${DNS_PORT}" \
ZONE="${ZONE}" \
NS1="${NS1}" \
NS2="${NS2}" \
DOH_URL="http://127.0.0.1:${HTTP_PORT}/dns-query" \
bash "${ROOT_DIR}/scripts/smoke-all.sh"

echo "self-contained smoke passed"
