#!/usr/bin/env bash
# Checks that run before a full compare. Does not start a measured trial.
# 1. rpm 1 rejects the second request on FirePoint (-rpm 1, no key file)
#    and on LiteLLM (a throwaway virtual key).
# 2. Prints the /key/generate request the benchmark uses.
# 3. 200 primary-503 requests through each proxy, then the mock counters.
# 4. Samples Postgres and LiteLLM CPU and RSS while scenario B is serving.
# config-b-spend.yaml is labeled and is not started.
set -uo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)
cd "$ROOT"

MOCK_ADDR=127.0.0.1:19111
FIRE_ADDR=127.0.0.1:18091
LITE_ADDR=127.0.0.1:14011
PG_PORT=54329
PG_DIR="$DIR/pg"
OUT="$DIR/results/verify"
mkdir -p "$OUT"

mock_pid=""
fire_pid=""
lite_pid=""
pg_started=0
failures=0

note() { printf '%s\n' "$*"; }
fail() { printf 'CHECK_FAILED %s\n' "$*"; failures=$((failures + 1)); }

cleanup() {
	stop_servers
	if [[ "$pg_started" == "1" ]]; then
		pgbin="$(brew --prefix postgresql@16)/bin"
		"$pgbin/pg_ctl" -D "$PG_DIR" -m fast stop >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

stop_servers() {
	if [[ -n ${lite_pid:-} ]]; then
		kill "$lite_pid" 2>/dev/null || true
		wait "$lite_pid" 2>/dev/null || true
		lite_pid=""
	fi
	if [[ -n ${fire_pid:-} ]]; then
		kill "$fire_pid" 2>/dev/null || true
		wait "$fire_pid" 2>/dev/null || true
		fire_pid=""
	fi
	if [[ -n ${mock_pid:-} ]]; then
		kill "$mock_pid" 2>/dev/null || true
		wait "$mock_pid" 2>/dev/null || true
		mock_pid=""
	fi
}

wait_http() {
	local url=$1 pid=$2 log=$3 tries=$4
	local i
	for i in $(seq 1 "$tries"); do
		if curl -sf -o /dev/null --max-time 2 "$url"; then
			return 0
		fi
		if ! kill -0 "$pid" 2>/dev/null; then
			echo "process $pid exited before $url answered" >&2
			tail -n 40 "$log" >&2 || true
			return 1
		fi
		sleep 0.1
	done
	echo "timed out waiting for $url" >&2
	tail -n 40 "$log" >&2 || true
	return 1
}

start_postgres() {
	local pgbin
	pgbin="$(brew --prefix postgresql@16 2>/dev/null || true)/bin"
	if [[ ! -x "$pgbin/initdb" ]]; then
		echo "postgresql@16 is required (brew install postgresql@16)" >&2
		exit 1
	fi
	if [[ ! -f "$PG_DIR/PG_VERSION" ]]; then
		"$pgbin/initdb" -D "$PG_DIR" -U litellm --auth-local=trust --auth-host=trust
	fi
	if ! "$pgbin/pg_ctl" -D "$PG_DIR" status >/dev/null 2>&1; then
		"$pgbin/pg_ctl" -D "$PG_DIR" -l "$OUT/postgres.log" -o "-p $PG_PORT -k $PG_DIR" start
		pg_started=1
	fi
	local _
	for _ in $(seq 1 50); do
		if "$pgbin/psql" -h 127.0.0.1 -p "$PG_PORT" -U litellm -d postgres -c 'select 1' >/dev/null 2>&1; then
			break
		fi
		sleep 0.1
	done
	"$pgbin/psql" -h 127.0.0.1 -p "$PG_PORT" -U litellm -d postgres -c 'select 1' >/dev/null
	"$pgbin/psql" -h 127.0.0.1 -p "$PG_PORT" -U litellm -d postgres -tAc \
		"SELECT 1 FROM pg_database WHERE datname = 'litellm'" | grep -q 1 \
		|| "$pgbin/createdb" -h 127.0.0.1 -p "$PG_PORT" -U litellm litellm
}

start_mock() {
	local primary=${1:-}
	local args=(-addr "$MOCK_ADDR" -first-token-delay 200ms -token-interval 20ms -tokens 50)
	if [[ -n "$primary" ]]; then
		args+=(-primary "$primary")
	fi
	./bin/mockupstream "${args[@]}" >"$OUT/mock.log" 2>&1 &
	mock_pid=$!
	wait_http "http://$MOCK_ADDR/healthz" "$mock_pid" "$OUT/mock.log" 50
}

start_firepoint() {
	env -u FIREWORKS_API_KEY ./bin/firepoint "$@" >"$OUT/firepoint.log" 2>&1 &
	fire_pid=$!
	wait_http "http://$FIRE_ADDR/healthz" "$fire_pid" "$OUT/firepoint.log" 50
}

start_litellm() {
	local config=$1
	local tries=$2
	if [[ -n ${DATABASE_URL:-} ]]; then
		env DISABLE_ADMIN_UI=True LITELLM_LOG=ERROR \
			LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-}" \
			DATABASE_URL="$DATABASE_URL" \
			"$DIR/.venv/bin/litellm" \
			--config "$config" --host 127.0.0.1 --port "${LITE_ADDR##*:}" --num_workers 1 \
			>"$OUT/litellm.log" 2>&1 &
	else
		env -u DATABASE_URL -u LITELLM_MASTER_KEY \
			DISABLE_ADMIN_UI=True LITELLM_LOG=ERROR \
			"$DIR/.venv/bin/litellm" \
			--config "$config" --host 127.0.0.1 --port "${LITE_ADDR##*:}" --num_workers 1 \
			>"$OUT/litellm.log" 2>&1 &
	fi
	lite_pid=$!
	wait_http "http://$LITE_ADDR/health/liveliness" "$lite_pid" "$OUT/litellm.log" "$tries"
}

# One non-streaming chat. Prints "<status> <body-on-one-line>".
chat_once() {
	local url=$1 key=$2
	local -a hdr=(-H 'Content-Type: application/json')
	if [[ -n "$key" ]]; then
		hdr+=(-H "Authorization: Bearer $key")
	fi
	local body status
	body=$(mktemp)
	status=$(curl -s -o "$body" -w '%{http_code}' --max-time 20 \
		"${hdr[@]}" \
		-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
		"$url" || true)
	STATUS="$status" KEY="$key" BODY="$body" python3 - <<'PY'
import os
text = open(os.environ["BODY"]).read().replace("\n", " ")
key = os.environ.get("KEY") or ""
if key:
    text = text.replace(key, "<redacted>")
print(os.environ["STATUS"], text)
PY
	rm -f "$body"
}

make build
if [[ ! -x "$DIR/.venv/bin/litellm" ]]; then
	echo "missing LiteLLM venv; run bench/litellm/setup.sh" >&2
	exit 1
fi

note "=== 1. limit enforcement ==="
note "firepoint argv: -listen $FIRE_ADDR -upstream http://$MOCK_ADDR -rpm 1 -tpm 100000000"
note "no key file, so the process -rpm flag is the limiter. Burst equals one minute, so rpm 1 allows one request."

start_postgres
export DATABASE_URL="postgresql://litellm@127.0.0.1:${PG_PORT}/litellm"
export LITELLM_MASTER_KEY="sk-$(openssl rand -hex 32)"
start_mock
start_firepoint -listen "$FIRE_ADDR" -upstream "http://$MOCK_ADDR" -rpm 1 -tpm 100000000
start_litellm "$DIR/config-b.yaml" 1800

throw_json=$(curl -sf -X POST "http://$LITE_ADDR/key/generate" \
	-H "Authorization: Bearer $LITELLM_MASTER_KEY" \
	-H "Content-Type: application/json" \
	-d '{"models":["bench"],"rpm_limit":1,"tpm_limit":100000000}')
THROW_KEY=$(KEY_JSON="$throw_json" python3 - <<'PY'
import json, os, sys
body = json.loads(os.environ["KEY_JSON"])
print(body.get("key") or "")
print(f"throwaway_key_generate_response rpm_limit={body.get('rpm_limit')} tpm_limit={body.get('tpm_limit')}", file=sys.stderr)
PY
)
note "throwaway LiteLLM request: POST /key/generate {\"models\":[\"bench\"],\"rpm_limit\":1,\"tpm_limit\":100000000}"

bench_request='{"models":["bench"],"rpm_limit":1000000,"tpm_limit":100000000}'
note "benchmark LiteLLM request: POST http://$LITE_ADDR/key/generate"
note "benchmark LiteLLM request headers: Authorization: Bearer <master key, not printed>; Content-Type: application/json"
note "benchmark LiteLLM request body: $bench_request"
bench_json=$(curl -sf -X POST "http://$LITE_ADDR/key/generate" \
	-H "Authorization: Bearer $LITELLM_MASTER_KEY" \
	-H "Content-Type: application/json" \
	-d "$bench_request")
BENCH_KEY=$(KEY_JSON="$bench_json" python3 - <<'PY'
import json, os, sys
body = json.loads(os.environ["KEY_JSON"])
print(body.get("key") or "")
print(
    "benchmark_key_generate_response rpm_limit=%s tpm_limit=%s key_present=%s"
    % (body.get("rpm_limit"), body.get("tpm_limit"), bool(body.get("key"))),
    file=sys.stderr,
)
if body.get("rpm_limit") != 1000000 or body.get("tpm_limit") != 100000000:
    sys.exit("benchmark key limits were not returned")
PY
)

note "two FirePoint requests, same minute, no Authorization header"
fp1=$(chat_once "http://$FIRE_ADDR/v1/chat/completions" "")
note "firepoint_request_1 $fp1"
fp2=$(chat_once "http://$FIRE_ADDR/v1/chat/completions" "")
note "firepoint_request_2 $fp2"
[[ "${fp1%% *}" == "200" ]] || fail "firepoint first request status ${fp1%% *}, want 200"
[[ "${fp2%% *}" == "429" ]] || fail "firepoint second request status ${fp2%% *}, want 429"

note "two LiteLLM requests, same minute, throwaway virtual key rpm_limit 1"
lt1=$(chat_once "http://$LITE_ADDR/v1/chat/completions" "$THROW_KEY")
note "litellm_request_1 $lt1"
lt2=$(chat_once "http://$LITE_ADDR/v1/chat/completions" "$THROW_KEY")
note "litellm_request_2 $lt2"
[[ "${lt1%% *}" == "200" ]] || fail "litellm first request status ${lt1%% *}, want 200"
[[ "${lt2%% *}" == "429" ]] || fail "litellm second request status ${lt2%% *}, want 429"

note "=== FirePoint key file for scenario B (generated the same way as run_compare.sh) ==="
gen=$(./bin/firepoint keygen --tenant bench --rpm 1000000 --tpm 100000000)
GEN="$gen" DEST="$OUT/keys.json" python3 - <<'PY'
import json, os
text = os.environ["GEN"]
_, _, blob = text.partition("\n")
entry = json.loads(blob)
with open(os.environ["DEST"], "w") as fh:
    json.dump({"tenants": [entry]}, fh, indent=2)
    fh.write("\n")
PY
chmod 600 "$OUT/keys.json"
note "key file path: $OUT/keys.json"
cat "$OUT/keys.json"
if [[ -f "$DIR/results/B/keys.json" ]]; then
	note "key file from the last scenario B smoke: $DIR/results/B/keys.json"
	cat "$DIR/results/B/keys.json"
fi

note "=== 3. CPU and RSS while scenario B is serving ==="
# Swap the rpm-1 process for the scenario B process: key file, high rpm.
kill "$fire_pid" 2>/dev/null || true
wait "$fire_pid" 2>/dev/null || true
fire_pid=""
start_firepoint -listen "$FIRE_ADDR" -upstream "http://$MOCK_ADDR" -rpm 1000000 -tpm 100000000 -keys "$OUT/keys.json"
export BENCH_KEY
note "scenario B processes: litellm pid $lite_pid, postgres data dir $PG_DIR"
note "ps pcpu is the lifetime average. top cpu is one interval. rss is KB."

python3 - <<PY &
import json, os, time, urllib.request
url = "http://$LITE_ADDR/v1/chat/completions"
key = os.environ["BENCH_KEY"]
body = json.dumps({"model":"bench","messages":[{"role":"user","content":"hi"}]}).encode()
end = time.time() + 8
while time.time() < end:
    req = urllib.request.Request(url, data=body, headers={
        "Content-Type": "application/json",
        "Authorization": "Bearer " + key,
    })
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            resp.read()
    except Exception as exc:
        print("sample_load_error", type(exc).__name__, getattr(exc, "code", ""), flush=True)
        break
PY
load_pid=$!

pg_master=$(head -n 1 "$PG_DIR/postmaster.pid" 2>/dev/null || true)
for sample in 1 2 3 4; do
	note "--- sample $sample $(date +%H:%M:%S) ---"
	note "litellm ps:"
	ps -p "$lite_pid" -o pid=,ppid=,pcpu=,rss=,command= || note "litellm pid $lite_pid not in ps"
	# Workers are children of the process we started.
	child=$(pgrep -P "$lite_pid" | paste -sd, - || true)
	if [[ -n "$child" ]]; then
		note "litellm children:"
		ps -p "$child" -o pid=,ppid=,pcpu=,rss=,command= || true
	fi
	note "postgres ps (postmaster $pg_master and its children):"
	ps -p "$pg_master" -o pid=,ppid=,pcpu=,rss=,command= || note "postmaster $pg_master not in ps"
	pg_children=$(pgrep -P "$pg_master" | paste -sd, - || true)
	if [[ -n "$pg_children" ]]; then
		ps -p "$pg_children" -o pid=,ppid=,pcpu=,rss=,command= || true
	fi
	if [[ "$sample" == "2" ]]; then
		note "top interval, litellm $lite_pid:"
		top -l 2 -s 1 -pid "$lite_pid" -stats pid,cpu,rsize,command 2>/dev/null | tail -n 5 || note "top litellm failed"
		note "top interval, postgres $pg_master:"
		top -l 2 -s 1 -pid "$pg_master" -stats pid,cpu,rsize,command 2>/dev/null | tail -n 5 || note "top postgres failed"
	fi
	sleep 1
done
wait "$load_pid" 2>/dev/null || true

note "=== 2. cooldown, 200 requests, primary returns 503 ==="
stop_servers
unset DATABASE_URL || true
unset LITELLM_MASTER_KEY || true
start_mock primary
start_firepoint -listen "$FIRE_ADDR" -upstream "http://$MOCK_ADDR" -rpm 1000000 -tpm 100000000 -config "$DIR/router-c.json"
start_litellm "$DIR/config-c.yaml" 1200

burst() {
	local url=$1
	URL="$url" python3 - <<'PY'
import json, os, urllib.error, urllib.request
from collections import Counter
import concurrent.futures
url = os.environ["URL"]
body = json.dumps({"model": "primary", "messages": [{"role": "user", "content": "hi"}]}).encode()

def one(_):
    req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            resp.read()
            return str(resp.status)
    except urllib.error.HTTPError as exc:
        exc.read()
        return str(exc.code)
    except Exception as exc:
        return type(exc).__name__ + ":" + str(getattr(exc, "code", ""))

with concurrent.futures.ThreadPoolExecutor(max_workers=32) as pool:
    codes = list(pool.map(one, range(200)))
print("client_status_counts", dict(Counter(codes)))
print("client_non_200", sum(1 for code in codes if code != "200"))
PY
}

for path in firepoint litellm; do
	curl -sf -X POST "http://$MOCK_ADDR/counts/reset" >/dev/null
	note "--- $path 200 requests model=primary concurrency=32 ---"
	if [[ "$path" == "firepoint" ]]; then
		burst "http://$FIRE_ADDR/v1/chat/completions"
	else
		burst "http://$LITE_ADDR/v1/chat/completions"
	fi
	counts=$(curl -sf "http://$MOCK_ADDR/counts")
	note "mock_counts $counts"
	python3 - "$path" "$counts" <<'PY'
import json, sys
path, raw = sys.argv[1], sys.argv[2]
models = json.loads(raw).get("models", {})
primary = int(models.get("primary", 0))
fallback = int(models.get("fallback", 0))
print(f"{path} primary_requests={primary} fallback_requests={fallback}")
if path == "litellm":
    if primary < 200:
        print("litellm_primary_skipped yes: primary count is below 200 client requests")
    else:
        print("litellm_primary_skipped no: every client request reached primary at least once")
    if primary > 200:
        print(f"litellm_primary_extra {primary - 200}: more primary hits than client requests")
PY
done

note "=== fallback file used for scenario C ==="
cat "$DIR/router-c.json"

note "=== labeled config not started: config-b-spend.yaml ==="
note "path: $DIR/config-b-spend.yaml"
cat "$DIR/config-b-spend.yaml"

note "verify_failures=$failures"
if [[ "$failures" != "0" ]]; then
	exit 1
fi
