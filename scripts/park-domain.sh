#!/usr/bin/env bash
set -euo pipefail

# Park a zone and configure root NS glue for IPv4/IPv6/dual-stack.
#
# Modes:
#   ipv4 | ipv6 | dual
#
# Usage:
#   API_TOKEN=... bash scripts/park-domain.sh [zone] [domain_mode] [ns_mode]
#
# Examples:
#   # domain + NS dual-stack
#   API_TOKEN=... PARK_IPV4=198.51.100.50 PARK_IPV6=2001:db8::50 \
#   NS1_IPV4=198.51.100.10 NS2_IPV4=198.51.100.11 \
#   NS1_IPV6=2001:db8::10 NS2_IPV6=2001:db8::11 \
#   bash scripts/park-domain.sh cloudroof.eu dual dual
#
#   # IPv4-only parked domain, dual-stack NS
#   API_TOKEN=... PARK_IPV4=198.51.100.50 \
#   NS1_IPV4=198.51.100.10 NS2_IPV4=198.51.100.11 \
#   NS1_IPV6=2001:db8::10 NS2_IPV6=2001:db8::11 \
#   bash scripts/park-domain.sh cloudroof.eu ipv4 dual
#
#   # only add AAAA for existing NS records (keep existing A)
#   API_TOKEN=... SKIP_DOMAIN=1 NS1_IPV6=2001:db8::10 NS2_IPV6=2001:db8::11 \
#   bash scripts/park-domain.sh cloudroof.eu ipv4 ipv6

API_BASE="${API_BASE:-http://127.0.0.1:8080}"
API_TOKEN="${API_TOKEN:-}"

ZONE="${1:-${ZONE:-cloudroof.eu}}"
DOMAIN_MODE="${2:-${DOMAIN_MODE:-dual}}"
NS_MODE="${3:-${NS_MODE:-dual}}"

NS1="${NS1:-snail.${ZONE}}"
NS2="${NS2:-rabbit.${ZONE}}"

PARK_HOST="${PARK_HOST:-${ZONE}}"
PARK_IPV4="${PARK_IPV4:-}"
PARK_IPV6="${PARK_IPV6:-}"

NS1_IPV4="${NS1_IPV4:-}"
NS2_IPV4="${NS2_IPV4:-}"
NS1_IPV6="${NS1_IPV6:-}"
NS2_IPV6="${NS2_IPV6:-}"

SOA_TTL="${SOA_TTL:-60}"
TTL="${TTL:-60}"

SKIP_DOMAIN="${SKIP_DOMAIN:-0}"
PRUNE_OTHER_TYPES="${PRUNE_OTHER_TYPES:-0}"

if [[ -z "${API_TOKEN}" ]]; then
  echo "error: API_TOKEN is required" >&2
  exit 1
fi

is_mode() {
  [[ "$1" == "ipv4" || "$1" == "ipv6" || "$1" == "dual" ]]
}

if ! is_mode "${DOMAIN_MODE}"; then
  echo "error: domain_mode must be ipv4|ipv6|dual" >&2
  exit 1
fi
if ! is_mode "${NS_MODE}"; then
  echo "error: ns_mode must be ipv4|ipv6|dual" >&2
  exit 1
fi

auth_header=("-H" "Authorization: Bearer ${API_TOKEN}")

upsert_record() {
  local name="$1"
  local rtype="$2"
  local value="$3"

  curl -fsS -X PUT "${API_BASE}/v1/records/${name}" \
    "${auth_header[@]}" \
    -H "Content-Type: application/json" \
    -d "{\"type\":\"${rtype}\",\"ip\":\"${value}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}" >/dev/null
}

delete_type() {
  local name="$1"
  local rtype="$2"

  curl -fsS -X DELETE "${API_BASE}/v1/records/${name}?type=${rtype}" \
    "${auth_header[@]}" >/dev/null
}

require_non_empty() {
  local var_name="$1"
  local value="$2"
  if [[ -z "${value}" ]]; then
    echo "error: ${var_name} is required for selected mode" >&2
    exit 1
  fi
}

echo "==> set authoritative NS for zone ${ZONE}"
curl -fsS -X PUT "${API_BASE}/v1/zones/${ZONE}" \
  "${auth_header[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"ns\":[\"${NS1}\",\"${NS2}\"],\"soa_ttl\":${SOA_TTL}}"
echo

echo "==> configure NS glue (${NS_MODE})"
if [[ "${NS_MODE}" == "ipv4" || "${NS_MODE}" == "dual" ]]; then
  require_non_empty "NS1_IPV4" "${NS1_IPV4}"
  require_non_empty "NS2_IPV4" "${NS2_IPV4}"
  upsert_record "${NS1}" "A" "${NS1_IPV4}"
  upsert_record "${NS2}" "A" "${NS2_IPV4}"
fi
if [[ "${NS_MODE}" == "ipv6" || "${NS_MODE}" == "dual" ]]; then
  require_non_empty "NS1_IPV6" "${NS1_IPV6}"
  require_non_empty "NS2_IPV6" "${NS2_IPV6}"
  upsert_record "${NS1}" "AAAA" "${NS1_IPV6}"
  upsert_record "${NS2}" "AAAA" "${NS2_IPV6}"
fi

if [[ "${PRUNE_OTHER_TYPES}" == "1" ]]; then
  if [[ "${NS_MODE}" == "ipv4" ]]; then
    delete_type "${NS1}" "AAAA"
    delete_type "${NS2}" "AAAA"
  elif [[ "${NS_MODE}" == "ipv6" ]]; then
    delete_type "${NS1}" "A"
    delete_type "${NS2}" "A"
  fi
fi

if [[ "${SKIP_DOMAIN}" == "1" ]]; then
  echo "==> skipped parked domain A/AAAA update"
  echo "done"
  exit 0
fi

echo "==> configure parked domain ${PARK_HOST} (${DOMAIN_MODE})"
if [[ "${DOMAIN_MODE}" == "ipv4" || "${DOMAIN_MODE}" == "dual" ]]; then
  require_non_empty "PARK_IPV4" "${PARK_IPV4}"
  upsert_record "${PARK_HOST}" "A" "${PARK_IPV4}"
fi
if [[ "${DOMAIN_MODE}" == "ipv6" || "${DOMAIN_MODE}" == "dual" ]]; then
  require_non_empty "PARK_IPV6" "${PARK_IPV6}"
  upsert_record "${PARK_HOST}" "AAAA" "${PARK_IPV6}"
fi

if [[ "${PRUNE_OTHER_TYPES}" == "1" ]]; then
  if [[ "${DOMAIN_MODE}" == "ipv4" ]]; then
    delete_type "${PARK_HOST}" "AAAA"
  elif [[ "${DOMAIN_MODE}" == "ipv6" ]]; then
    delete_type "${PARK_HOST}" "A"
  fi
fi

echo "done"
