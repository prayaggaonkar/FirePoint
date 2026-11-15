#!/usr/bin/env bash
# Build FireRouter and the mock, then run the OpenAI SDK suites against them.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

pick_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

MOCK_PORT=$(pick_port)
PROXY_PORT=$(pick_port)
TOKENS=8
FIRST_MS=40
INTERVAL_MS=150

mkdir -p bin
go build -o bin/mockupstream ./cmd/mockupstream
go build -o bin/firerouter ./cmd/firerouter

mock_log=$(mktemp)
proxy_log=$(mktemp)
./bin/mockupstream \
  -addr "127.0.0.1:${MOCK_PORT}" \
  -tokens "$TOKENS" \
  -first-token-delay "${FIRST_MS}ms" \
  -token-interval "${INTERVAL_MS}ms" \
  >"$mock_log" 2>&1 &
mock_pid=$!
./bin/firerouter \
  -listen "127.0.0.1:${PROXY_PORT}" \
  -upstream "http://127.0.0.1:${MOCK_PORT}" \
  >"$proxy_log" 2>&1 &
proxy_pid=$!

cleanup() {
  kill "$proxy_pid" "$mock_pid" 2>/dev/null || true
  wait "$proxy_pid" "$mock_pid" 2>/dev/null || true
  rm -f "$mock_log" "$proxy_log"
}
trap cleanup EXIT

ready=0
for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null \
    && curl -sf "http://127.0.0.1:${PROXY_PORT}/healthz" >/dev/null; then
    ready=1
    break
  fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  echo "mock and proxy did not become ready" >&2
  echo "--- mock ---" >&2
  cat "$mock_log" >&2 || true
  echo "--- proxy ---" >&2
  cat "$proxy_log" >&2 || true
  exit 1
fi

if [[ ! -d compat/python/.venv ]]; then
  python3 -m venv compat/python/.venv
fi
compat/python/.venv/bin/pip install -q -r compat/python/requirements.txt
(cd compat/js && npm ci --silent)

export FIREROUTER_BASE_URL="http://127.0.0.1:${PROXY_PORT}/v1"
export MOCK_TOKENS="$TOKENS"
export MOCK_FIRST_MS="$FIRST_MS"
export MOCK_INTERVAL_MS="$INTERVAL_MS"
export MOCK_LOG="$mock_log"

echo "compat proxy ${FIREROUTER_BASE_URL} mock :${MOCK_PORT}"

set +e
compat/python/.venv/bin/python compat/python/test_openai_sdk.py
py=$?
node compat/js/test_openai_sdk.mjs
js=$?
set -e

if [[ "$py" -ne 0 || "$js" -ne 0 ]]; then
  echo "compat failed: python=${py} js=${js}" >&2
  echo "--- mock ---" >&2
  cat "$mock_log" >&2 || true
  echo "--- proxy ---" >&2
  cat "$proxy_log" >&2 || true
  exit 1
fi
