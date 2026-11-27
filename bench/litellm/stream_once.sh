#!/usr/bin/env bash
# Start the mock, start LiteLLM in front of it, and send one streaming chat.
# Prints the wait until the first SSE data line, and how many chunks arrived.
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)
cd "$ROOT"

MOCK_ADDR=${LITELLM_MOCK_ADDR:-127.0.0.1:19111}
PROXY_ADDR=${LITELLM_PROXY_ADDR:-127.0.0.1:14011}
# config.yaml names this port. A different port would miss the mock.
if [[ "$MOCK_ADDR" != "127.0.0.1:19111" ]]; then
	echo "config.yaml api_base is http://127.0.0.1:19111/v1; MOCK_ADDR is ${MOCK_ADDR}" >&2
	exit 1
fi

if [[ ! -x "$DIR/.venv/bin/litellm" ]]; then
	echo "missing LiteLLM venv; run bench/litellm/setup.sh" >&2
	exit 1
fi

mock_pid=""
proxy_pid=""
mock_log=""
proxy_log=""

cleanup() {
	if [[ -n $proxy_pid ]]; then
		kill "$proxy_pid" 2>/dev/null || true
		wait "$proxy_pid" 2>/dev/null || true
	fi
	if [[ -n $mock_pid ]]; then
		kill "$mock_pid" 2>/dev/null || true
		wait "$mock_pid" 2>/dev/null || true
	fi
	rm -f "$mock_log" "$proxy_log"
}
trap cleanup EXIT

wait_http() {
	local url=$1
	local pid=$2
	local log=$3
	local tries=$4
	local i
	for i in $(seq 1 "$tries"); do
		if ! kill -0 "$pid" 2>/dev/null; then
			echo "process $pid exited before $url answered" >&2
			cat "$log" >&2 || true
			return 1
		fi
		if curl -sf "$url" >/dev/null; then
			return 0
		fi
		sleep 0.1
	done
	echo "timed out waiting for $url" >&2
	cat "$log" >&2 || true
	return 1
}

go build -o bin/mockupstream ./cmd/mockupstream

mock_log=$(mktemp)
proxy_log=$(mktemp)

# Same schedule as bench/run_bench.sh, so a buffered body would show up as
# a first data line near 1.2s instead of near the 200ms first-token delay.
./bin/mockupstream \
	-addr "$MOCK_ADDR" \
	-first-token-delay 200ms \
	-token-interval 20ms \
	-tokens 50 \
	>"$mock_log" 2>&1 &
mock_pid=$!

# The admin UI flag is read from the environment at import.
# Drop a database URL or master key inherited from the shell so this
# process cannot attach to either.
export DISABLE_ADMIN_UI=True
unset DATABASE_URL || true
unset LITELLM_MASTER_KEY || true
export LITELLM_LOG=ERROR

"$DIR/.venv/bin/litellm" \
	--config "$DIR/config.yaml" \
	--host 127.0.0.1 \
	--port "${PROXY_ADDR##*:}" \
	--num_workers 1 \
	>"$proxy_log" 2>&1 &
proxy_pid=$!

wait_http "http://$MOCK_ADDR/healthz" "$mock_pid" "$mock_log" 50
wait_http "http://$PROXY_ADDR/health/liveliness" "$proxy_pid" "$proxy_log" 1200

PROXY_ADDR="$PROXY_ADDR" "$DIR/.venv/bin/python" - <<'PY'
import http.client
import json
import os
import time

host, port = os.environ["PROXY_ADDR"].rsplit(":", 1)
body = json.dumps({
    "model": "bench",
    "stream": True,
    "messages": [{"role": "user", "content": "hi"}],
}).encode()

conn = http.client.HTTPConnection(host, int(port), timeout=20)
started = time.perf_counter()
conn.request(
    "POST",
    "/v1/chat/completions",
    body=body,
    headers={
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
        # Reject gzip so the client can see each event as it is flushed.
        "Accept-Encoding": "identity",
    },
)
resp = conn.getresponse()
if resp.status != 200:
    detail = resp.read(2048).decode("utf-8", "replace")
    raise SystemExit(f"status {resp.status}\n{detail}")

def payload(line):
    line = line.strip()
    if not line.startswith("data:"):
        return None
    return line[len("data:"):].strip()

chunks = 0
errors = 0
first_ms = None
second_ms = None
done_ms = None
while True:
    raw = resp.readline()
    if not raw:
        break
    item = payload(raw.decode("utf-8", "replace"))
    if item is None:
        continue
    now_ms = (time.perf_counter() - started) * 1000
    if item == "[DONE]":
        done_ms = now_ms
        break
    chunks += 1
    # LiteLLM turns a stream that never sends finish_reason into an error
    # event. Count it separately so a clean chunk total is not assumed.
    if item.startswith('{"error"'):
        errors += 1
    if first_ms is None:
        first_ms = now_ms
    elif second_ms is None:
        second_ms = now_ms

if first_ms is None:
    raise SystemExit("no data lines")
if done_ms is None:
    raise SystemExit("stream ended without data: [DONE]")

print(f"first_data_ms={first_ms:.3f}")
print(f"chunks={chunks}")
print(f"error_chunks={errors}")
print("done=yes")
if second_ms is not None:
    print(f"second_data_ms={second_ms:.3f}")
print(f"done_ms={done_ms:.3f}")
PY
