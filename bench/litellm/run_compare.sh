#!/usr/bin/env bash
# Three paths, one mock. Each trial rotates which path is measured first.
# Warmup is discarded by loadgen.
#
# SCENARIO=A (default) is open: no client key, FirePoint budgets raised.
# SCENARIO=B turns auth and limits on, with limits high enough that nothing
# is rejected. LiteLLM's virtual key lives in local Postgres. FirePoint's
# key is a sha256 in a JSON file. See config-b.yaml.
# SCENARIO=C is failover. The mock's -primary model returns 503 immediately.
# SCENARIO=all runs A, then B, then C.
#
# SMOKE=1 measures 5s once per path instead of 30s across five trials.
# Scenarios A and B also sweep 100 and 200 rps. Saturation errors there are
# recorded and do not fail the run. The 20 rps pass still fails on errors.
# LiteLLM is started twice: workers=default (1, DEFAULT_NUM_WORKERS_LITELLM_PROXY)
# and workers=cpu (logical CPUs). The server stays the default uvicorn path.
# Results land in bench/litellm/results/<scenario>/<workers>/rps-<rate>.
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)
cd "$ROOT"

RUNS=${BENCH_RUNS:-5}
RATE=${BENCH_RATE:-20}
DURATION=${BENCH_DURATION:-30s}
WARMUP=${BENCH_WARMUP:-10s}
CONCURRENCY=${BENCH_CONCURRENCY:-64}
TOKENS=${BENCH_TOKENS:-50}
SCENARIO=${SCENARIO:-A}
# config.yaml and the scenario files send LiteLLM at this mock.
MOCK_ADDR=${BENCH_MOCK_ADDR:-127.0.0.1:19111}
FIRE_ADDR=${BENCH_PROXY_ADDR:-127.0.0.1:18091}
LITE_ADDR=${LITELLM_PROXY_ADDR:-127.0.0.1:14011}
BASE=${BENCH_OUT:-bench/litellm/results}
PG_PORT=54329
PG_DIR="$DIR/pg"
# Documented CLI default. Passing it explicitly avoids a NUM_WORKERS value
# inherited from the shell.
LITE_DEFAULT_WORKERS=1
case $(uname -s) in
Darwin) CORES=$(sysctl -n hw.ncpu) ;;
Linux) CORES=$(nproc) ;;
*) CORES=0 ;;
esac
if [[ "$CORES" -lt 1 ]]; then
	echo "could not read the CPU count" >&2
	exit 1
fi

if [[ "${SMOKE:-}" == "1" ]]; then
	RUNS=1
	DURATION=5s
fi

if [[ "$MOCK_ADDR" != "127.0.0.1:19111" ]]; then
	echo "LiteLLM api_base is http://127.0.0.1:19111/v1; MOCK_ADDR is ${MOCK_ADDR}" >&2
	exit 1
fi
if [[ ! -x "$DIR/.venv/bin/litellm" ]]; then
	echo "missing LiteLLM venv; run bench/litellm/setup.sh" >&2
	exit 1
fi

case "$SCENARIO" in
A | B | C) scenarios=("$SCENARIO") ;;
all) scenarios=(A B C) ;;
*)
	echo "SCENARIO must be A, B, C, or all" >&2
	exit 1
	;;
esac

mock_pid=""
fire_pid=""
lite_pid=""
sample_pid=""
pg_started=0
OUT="$BASE"

cleanup() {
	stop_servers
	if [[ "$pg_started" == "1" ]]; then
		pgbin="$(brew --prefix postgresql@16)/bin"
		"$pgbin/pg_ctl" -D "$PG_DIR" -m fast stop >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

stop_litellm() {
	if [[ -n ${lite_pid:-} ]]; then
		# Worker processes are children. Signal them before the parent so
		# the listen port is released.
		pkill -P "$lite_pid" 2>/dev/null || true
		kill "$lite_pid" 2>/dev/null || true
		wait "$lite_pid" 2>/dev/null || true
		lite_pid=""
	fi
}

stop_servers() {
	if [[ -n ${sample_pid:-} ]]; then
		kill "$sample_pid" 2>/dev/null || true
		wait "$sample_pid" 2>/dev/null || true
		sample_pid=""
	fi
	stop_litellm
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
			# The curl can hit a different process that already owned the port.
			# The child we started has to still be the one that is alive.
			if kill -0 "$pid" 2>/dev/null; then
				return 0
			fi
		fi
		sleep 0.1
	done
	echo "timed out waiting for $url" >&2
	cat "$log" >&2 || true
	return 1
}

# Loadgen counts a non-200 as errors, and a 429 on its own.
require_clean() {
	local file=$1
	local run=$2
	local path=$3
	python3 - "$file" "$run" "$path" <<'PY'
import json, sys
report = json.load(open(sys.argv[1]))
errors = int(report.get("errors") or 0)
denied = int(report.get("status_429") or 0)
if errors or denied:
    print(
        f"trial {sys.argv[2]} {sys.argv[3]} failed: {errors} errors, {denied} status 429",
        file=sys.stderr,
    )
    sys.exit(1)
PY
}

# Scenario C prints success and the mock counters. A proxy that let a
# primary failure through exits. The direct path is expected to see 503s.
report_failover() {
	local file=$1
	local counts=$2
	local run=$3
	local path=$4
	python3 - "$file" "$counts" "$run" "$path" <<'PY'
import json, sys
report = json.load(open(sys.argv[1]))
counts = json.load(open(sys.argv[2])).get("models", {})
run, path = sys.argv[3], sys.argv[4]

def rate(window):
    n = int(window.get("requests") or 0)
    bad = int(window.get("errors") or 0) + int(window.get("status_429") or 0)
    if n == 0:
        return "n/a"
    return f"{(n - bad) / n:.3f}"

print(f"trial {run} {path} success_rate={rate(report)} errors={report.get('errors', 0)} status_429={report.get('status_429', 0)}")
if "first_5s" in report:
    print(f"trial {run} {path} first_5s_success_rate={rate(report['first_5s'])}")
    print(f"trial {run} {path} steady_success_rate={rate(report['steady'])}")
print(f"trial {run} {path} mock_counts={json.dumps(counts, sort_keys=True)} (warmup and measured)")
errors = int(report.get("errors") or 0)
denied = int(report.get("status_429") or 0)
if path != "direct" and (errors or denied):
    print(
        f"trial {run} {path} failed: primary errors reached the client "
        f"({errors} errors, {denied} status 429)",
        file=sys.stderr,
    )
    sys.exit(1)
PY
}

url_for() {
	case $1 in
	direct) echo "http://$MOCK_ADDR/v1/chat/completions" ;;
	firepoint) echo "http://$FIRE_ADDR/v1/chat/completions" ;;
	litellm) echo "http://$LITE_ADDR/v1/chat/completions" ;;
	*)
		echo "unknown path $1" >&2
		return 1
		;;
	esac
}

http_code() {
	curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$@" || true
}

start_postgres() {
	local pgbin
	pgbin="$(brew --prefix postgresql@16 2>/dev/null || true)/bin"
	if [[ ! -x "$pgbin/initdb" ]]; then
		echo "postgresql@16 is required for scenario B (brew install postgresql@16)" >&2
		exit 1
	fi
	mkdir -p "$BASE"
	if [[ ! -f "$PG_DIR/PG_VERSION" ]]; then
		"$pgbin/initdb" -D "$PG_DIR" -U litellm --auth-local=trust --auth-host=trust
	fi
	if ! "$pgbin/pg_ctl" -D "$PG_DIR" status >/dev/null 2>&1; then
		"$pgbin/pg_ctl" -D "$PG_DIR" -l "$BASE/postgres.log" -o "-p $PG_PORT -k $PG_DIR" start
		pg_started=1
	fi
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

write_environment() {
	local scenario=$1
	GO_VERSION=$(go version)
	OS_NAME=$(uname -srm)
	case $(uname -s) in
	Darwin)
		CPU_MODEL=$(sysctl -n machdep.cpu.brand_string)
		CORES=$(sysctl -n hw.ncpu)
		;;
	Linux)
		CPU_MODEL=$(awk -F: '/^model name/{gsub(/^ +/, "", $2); print $2; exit}' /proc/cpuinfo)
		CORES=$(nproc)
		;;
	*)
		CPU_MODEL=unknown
		CORES=0
		;;
	esac
	LITELLM_VERSION=$("$DIR/.venv/bin/python" -c 'import importlib.metadata as m; print(m.version("litellm"))')
	SCENARIO_NAME="$scenario" GO_VERSION="$GO_VERSION" CPU_MODEL="$CPU_MODEL" CORES="$CORES" \
		OS_NAME="$OS_NAME" LITELLM_VERSION="$LITELLM_VERSION" OUT="$OUT" python3 - <<'PY'
import json, os
body = {
    "scenario": os.environ["SCENARIO_NAME"],
    "go_version": os.environ["GO_VERSION"],
    "litellm_version": os.environ["LITELLM_VERSION"],
    "cpu_model": os.environ["CPU_MODEL"] or "unknown",
    "cores": int(os.environ["CORES"]),
    "os": os.environ["OS_NAME"],
}
if body["scenario"] == "C":
    body["firepoint_breaker"] = {
        "window_s": 30,
        "min_requests": 20,
        "error_threshold": 0.5,
        "open_s": 15,
        "half_open_probes": 3,
    }
    body["litellm_cooldown"] = {
        "num_retries": 0,
        "allowed_fails": None,
        "rule": "single deployment: 100% failures and 1000 requests in the current minute, or a non-retryable status. The 50% rule is skipped.",
        "cooldown_time_s": 5,
    }
json.dump(body, open(os.environ["OUT"] + "/environment.json", "w"), indent=2)
open(os.environ["OUT"] + "/environment.json", "a").write("\n")
PY
}

ensure_free() {
	local port=$1
	local holders
	holders=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
	if [[ -n "$holders" ]]; then
		echo "port $port is already in use" >&2
		printf '%s\n' "$holders" >&2
		exit 1
	fi
}

start_mock() {
	local primary=${1:-}
	ensure_free "${MOCK_ADDR##*:}"
	local args=(-addr "$MOCK_ADDR" -first-token-delay 200ms -token-interval 20ms -tokens "$TOKENS")
	if [[ -n "$primary" ]]; then
		args+=(-primary "$primary")
	fi
	./bin/mockupstream "${args[@]}" >"$OUT/mock.log" 2>&1 &
	mock_pid=$!
	wait_http "http://$MOCK_ADDR/healthz" "$mock_pid" "$OUT/mock.log" 50
}

start_firepoint() {
	ensure_free "${FIRE_ADDR##*:}"
	local -a args=(-listen "$FIRE_ADDR" -upstream "http://$MOCK_ADDR" -rpm 1000000 -tpm 100000000)
	if [[ -n ${1:-} ]]; then
		args+=(-keys "$1")
	fi
	if [[ -n ${2:-} ]]; then
		args+=(-config "$2")
	fi
	env -u FIREWORKS_API_KEY ./bin/firepoint "${args[@]}" >"$OUT/firepoint.log" 2>&1 &
	fire_pid=$!
	wait_http "http://$FIRE_ADDR/healthz" "$fire_pid" "$OUT/firepoint.log" 50
}

start_litellm() {
	local config=$1
	local workers=$2
	local tries=$3
	local log=$4
	ensure_free "${LITE_ADDR##*:}"
	# A and C must not attach to a database or a master key from the shell.
	# B passes both in the environment. The admin UI flag is read at import.
	# NUM_WORKERS is unset so the flag is the only worker setting.
	# --num_workers is uvicorn's process count. Gunicorn is not selected.
	if [[ -n ${DATABASE_URL:-} ]]; then
		env -u NUM_WORKERS DISABLE_ADMIN_UI=True LITELLM_LOG=ERROR \
			LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-}" \
			DATABASE_URL="$DATABASE_URL" \
			"$DIR/.venv/bin/litellm" \
			--config "$config" \
			--host 127.0.0.1 \
			--port "${LITE_ADDR##*:}" \
			--num_workers "$workers" \
			>"$log" 2>&1 &
	else
		env -u DATABASE_URL -u LITELLM_MASTER_KEY -u NUM_WORKERS \
			DISABLE_ADMIN_UI=True LITELLM_LOG=ERROR \
			"$DIR/.venv/bin/litellm" \
			--config "$config" \
			--host 127.0.0.1 \
			--port "${LITE_ADDR##*:}" \
			--num_workers "$workers" \
			>"$log" 2>&1 &
	fi
	lite_pid=$!
	wait_http "http://$LITE_ADDR/health/liveliness" "$lite_pid" "$log" "$tries"
}

# Two seconds of in-flight streams, and never below the 20 rps cap of 64.
# A 50-token mock stream is about 1.2s, so 64 would clip 100 and 200 rps.
concurrency_for() {
	python3 -c 'import math,sys; rate=float(sys.argv[1]); print(max(64, math.ceil(rate * 2)))' "$1"
}

seconds_of() {
	local value=$1
	value=${value%s}
	printf '%s\n' "$value"
}

# scenario model rate concurrency worker_label worker_count strict
# strict=1 fails the run on errors. The 100 and 200 rps sweeps pass 0.
run_trials() {
	local scenario=$1
	local model=$2
	local rate=$3
	local conc=$4
	local workers_label=$5
	local workers_count=$6
	local strict=$7
	local dest="$OUT/workers-${workers_label}/rps-${rate}"
	local paths=(direct firepoint litellm)
	local run start step path key proxy
	mkdir -p "$dest"
	for run in $(seq 1 "$RUNS"); do
		start=$(( (run - 1) % 3 ))
		order=()
		for step in 0 1 2; do
			order+=("${paths[$(( (start + step) % 3 ))]}")
		done
		echo "trial $run/$RUNS litellm_workers=$workers_label count=$workers_count requested_rps=$rate concurrency=$conc order: ${order[*]}"
		for path in "${order[@]}"; do
			if [[ "$scenario" == "C" ]]; then
				curl -sf -X POST "http://$MOCK_ADDR/counts/reset" >/dev/null
			fi
			key=""
			if [[ "$scenario" == "B" && "$path" == "firepoint" ]]; then
				key=$FP_KEY
			elif [[ "$scenario" == "B" && "$path" == "litellm" ]]; then
				key=$LITE_KEY
			fi
			proxy=""
			case $path in
			firepoint) proxy=$fire_pid ;;
			litellm) proxy=$lite_pid ;;
			esac
			echo "trial $run/$RUNS $path litellm_workers=$workers_label count=$workers_count requested_rps=$rate"
			if [[ -n "$proxy" ]]; then
				"$DIR/.venv/bin/python" "$DIR/sample_proc.py" \
					--pid "$proxy" \
					--warmup "$(seconds_of "$WARMUP")" \
					--duration "$(seconds_of "$DURATION")" \
					--out "$dest/run-${run}-${path}.resource" &
				sample_pid=$!
			else
				printf '%s\n' '{"root_pid":null,"samples":0,"process_count_max":0,"cpu_percent_median":null,"cpu_percent_max":null,"rss_bytes_median":null,"rss_bytes_max":null,"error":"direct path has no proxy process"}' \
					>"$dest/run-${run}-${path}.resource"
			fi
			# A fixed argv list. An empty array under set -u is an unbound
			# variable on the bash this machine ships.
			cmd=(./bin/loadgen
				-url "$(url_for "$path")"
				-model "$model"
				-api-key "$key"
				-rate "$rate"
				-duration "$DURATION"
				-warmup "$WARMUP"
				-concurrency "$conc"
				-json "$dest/run-${run}-${path}.tmp")
			if [[ "$scenario" == "C" ]]; then
				cmd+=(-split 5s)
			fi
			"${cmd[@]}"
			if [[ -n ${sample_pid:-} ]]; then
				wait "$sample_pid" || true
				sample_pid=""
			fi
			if [[ "$scenario" == "C" ]]; then
				curl -sf "http://$MOCK_ADDR/counts" >"$dest/run-${run}-${path}.counts"
			fi
			REPORT="$dest/run-${run}-${path}.tmp" RESOURCE="$dest/run-${run}-${path}.resource" \
				PATH_NAME="$path" WORKERS_LABEL="$workers_label" WORKERS_COUNT="$workers_count" \
				STRICT="$strict" python3 - <<'PY'
import json, os, sys
report = json.load(open(os.environ["REPORT"]))
resource = json.load(open(os.environ["RESOURCE"]))
duration = float(report.get("duration_seconds") or 0)
requests = int(report.get("requests") or 0)
errors = int(report.get("errors") or 0)
denied = int(report.get("status_429") or 0)
ok = requests - errors - denied
achieved = requests / duration if duration else 0
successful = ok / duration if duration else 0
codes = report.get("status_codes") or {}
cap = int(report.get("concurrency_cap") or 0)
print(
    "throughput path={path} litellm_workers={label} count={count} "
    "requested_rps={req} recorded_per_s={rec:.3f} successful_per_s={ok:.3f} "
    "errors={errors} status_429={denied} status_codes={codes} error_kinds={kinds} concurrency_cap={cap}".format(
        path=os.environ["PATH_NAME"],
        label=os.environ["WORKERS_LABEL"],
        count=os.environ["WORKERS_COUNT"],
        req=report.get("rate_per_sec"),
        rec=achieved,
        ok=successful,
        errors=errors,
        denied=denied,
        codes=json.dumps(codes, sort_keys=True),
        kinds=json.dumps(report.get("error_kinds") or {}, sort_keys=True),
        cap=cap,
    )
)
print(
    "resource path={path} litellm_workers={label} samples={samples} processes={procs} "
    "cpu_percent_median={cpu_med} cpu_percent_max={cpu_max} "
    "rss_bytes_median={rss_med} rss_bytes_max={rss_max}".format(
        path=os.environ["PATH_NAME"],
        label=os.environ["WORKERS_LABEL"],
        samples=resource.get("samples"),
        procs=resource.get("process_count_max"),
        cpu_med=resource.get("cpu_percent_median"),
        cpu_max=resource.get("cpu_percent_max"),
        rss_med=resource.get("rss_bytes_median"),
        rss_max=resource.get("rss_bytes_max"),
    )
)
if resource.get("error"):
    print("resource_note path={0} {1}".format(os.environ["PATH_NAME"], resource["error"]))
PY
			if [[ "$scenario" == "C" ]]; then
				report_failover "$dest/run-${run}-${path}.tmp" "$dest/run-${run}-${path}.counts" "$run" "$path"
			elif [[ "$strict" == "1" ]]; then
				require_clean "$dest/run-${run}-${path}.tmp" "$run" "$path"
			fi
		done
		RUN="$run" DEST="$dest" OUT="$OUT" ORDER="${order[*]}" SCENARIO_NAME="$scenario" \
			WORKERS_LABEL="$workers_label" WORKERS_COUNT="$workers_count" RATE_NOW="$rate" \
			CONC_NOW="$conc" python3 - <<'PY'
import json, os
dest = os.environ["DEST"]
run = os.environ["RUN"]
order = os.environ["ORDER"].split()
env = json.load(open(os.environ["OUT"] + "/environment.json"))
body = {
    "run": int(run),
    "scenario": os.environ["SCENARIO_NAME"],
    "order": order,
    "requested_rps": float(os.environ["RATE_NOW"]),
    "concurrency": int(os.environ["CONC_NOW"]),
    "litellm_workers": os.environ["WORKERS_LABEL"],
    "litellm_worker_count": int(os.environ["WORKERS_COUNT"]),
    "environment": env,
}
for path in order:
    report = json.load(open(f"{dest}/run-{run}-{path}.tmp"))
    report["resource"] = json.load(open(f"{dest}/run-{run}-{path}.resource"))
    duration = float(report.get("duration_seconds") or 0)
    requests = int(report.get("requests") or 0)
    errors = int(report.get("errors") or 0)
    denied = int(report.get("status_429") or 0)
    report["recorded_per_s"] = requests / duration if duration else 0
    report["successful_per_s"] = (requests - errors - denied) / duration if duration else 0
    counts = f"{dest}/run-{run}-{path}.counts"
    if os.path.exists(counts):
        report["mock"] = json.load(open(counts))
    body[path] = report
with open(f"{dest}/run-{run}.json", "w") as fh:
    json.dump(body, fh, indent=2)
    fh.write("\n")
for path in order:
    os.remove(f"{dest}/run-{run}-{path}.tmp")
    os.remove(f"{dest}/run-{run}-{path}.resource")
    counts = f"{dest}/run-{run}-{path}.counts"
    if os.path.exists(counts):
        os.remove(counts)
PY
		echo "wrote $dest/run-${run}.json"
	done
}

# One LiteLLM worker setting, then the rates for this scenario.
# A and B sweep 20, 100, and 200. C stays at 20.
run_worker_matrix() {
	local scenario=$1
	local model=$2
	local config=$3
	local tries=$4
	local label count rate conc strict
	local -a rates
	for label in default cpu; do
		if [[ "$label" == "default" ]]; then
			count=$LITE_DEFAULT_WORKERS
		else
			count=$CORES
		fi
		stop_litellm
		echo "litellm_workers label=$label count=$count"
		echo "litellm_workers note: default is $LITE_DEFAULT_WORKERS from DEFAULT_NUM_WORKERS_LITELLM_PROXY. cpu is the logical CPU count ($CORES). uvicorn is the CLI default; gunicorn is not enabled."
		start_litellm "$config" "$count" "$tries" "$OUT/litellm-${label}.log"
		if [[ "$scenario" == "B" ]]; then
			no_fire=$(http_code -H 'Content-Type: application/json' \
				-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
				"http://$FIRE_ADDR/v1/chat/completions")
			yes_fire=$(http_code -H "Authorization: Bearer $FP_KEY" -H 'Content-Type: application/json' \
				-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
				"http://$FIRE_ADDR/v1/chat/completions")
			no_lite=$(http_code -H 'Content-Type: application/json' \
				-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
				"http://$LITE_ADDR/v1/chat/completions")
			yes_lite=$(http_code -H "Authorization: Bearer $LITE_KEY" -H 'Content-Type: application/json' \
				-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
				"http://$LITE_ADDR/v1/chat/completions")
			echo "auth_check litellm_workers=$label firepoint_without_key=$no_fire firepoint_with_key=$yes_fire litellm_without_key=$no_lite litellm_with_virtual_key=$yes_lite"
			if [[ "$no_fire" != "401" || "$yes_fire" != "200" || "$no_lite" != "401" || "$yes_lite" != "200" ]]; then
				echo "auth check failed" >&2
				exit 1
			fi
		fi
		if [[ "$scenario" == "C" ]]; then
			direct_code=$(http_code -H 'Content-Type: application/json' \
				-d '{"model":"primary","messages":[{"role":"user","content":"hi"}]}' \
				"http://$MOCK_ADDR/v1/chat/completions")
			fire_code=$(http_code -H 'Content-Type: application/json' \
				-d '{"model":"primary","messages":[{"role":"user","content":"hi"}]}' \
				"http://$FIRE_ADDR/v1/chat/completions")
			lite_code=$(http_code -H 'Content-Type: application/json' \
				-d '{"model":"primary","messages":[{"role":"user","content":"hi"}]}' \
				"http://$LITE_ADDR/v1/chat/completions")
			echo "failover_check litellm_workers=$label direct=$direct_code firepoint=$fire_code litellm=$lite_code"
			if [[ "$direct_code" != "503" || "$fire_code" != "200" || "$lite_code" != "200" ]]; then
				echo "failover check failed: a proxy returned the primary error, or the direct path did not" >&2
				exit 1
			fi
			curl -sf -X POST "http://$MOCK_ADDR/counts/reset" >/dev/null
		fi
		if [[ "$scenario" == "C" ]]; then
			rates=(20)
		else
			rates=(20 100 200)
		fi
		for rate in "${rates[@]}"; do
			conc=$(concurrency_for "$rate")
			strict=1
			if [[ "$scenario" != "C" && "$rate" != "20" ]]; then
				strict=0
			fi
			echo "sweep scenario=$scenario litellm_workers=$label count=$count requested_rps=$rate concurrency=$conc strict=$strict"
			run_trials "$scenario" "$model" "$rate" "$conc" "$label" "$count" "$strict"
		done
	done
}

for scenario in "${scenarios[@]}"; do
	if [[ "$scenario" == "B" ]]; then
		cat <<'EOF'
scenario B: LiteLLM virtual keys store rpm_limit and tpm_limit in Postgres.
A master key would authenticate and skip those per-key checks, so it is used
only to call /key/generate. Loadgen sends the generated virtual key.
FirePoint uses a sha256 in a JSON file and does not use this database.
The Python prisma package migrates that database. litellm[proxy] does not
include it.
Limits are 1000000 rpm and 100000000 tpm on both, above the offered load.
EOF
	fi
done

make build
mkdir -p "$BASE"

for scenario in "${scenarios[@]}"; do
	stop_servers
	unset DATABASE_URL || true
	unset LITELLM_MASTER_KEY || true
	OUT="$BASE/$scenario"
	mkdir -p "$OUT"
	echo "scenario $scenario"
	case "$scenario" in
	A)
		start_mock
		start_firepoint
		write_environment A
		run_worker_matrix A bench "$DIR/config.yaml" 1200
		;;
	B)
		start_postgres
		export DATABASE_URL="postgresql://litellm@127.0.0.1:${PG_PORT}/litellm"
		export LITELLM_MASTER_KEY="sk-$(openssl rand -hex 32)"
		start_mock
		gen=$(./bin/firepoint keygen --tenant bench --rpm 1000000 --tpm 100000000)
		FP_KEY=$(printf '%s\n' "$gen" | head -n 1)
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
		start_firepoint "$OUT/keys.json"
		# The key is generated through the default worker process. The cpu
		# worker process reads the same database row.
		start_litellm "$DIR/config-b.yaml" "$LITE_DEFAULT_WORKERS" 1800 "$OUT/litellm-default.log"
		key_json=$(curl -sf -X POST "http://$LITE_ADDR/key/generate" \
			-H "Authorization: Bearer $LITELLM_MASTER_KEY" \
			-H "Content-Type: application/json" \
			-d '{"models":["bench"],"rpm_limit":1000000,"tpm_limit":100000000}')
		LITE_KEY=$(KEY_JSON="$key_json" python3 - <<'PY'
import json, os
body = json.loads(os.environ["KEY_JSON"])
print(body["key"])
print(f"litellm_virtual_key rpm_limit={body.get('rpm_limit')} tpm_limit={body.get('tpm_limit')}", file=__import__("sys").stderr)
PY
)
		# The python above prints the key on stdout and the limits on stderr.
		# Keep the key. The limits line already went to our stderr via python,
		# and also got captured if we merged streams. Print limits explicitly.
		KEY_JSON="$key_json" python3 - <<'PY'
import json, os, sys
body = json.loads(os.environ["KEY_JSON"])
print(f"litellm_virtual_key rpm_limit={body.get('rpm_limit')} tpm_limit={body.get('tpm_limit')}")
if not body.get("key") or body.get("rpm_limit") != 1000000 or body.get("tpm_limit") != 100000000:
    sys.exit("key/generate did not return the virtual key limits")
PY
		no_fire=$(http_code -H 'Content-Type: application/json' \
			-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
			"http://$FIRE_ADDR/v1/chat/completions")
		yes_fire=$(http_code -H "Authorization: Bearer $FP_KEY" -H 'Content-Type: application/json' \
			-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
			"http://$FIRE_ADDR/v1/chat/completions")
		no_lite=$(http_code -H 'Content-Type: application/json' \
			-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
			"http://$LITE_ADDR/v1/chat/completions")
		yes_lite=$(http_code -H "Authorization: Bearer $LITE_KEY" -H 'Content-Type: application/json' \
			-d '{"model":"bench","messages":[{"role":"user","content":"hi"}]}' \
			"http://$LITE_ADDR/v1/chat/completions")
		echo "auth_check litellm_workers=default firepoint_without_key=$no_fire firepoint_with_key=$yes_fire litellm_without_key=$no_lite litellm_with_virtual_key=$yes_lite"
		if [[ "$no_fire" != "401" || "$yes_fire" != "200" || "$no_lite" != "401" || "$yes_lite" != "200" ]]; then
			echo "auth check failed" >&2
			exit 1
		fi
		write_environment B
		# Default workers are already up for key generation. Matrix restarts
		# them so both labels boot the same way.
		stop_litellm
		run_worker_matrix B bench "$DIR/config-b.yaml" 1800
		unset LITELLM_MASTER_KEY
		unset DATABASE_URL
		;;
	C)
		echo "scenario C: FirePoint breaker window 30s, min 20 samples, error threshold 0.5, open 15s, half-open probes 3."
		echo "scenario C: LiteLLM num_retries 0, allowed_fails unset, cooldown_time 5s."
		echo "scenario C: primary is one deployment, so the 50% rule does not apply. Single-deployment cooldown needs 100% failures and 1000 requests in the current minute, or a non-retryable status. 503 is retryable."
		echo "scenario C: config-c-default-retries.yaml keeps the default of 2 retries and is not started."
		echo "scenario C: first_5s is the first 5s of the measured window. steady is the rest. Mock counts include warmup."
		start_mock primary
		start_firepoint "" "$DIR/router-c.json"
		write_environment C
		run_worker_matrix C primary "$DIR/config-c.yaml" 1200
		;;
	esac
	stop_servers
done
