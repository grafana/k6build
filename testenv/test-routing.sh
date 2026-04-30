#!/usr/bin/env bash
# test-routing.sh — verify k6build catalog routing and end-to-end builds.
#
# Requires the server to be running (./start-server.sh in another terminal).
#
# Usage:
#   ./test-routing.sh [BUILD_PORT]   default port: 3000
#
# What it tests:
#   1. Routing — /resolve checks that v1 and v2 requests hit the correct catalog
#   2. Build   — /build + download for both v1 and v2, verifies the binary runs

set -euo pipefail

BUILD_PORT="${1:-${BUILD_PORT:-3000}}"
BASE_URL="http://localhost:${BUILD_PORT}"
PLATFORM="${PLATFORM:-$(go env GOOS)/$(go env GOARCH)}"
OUT_DIR="${OUT_DIR:-/tmp/k6build-testenv/binaries}"
PASS=0
FAIL=0

mkdir -p "${OUT_DIR}"

# ── helpers ──────────────────────────────────────────────────────────────────

check_resolve() {
    local description="$1"
    local k6_mod_path="$2"
    local expected_k6_version="$3"

    local body
    if [ -n "${k6_mod_path}" ]; then
        body="{\"k6_mod_path\":\"${k6_mod_path}\",\"k6\":\"*\",\"dependencies\":[]}"
    else
        body="{\"k6\":\"*\",\"dependencies\":[]}"
    fi

    response=$(curl -sf -X POST "${BASE_URL}/resolve" \
        -H "Content-Type: application/json" \
        -d "${body}" 2>&1) || {
        echo "FAIL [${description}]: curl failed — is the server running on :${BUILD_PORT}?"
        FAIL=$((FAIL + 1))
        return
    }

    got=$(echo "${response}" | jq -r '.dependencies.k6 // "<missing>"')

    if [ "${got}" = "${expected_k6_version}" ]; then
        echo "PASS [${description}]: k6=${got}"
        PASS=$((PASS + 1))
    else
        echo "FAIL [${description}]: expected k6=${expected_k6_version}, got k6=${got}"
        echo "       full response: ${response}"
        FAIL=$((FAIL + 1))
    fi
}

check_build() {
    local description="$1"
    local k6_mod_path="$2"
    local out_binary="$3"

    local body
    if [ -n "${k6_mod_path}" ]; then
        body="{\"k6_mod_path\":\"${k6_mod_path}\",\"k6\":\"*\",\"platform\":\"${PLATFORM}\",\"dependencies\":[{\"name\":\"k6/x/faker\",\"constraints\":\"*\"}]}"
    else
        body="{\"k6\":\"*\",\"platform\":\"${PLATFORM}\",\"dependencies\":[{\"name\":\"k6/x/faker\",\"constraints\":\"*\"}]}"
    fi

    echo "  building ${description} (this may take a minute)..."
    response=$(curl -sf -X POST "${BASE_URL}/build" \
        -H "Content-Type: application/json" \
        -d "${body}" 2>&1) || {
        echo "FAIL [${description}]: build request failed — is the server running on :${BUILD_PORT}?"
        FAIL=$((FAIL + 1))
        return
    }

    artifact_url=$(echo "${response}" | jq -r '.artifact.url // "<missing>"')
    if [ "${artifact_url}" = "<missing>" ] || [ -z "${artifact_url}" ]; then
        echo "FAIL [${description}]: no artifact URL in response"
        echo "       full response: ${response}"
        FAIL=$((FAIL + 1))
        return
    fi

    curl -sf -o "${out_binary}" "${artifact_url}" || {
        echo "FAIL [${description}]: failed to download binary from ${artifact_url}"
        FAIL=$((FAIL + 1))
        return
    }
    chmod +x "${out_binary}"

    echo "PASS [${description}]: $("${out_binary}" version 2>&1 | head -1)"
    PASS=$((PASS + 1))
}

# ── routing checks (fast, no build) ──────────────────────────────────────────

echo "==> Routing checks (${BASE_URL})"
echo ""

check_resolve "no k6_mod_path → v1 catalog"             ""                  "v1.7.1"
check_resolve "k6_mod_path=go.k6.io/k6 → v1 catalog"   "go.k6.io/k6"      "v1.7.1"
check_resolve "k6_mod_path=go.k6.io/k6/v2 → v2 catalog" "go.k6.io/k6/v2"  "v0.0.0+5e9049460a48"

# ── build checks (full compile + download) ────────────────────────────────────

echo ""
echo "==> Build checks (platform: ${PLATFORM})"
echo ""

check_build "v1 k6 + k6/x/faker" ""                "${OUT_DIR}/k6-v1"
check_build "v2 k6 + k6/x/faker" "go.k6.io/k6/v2" "${OUT_DIR}/k6-v2"

# ── summary ──────────────────────────────────────────────────────────────────

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
echo "Binaries written to ${OUT_DIR}/"
[ "${FAIL}" -eq 0 ]
