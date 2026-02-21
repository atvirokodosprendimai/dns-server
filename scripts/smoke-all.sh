#!/usr/bin/env bash
set -euo pipefail

# Comprehensive smoke test for DNS/API behavior:
# - NS, SOA
# - A / AAAA RRset add/remove (round-robin pool)
# - TXT
# - CNAME
# - MX
# - DoH endpoint reachability
# - optional ICMP ping checks

API_BASE="${API_BASE:-http://127.0.0.1:8080}"
API_TOKEN="${API_TOKEN:-}"
DNS_HOST="${DNS_HOST:-127.0.0.1}"
DNS_PORT="${DNS_PORT:-53}"
ZONE="${ZONE:-cloudroof.eu}"
NS1="${NS1:-snail.cloudroof.eu}"
NS2="${NS2:-rabbit.cloudroof.eu}"
TTL="${TTL:-60}"
DOH_URL="${DOH_URL:-${API_BASE}/dns-query}"

# test data defaults (override if needed)
A1="${A1:-198.51.100.101}"
A2="${A2:-198.51.100.102}"
AAAA1="${AAAA1:-2001:db8::101}"
AAAA2="${AAAA2:-2001:db8::102}"
MX_TARGET1="${MX_TARGET1:-mail1.${ZONE}}"
MX_TARGET2="${MX_TARGET2:-mail2.${ZONE}}"
TXT_VALUE="${TXT_VALUE:-smoke-verification=ok}"

PING_CHECK="${PING_CHECK:-0}"

if [[ -z "${API_TOKEN}" ]]; then
  echo "error: API_TOKEN is required" >&2
  exit 1
fi

auth_header=("-H" "Authorization: Bearer ${API_TOKEN}")

ts="$(date +%s)"
POOL_HOST="rr-${ts}.${ZONE}"
IPV6_POOL_HOST="rr6-${ts}.${ZONE}"
TXT_HOST="txt-${ts}.${ZONE}"
CNAME_HOST="www-${ts}.${ZONE}"
MX_HOST="mx-${ts}.${ZONE}"

pass() { echo "[PASS] $*"; }
fail() { echo "[FAIL] $*" >&2; exit 1; }

api_put_record() {
  local name="$1"
  local body="$2"
  curl -fsS -X PUT "${API_BASE}/v1/records/${name}" \
    "${auth_header[@]}" -H "Content-Type: application/json" -d "${body}" >/dev/null
}

api_add_record() {
  local name="$1"
  local body="$2"
  curl -fsS -X POST "${API_BASE}/v1/records/${name}/add" \
    "${auth_header[@]}" -H "Content-Type: application/json" -d "${body}" >/dev/null
}

api_remove_record() {
  local name="$1"
  local body="$2"
  curl -fsS -X POST "${API_BASE}/v1/records/${name}/remove" \
    "${auth_header[@]}" -H "Content-Type: application/json" -d "${body}" >/dev/null
}

dig_short() {
  local name="$1"
  local rtype="$2"
  dig @"${DNS_HOST}" -p "${DNS_PORT}" "${name}" "${rtype}" +short
}

echo "==> zone baseline checks"
ns_output="$(dig_short "${ZONE}" NS)"
[[ "${ns_output}" == *"${NS1}."* || "${ns_output}" == *"${NS1}"* ]] || fail "NS1 missing in NS set"
[[ "${ns_output}" == *"${NS2}."* || "${ns_output}" == *"${NS2}"* ]] || fail "NS2 missing in NS set"
pass "zone NS contains expected NS1/NS2"

soa_output="$(dig_short "${ZONE}" SOA)"
[[ -n "${soa_output}" ]] || fail "SOA response is empty"
pass "zone SOA responds"

echo "==> A RRset add/remove"
api_add_record "${POOL_HOST}" "{\"type\":\"A\",\"ip\":\"${A1}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
api_add_record "${POOL_HOST}" "{\"type\":\"A\",\"ip\":\"${A2}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
a_out="$(dig_short "${POOL_HOST}" A)"
[[ "${a_out}" == *"${A1}"* ]] || fail "A RRset missing ${A1}"
[[ "${a_out}" == *"${A2}"* ]] || fail "A RRset missing ${A2}"
pass "A RRset add works"

api_remove_record "${POOL_HOST}" "{\"type\":\"A\",\"ip\":\"${A2}\",\"zone\":\"${ZONE}\"}"
a_out_after="$(dig_short "${POOL_HOST}" A)"
[[ "${a_out_after}" == *"${A1}"* ]] || fail "A RRset remove removed wrong value"
[[ "${a_out_after}" != *"${A2}"* ]] || fail "A RRset remove failed for ${A2}"
pass "A RRset remove works"

echo "==> AAAA RRset add/remove"
api_add_record "${IPV6_POOL_HOST}" "{\"type\":\"AAAA\",\"ip\":\"${AAAA1}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
api_add_record "${IPV6_POOL_HOST}" "{\"type\":\"AAAA\",\"ip\":\"${AAAA2}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
aaaa_out="$(dig_short "${IPV6_POOL_HOST}" AAAA)"
[[ "${aaaa_out}" == *"${AAAA1}"* ]] || fail "AAAA RRset missing ${AAAA1}"
[[ "${aaaa_out}" == *"${AAAA2}"* ]] || fail "AAAA RRset missing ${AAAA2}"
pass "AAAA RRset add works"

api_remove_record "${IPV6_POOL_HOST}" "{\"type\":\"AAAA\",\"ip\":\"${AAAA2}\",\"zone\":\"${ZONE}\"}"
aaaa_out_after="$(dig_short "${IPV6_POOL_HOST}" AAAA)"
[[ "${aaaa_out_after}" == *"${AAAA1}"* ]] || fail "AAAA RRset remove removed wrong value"
[[ "${aaaa_out_after}" != *"${AAAA2}"* ]] || fail "AAAA RRset remove failed for ${AAAA2}"
pass "AAAA RRset remove works"

echo "==> TXT"
api_put_record "${TXT_HOST}" "{\"type\":\"TXT\",\"text\":\"${TXT_VALUE}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
txt_out="$(dig_short "${TXT_HOST}" TXT)"
[[ "${txt_out}" == *"${TXT_VALUE}"* ]] || fail "TXT value mismatch"
pass "TXT works"

echo "==> CNAME"
api_put_record "${CNAME_HOST}" "{\"type\":\"CNAME\",\"target\":\"${POOL_HOST}\",\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
cname_out="$(dig_short "${CNAME_HOST}" CNAME)"
[[ "${cname_out}" == *"${POOL_HOST}."* || "${cname_out}" == *"${POOL_HOST}"* ]] || fail "CNAME target mismatch"
pass "CNAME works"

echo "==> MX"
api_add_record "${MX_HOST}" "{\"type\":\"MX\",\"target\":\"${MX_TARGET1}\",\"priority\":10,\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
api_add_record "${MX_HOST}" "{\"type\":\"MX\",\"target\":\"${MX_TARGET2}\",\"priority\":20,\"ttl\":${TTL},\"zone\":\"${ZONE}\"}"
mx_out="$(dig_short "${MX_HOST}" MX)"
[[ "${mx_out}" == *"10 ${MX_TARGET1}."* || "${mx_out}" == *"10 ${MX_TARGET1}"* ]] || fail "MX preference 10 target missing"
[[ "${mx_out}" == *"20 ${MX_TARGET2}."* || "${mx_out}" == *"20 ${MX_TARGET2}"* ]] || fail "MX preference 20 target missing"
pass "MX works"

echo "==> DoH endpoint reachability"
doh_status="$(curl -s -o /dev/null -w "%{http_code}" -X POST "${DOH_URL}" --data-binary "")"
[[ "${doh_status}" == "400" || "${doh_status}" == "200" ]] || fail "unexpected DoH status ${doh_status}"
pass "DoH endpoint reachable"

if [[ "${PING_CHECK}" == "1" ]]; then
  echo "==> optional ping checks"
  ping -c 1 -W 1 "${A1}" >/dev/null 2>&1 || fail "ping failed for ${A1}"
  ping -c 1 -W 1 "${A2}" >/dev/null 2>&1 || fail "ping failed for ${A2}"
  pass "ping checks passed"
fi

echo "all smoke checks passed"
