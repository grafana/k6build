#!/usr/bin/env bash
# start-server.sh — build and start a k6build server with both v1 and v2 catalogs.
#
# Usage:
#   cd testenv && ./start-server.sh
#
# Ports (override with env vars):
#   BUILD_PORT=3000   — k6build build service (point k6 here via K6_BUILD_SERVICE_URL)
#   STORE_PORT=3001   — k6build object store (internal use)
#
# Example: point k6 at the local server
#   export K6_BUILD_SERVICE_URL=http://localhost:3000
#   k6 run my-script.js

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

BUILD_PORT="${BUILD_PORT:-3000}"
STORE_PORT="${STORE_PORT:-3001}"
STORE_DIR="${STORE_DIR:-/tmp/k6build-testenv/store}"
BIN=/tmp/k6build-testenv/k6build

mkdir -p /tmp/k6build-testenv

# Build the k6build binary
echo "Building k6build..."
cd "${REPO_ROOT}"
go build -o "${BIN}" ./cmd/k6build

cleanup() {
    echo ""
    echo "Stopping servers..."
    kill "${STORE_PID}" "${BUILD_PID}" 2>/dev/null || true
    wait "${STORE_PID}" "${BUILD_PID}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Start the object store server
echo "Starting object store on :${STORE_PORT}"
"${BIN}" store \
    --download-url "http://localhost:${STORE_PORT}" \
    --store-dir "${STORE_DIR}" \
    --port "${STORE_PORT}" &
STORE_PID=$!

# Give the store a moment to bind
sleep 1

# Start the build service
echo "Starting k6build server on :${BUILD_PORT}"
echo "  v1 catalog : ${SCRIPT_DIR}/catalog-v1.json"
echo "  v2 catalog : ${SCRIPT_DIR}/catalog-v2.json"
echo ""
echo "  Set K6_BUILD_SERVICE_URL=http://localhost:${BUILD_PORT}"
echo "  Then run: k6 run <script>"
echo "  Or test routing: ./test-routing.sh"
echo ""
echo "Press Ctrl-C to stop."
echo ""

"${BIN}" server \
    --catalog "${SCRIPT_DIR}/catalog-v1.json" \
    --catalog-for "go.k6.io/k6/v2=${SCRIPT_DIR}/catalog-v2.json" \
    --store-url "http://localhost:${STORE_PORT}" \
    --port "${BUILD_PORT}" \
    --verbose &
BUILD_PID=$!

wait "${BUILD_PID}"
