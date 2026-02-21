#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 3 ]]; then
  echo "usage: $0 domain.name [ns1] [ns2]" >&2
  exit 1
fi

API_BASE="${API_BASE:-http://127.0.0.1:8080}"
API_TOKEN="${API_TOKEN:-}"
SOA_TTL="${SOA_TTL:-60}"
RECORD_TTL="${RECORD_TTL:-60}"

ZONE_RAW="$1"
NS1_RAW="${2:-}"
NS2_RAW="${3:-}"

if [[ -z "${API_TOKEN}" ]]; then
  echo "error: API_TOKEN is required" >&2
  exit 1
fi

strip_dot() {
  local v
  v="${1#.}"
  v="${v%.}"
  echo "${v}"
}

ZONE="$(strip_dot "${ZONE_RAW}")"
if [[ -z "${ZONE}" ]]; then
  echo "error: invalid zone name" >&2
  exit 1
fi

AUTO_NS=0
if [[ -z "${NS1_RAW}" && -z "${NS2_RAW}" ]]; then
  AUTO_NS=1
  NS1="ns1.${ZONE}"
  NS2="ns2.${ZONE}"
else
  NS1="$(strip_dot "${NS1_RAW:-}")"
  NS2="$(strip_dot "${NS2_RAW:-}")"
  if [[ -z "${NS1}" || -z "${NS2}" ]]; then
    echo "error: provide both ns1 and ns2, or provide none" >&2
    exit 1
  fi
fi

AUTH_HEADER=("-H" "Authorization: Bearer ${API_TOKEN}")

echo "==> create/update zone ${ZONE} with NS ${NS1}, ${NS2}"
curl -fsS -X PUT "${API_BASE}/v1/zones/${ZONE}" \
  "${AUTH_HEADER[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"ns\":[\"${NS1}\",\"${NS2}\"],\"soa_ttl\":${SOA_TTL}}"
echo

create_glue_record() {
  local ns_name="$1"
  local ns_ip="$2"

  if [[ -z "${ns_ip}" ]]; then
    return 0
  fi

  echo "==> create/update glue A ${ns_name} -> ${ns_ip}"
  curl -fsS -X PUT "${API_BASE}/v1/records/${ns_name}" \
    "${AUTH_HEADER[@]}" \
    -H "Content-Type: application/json" \
    -d "{\"ip\":\"${ns_ip}\",\"ttl\":${RECORD_TTL},\"zone\":\"${ZONE}\"}"
  echo
}

if [[ "${AUTO_NS}" -eq 1 ]]; then
  NS1_IP="${NS1_IP:-${NS_IP:-}}"
  NS2_IP="${NS2_IP:-${NS_IP:-}}"

  if [[ -z "${NS1_IP}" || -z "${NS2_IP}" ]]; then
    echo "warning: auto NS hostnames created, but NS1_IP/NS2_IP (or NS_IP) not set; skipping glue A records" >&2
  else
    create_glue_record "${NS1}" "${NS1_IP}"
    create_glue_record "${NS2}" "${NS2_IP}"
  fi
else
  if [[ "${CREATE_GLUE:-0}" == "1" ]]; then
    create_glue_record "${NS1}" "${NS1_IP:-${NS_IP:-}}"
    create_glue_record "${NS2}" "${NS2_IP:-${NS_IP:-}}"
  fi
fi

echo "==> done"
