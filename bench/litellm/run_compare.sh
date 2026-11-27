#!/usr/bin/env bash
# Scenario A: no client key, FirePoint budgets raised so a rejection is not
# mistaken for proxy delay. Three paths share one mock. Each trial rotates
# which path is measured first. Warmup is discarded by loadgen.
# One JSON file is written per trial, under bench/litellm/results.
# SMOKE=1 measures 5s once per path instead of 30s across five trials.
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
# config.yaml sends LiteLLM at this mock. A different address would miss it.
MOCK_ADDR=${BENCH_MOCK_ADDR:-127.0.0.1:19111}
FIRE_ADDR=${BENCH_PROXY_ADDR:-127.0.0.1:18091}
LITE_ADDR=${LITELLM_PROXY_ADDR:-127.0.0.1:14011}
OUT=${BENCH_OUT:-bench/litellm/results}

if [[ "${SMOKE:-}" == "1" ]]; then
	RUNS=1
	DURATION=5s
fi

if [[ "$MOCK_ADDR" != "127.0.0.1:19111" ]]; then
	echo "config.yaml api_base is http://127.0.0.1:19111/v1; MOCK_ADDR is ${MOCK_ADDR}" >&2
	exit 1
fi
if [[ ! -x "$DIR/.venv/bin/litellm" ]]; then
	echo "missing LiteLLM venv; run bench/litellm/setup.sh" >&2
	exit 1
fi

mock_pid=""
fire_pid=""
lite_pid=""

cleanup() {
	if [[ -n $lite_pid ]]; then
		kill "$lite_pid" 2>/dev/null || true
		wait "$lite_pid" 2>/dev/null || true
	fi
	if [[ -n $fire_pid ]]; then
		kill "$fire_pid" 2>/dev/null || true
		wait "$fire_pid" 2>/dev/null || true
	fi
	if [[ -n $mock_pid ]]; then
		kill "$mock_pid" 2>/dev/null || true
		wait "$mock_pid" 2>/dev/null || true
	fi
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

# Loadgen counts a non-200 as errors, and a 429 on its own. Either one means
# this trial did not measure a served stream.
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

make build
mkdir -p "$OUT"

./bin/mockupstream \
	-addr "$MOCK_ADDR" \
	-first-token-delay 200ms \
	-token-interval 20ms \
	-tokens "$TOKENS" \
	>"$OUT/mock.log" 2>&1 &
mock_pid=$!

# No key file and no upstream key. Budgets are high so the limiter stays closed.
env -u FIREWORKS_API_KEY ./bin/firepoint \
	-listen "$FIRE_ADDR" \
	-upstream "http://$MOCK_ADDR" \
	-rpm 1000000 \
	-tpm 100000000 \
	>"$OUT/firepoint.log" 2>&1 &
fire_pid=$!

# The admin UI flag is read from the environment at import.
# Drop a database URL or master key inherited from the shell.
env -u DATABASE_URL -u LITELLM_MASTER_KEY \
	DISABLE_ADMIN_UI=True LITELLM_LOG=ERROR \
	"$DIR/.venv/bin/litellm" \
	--config "$DIR/config.yaml" \
	--host 127.0.0.1 \
	--port "${LITE_ADDR##*:}" \
	--num_workers 1 \
	>"$OUT/litellm.log" 2>&1 &
lite_pid=$!

wait_http "http://$MOCK_ADDR/healthz" "$mock_pid" "$OUT/mock.log" 50
wait_http "http://$FIRE_ADDR/healthz" "$fire_pid" "$OUT/firepoint.log" 50
wait_http "http://$LITE_ADDR/health/liveliness" "$lite_pid" "$OUT/litellm.log" 1200

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
export GO_VERSION CPU_MODEL CORES OS_NAME LITELLM_VERSION OUT

python3 - <<'PY'
import json, os
json.dump({
    "scenario": "A",
    "go_version": os.environ["GO_VERSION"],
    "litellm_version": os.environ["LITELLM_VERSION"],
    "cpu_model": os.environ["CPU_MODEL"] or "unknown",
    "cores": int(os.environ["CORES"]),
    "os": os.environ["OS_NAME"],
}, open(os.environ["OUT"] + "/environment.json", "w"), indent=2)
open(os.environ["OUT"] + "/environment.json", "a").write("\n")
PY

paths=(direct firepoint litellm)
for run in $(seq 1 "$RUNS"); do
	start=$(( (run - 1) % 3 ))
	order=()
	for step in 0 1 2; do
		order+=("${paths[$(( (start + step) % 3 ))]}")
	done
	echo "trial $run/$RUNS order: ${order[*]}"
	for path in "${order[@]}"; do
		echo "trial $run/$RUNS $path"
		./bin/loadgen \
			-url "$(url_for "$path")" \
			-model bench \
			-api-key "" \
			-rate "$RATE" \
			-duration "$DURATION" \
			-warmup "$WARMUP" \
			-concurrency "$CONCURRENCY" \
			-json "$OUT/run-${run}-${path}.tmp"
		require_clean "$OUT/run-${run}-${path}.tmp" "$run" "$path"
	done
	RUN="$run" OUT="$OUT" ORDER="${order[*]}" python3 - <<'PY'
import json, os
out = os.environ["OUT"]
run = os.environ["RUN"]
order = os.environ["ORDER"].split()
env = json.load(open(out + "/environment.json"))
body = {"run": int(run), "scenario": "A", "order": order, "environment": env}
for path in order:
    body[path] = json.load(open(f"{out}/run-{run}-{path}.tmp"))
with open(f"{out}/run-{run}.json", "w") as fh:
    json.dump(body, fh, indent=2)
    fh.write("\n")
for path in ("direct", "firepoint", "litellm"):
    os.remove(f"{out}/run-{run}-{path}.tmp")
PY
	echo "wrote $OUT/run-${run}.json"
done
