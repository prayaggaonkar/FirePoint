#!/usr/bin/env bash
# Four-act laptop demo. The mock is on :9000 and FirePoint is on :8080.
# Keys come from .env. The mock admin token defaults to "demo", matching
# deploy/docker-compose.yml.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

# deploy/demo.env is the committed pair that matches deploy/demo-keys.json.
# A local .env overrides it.
set -a
# shellcheck disable=SC1091
source "$ROOT/deploy/demo.env"
if [[ -f $ROOT/.env ]]; then
	# shellcheck disable=SC1091
	source "$ROOT/.env"
fi
set +a
: "${NOISY_API_KEY:?NOISY_API_KEY is missing from deploy/demo.env}"
: "${QUIET_API_KEY:?QUIET_API_KEY is missing from deploy/demo.env}"

MOCK=${MOCK:-http://127.0.0.1:9000}
PROXY=${PROXY:-http://127.0.0.1:8080}
ADMIN_TOKEN=${ADMIN_TOKEN:-demo}
# DefaultConfig in internal/breaker keeps an open breaker shut for this long.
COOLDOWN_SECONDS=15

if [[ -t 1 ]]; then
	c_bold=$'\033[1m'
	c_cyan=$'\033[36m'
	c_yellow=$'\033[33m'
	c_reset=$'\033[0m'
else
	c_bold=""
	c_cyan=""
	c_yellow=""
	c_reset=""
fi

bg_pids=""
log_dir=$(mktemp -d)

track() {
	bg_pids="$bg_pids $1"
}

kill_tree() {
	local pid=$1 child
	if [[ -r /proc/$pid/task/$pid/children ]]; then
		for child in $(cat "/proc/$pid/task/$pid/children" 2>/dev/null || true); do
			kill_tree "$child"
		done
	fi
	kill "$pid" 2>/dev/null || true
}

cleanup() {
	local pid
	for pid in $bg_pids; do
		kill_tree "$pid"
	done
	for pid in $bg_pids; do
		wait "$pid" 2>/dev/null || true
	done
	bg_pids=""
	if [[ -n ${MOCK:-} && -n ${ADMIN_TOKEN:-} ]]; then
		curl -sf -X POST "$MOCK/admin/reset" \
			-H "Authorization: Bearer $ADMIN_TOKEN" >/dev/null 2>&1 || true
	fi
	rm -rf "$log_dir"
}
trap cleanup EXIT

header() {
	printf '\n%s%s== %s ==%s\n' "$c_bold" "$c_cyan" "$1" "$c_reset"
}

note() {
	printf '%s%s%s\n' "$c_yellow" "$1" "$c_reset"
}

pause() {
	read -r -p $'\nPress Enter for the next act... ' _
}

python_bin() {
	local venv=$ROOT/demo/.venv
	if ! "$venv/bin/python" -c 'import openai' >/dev/null 2>&1; then
		python3 -m venv "$venv"
		"$venv/bin/pip" install -q -r "$ROOT/compat/python/requirements.txt"
	fi
	echo "$venv/bin/python"
}

ensure_loadgen() {
	if [[ ! -x bin/loadgen ]] || ! bin/loadgen -h 2>&1 | grep -q -- '-model'; then
		make build
	fi
}

preflight() {
	curl -sf "$MOCK/healthz" >/dev/null
	curl -sf "$PROXY/healthz" >/dev/null
}

admin_post() {
	local path=$1 body=${2:-}
	curl -sf -X POST "$MOCK$path" \
		-H "Authorization: Bearer $ADMIN_TOKEN" \
		-H 'Content-Type: application/json' \
		-d "$body" >/dev/null
}

# One chat, then wait so the next send is 200ms after this one started.
one_chat() {
	local key=$1 out hdr code model started
	out=$(mktemp)
	hdr=$(mktemp)
	started=$(date +%s%3N)
	code=$(curl -sS -D "$hdr" -o "$out" -w '%{http_code}' \
		-H "Authorization: Bearer $key" \
		-H 'Content-Type: application/json' \
		-d '{"model":"primary","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' \
		"$PROXY/v1/chat/completions" || echo ERR)
	model=$(grep -i '^X-FirePoint-Model:' "$hdr" | awk '{print $2}' | tr -d '\r' || true)
	printf '%s  status=%s  model=%s\n' "$(date +%H:%M:%S)" "$code" "${model:-none}"
	rm -f "$out" "$hdr"
	local elapsed=$(( $(date +%s%3N) - started ))
	if (( elapsed < 200 )); then
		sleep "$(python3 -c "print((200-$elapsed)/1000)")"
	fi
}

failover_loop() {
	local log=$1
	trap 'exit 0' TERM
	while true; do
		one_chat "$QUIET_API_KEY" | tee -a "$log"
	done
}

wait_for_line() {
	local file=$1 after=$2 needle=$3 timeout=$4 start now
	start=$(date +%s)
	while true; do
		if awk -v n="$after" -v pat="$needle" 'NR>n && $0 ~ pat { found=1 } END { exit !found }' "$file"; then
			return 0
		fi
		now=$(date +%s)
		if (( now - start > timeout )); then
			echo "timed out waiting for $needle" >&2
			return 1
		fi
		sleep 0.2
	done
}

breaker_value() {
	local model=$1
	curl -sf "$PROXY/metrics" | awk -v model="$model" '
		index($0, "firepoint_breaker_state{") && index($0, "model=\"" model "\"") { print $2; found=1 }
		END { if (!found) print "missing" }
	'
}

wait_for_breaker() {
	local want=$1 timeout=$2 start now got
	start=$(date +%s)
	while true; do
		got=$(breaker_value primary)
		if [[ $got == "$want" ]]; then
			return 0
		fi
		now=$(date +%s)
		if (( now - start > timeout )); then
			echo "timed out waiting for primary breaker $want (last $got)" >&2
			return 1
		fi
		sleep 0.3
	done
}

act_drop_in() {
	header "1. Drop-in"
	note "Same script, same key. Only BASE_URL changes."
	local py
	py=$(python_bin)
	OPENAI_API_KEY="$QUIET_API_KEY" BASE_URL="$MOCK/v1" "$py" demo/openai_demo.py
	echo
	OPENAI_API_KEY="$QUIET_API_KEY" BASE_URL="$PROXY/v1" "$py" demo/openai_demo.py
}

act_overhead() {
	header "2. Overhead"
	note "loadgen for 20s against the mock, then 20s through FirePoint."
	ensure_loadgen
	local direct=$log_dir/direct.json proxy=$log_dir/proxy.json
	bin/loadgen -url "$MOCK/v1/chat/completions" -model primary \
		-api-key "$QUIET_API_KEY" -rate 5 -duration 20s -warmup 0s \
		-concurrency 4 -json "$direct"
	bin/loadgen -url "$PROXY/v1/chat/completions" -model primary \
		-api-key "$QUIET_API_KEY" -rate 5 -duration 20s -warmup 0s \
		-concurrency 4 -json "$proxy"
	python3 - "$direct" "$proxy" <<'PY'
import json, sys
direct = json.load(open(sys.argv[1]))
proxy = json.load(open(sys.argv[2]))
rows = [("p50", direct["ttft_ms"]["p50"], proxy["ttft_ms"]["p50"]),
        ("p99", direct["ttft_ms"]["p99"], proxy["ttft_ms"]["p99"])]
print()
print(f"{'TTFT ms':<10} {'direct':>10} {'proxy':>10} {'proxy-direct':>14}")
for name, d, p in rows:
    print(f"{name:<10} {d:10.1f} {p:10.1f} {p-d:14.1f}")
PY
}

act_failover() {
	header "3. Failover"
	note "One request every 200ms. Primary fails, then the breaker cools down."
	local log=$log_dir/failover.log
	: >"$log"
	failover_loop "$log" &
	local failover_pid=$!
	track "$failover_pid"
	sleep 1.5
	local before
	before=$(wc -l <"$log")
	note "Telling the mock to fail model names that contain \"primary\"."
	admin_post /admin/fail '{"model_contains":"primary","rate":1}'
	wait_for_line "$log" "$before" 'model=fallback' 20
	note "Requests now name the fallback. Waiting until the primary breaker opens."
	wait_for_breaker 1 30
	before=$(wc -l <"$log")
	note "Mock is healthy again. The open breaker still skips primary for ${COOLDOWN_SECONDS}s."
	admin_post /admin/reset ''
	wait_for_line "$log" "$before" 'model=primary' $((COOLDOWN_SECONDS + 20))
	note "Primary is serving again."
	disown "$failover_pid" 2>/dev/null || true
	kill_tree "$failover_pid"
	wait "$failover_pid" 2>/dev/null || true
	bg_pids=${bg_pids// $failover_pid/}
}

count_codes() {
	local file=$1 want=$2
	awk -v want="$want" '$1==want { n++ } END { print n+0 }' "$file"
}

act_noisy() {
	header "4. Noisy neighbor"
	note "Noisy is capped at 60 requests per minute. Quiet keeps its own budget."
	local noisy=$log_dir/noisy.txt quiet=$log_dir/quiet.txt
	: >"$noisy"
	: >"$quiet"
	(
		for _ in $(seq 1 100); do
			code=$(curl -sS -o /dev/null -w '%{http_code}' \
				-H "Authorization: Bearer $NOISY_API_KEY" \
				-H 'Content-Type: application/json' \
				-d '{"model":"fallback","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' \
				"$PROXY/v1/chat/completions" || echo ERR)
			printf '%s\n' "$code" >>"$noisy"
		done
	) &
	track $!
	local noisy_pid=$!
	(
		for _ in $(seq 1 16); do
			code=$(curl -sS -o /dev/null -w '%{http_code}' \
				-H "Authorization: Bearer $QUIET_API_KEY" \
				-H 'Content-Type: application/json' \
				-d '{"model":"fallback","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}' \
				"$PROXY/v1/chat/completions" || echo ERR)
			printf '%s\n' "$code" >>"$quiet"
			sleep 0.4
		done
	) &
	track $!
	local quiet_pid=$!
	wait "$noisy_pid" || true
	wait "$quiet_pid" || true
	bg_pids=""
	local noisy_429 quiet_ok noisy_ok quiet_429
	noisy_429=$(count_codes "$noisy" 429)
	noisy_ok=$(count_codes "$noisy" 200)
	quiet_ok=$(count_codes "$quiet" 200)
	quiet_429=$(count_codes "$quiet" 429)
	printf '\n%-8s %8s %8s\n' "" "200" "429"
	printf '%-8s %8s %8s\n' noisy "$noisy_ok" "$noisy_429"
	printf '%-8s %8s %8s\n' quiet "$quiet_ok" "$quiet_429"
}

preflight
act_drop_in
pause
act_overhead
pause
act_failover
pause
act_noisy
note "Done."
