#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
PORT="${E2E_PORT:-18080}"
BASE_URL="http://127.0.0.1:${PORT}"
SERVER_PID=""
SERVER_LOG="/tmp/exe-image-forge-e2e-${PORT}.log"
PLAYWRIGHT_IMAGE="mcr.microsoft.com/playwright:v1.62.1-noble"

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

cd "$ROOT"
(cd vend && go run . -demo -addr "127.0.0.1:${PORT}") >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 120); do
  if curl -fsS "${BASE_URL}/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "demo server exited before becoming ready" >&2
    sed -n '1,160p' "$SERVER_LOG" >&2
    exit 1
  fi
  sleep 0.25
done
curl -fsS "${BASE_URL}/healthz" >/dev/null

export PLAYWRIGHT_EXTERNAL_SERVER=1
export PLAYWRIGHT_BASE_URL="$BASE_URL"

if command -v npm >/dev/null 2>&1; then
  npm ci --no-audit --no-fund
  npx playwright test "$@"
elif command -v docker >/dev/null 2>&1; then
  docker run --rm --network host \
    -e PLAYWRIGHT_EXTERNAL_SERVER=1 \
    -e PLAYWRIGHT_BASE_URL="$BASE_URL" \
    -e UPDATE_SCREENSHOTS="${UPDATE_SCREENSHOTS:-0}" \
    -v "$ROOT:/work" -w /work \
    "$PLAYWRIGHT_IMAGE" \
    /bin/bash -lc 'npm ci --no-audit --no-fund && npx playwright test "$@"' -- "$@"
else
  echo "Node.js/npm or Docker is required for browser tests" >&2
  exit 1
fi
