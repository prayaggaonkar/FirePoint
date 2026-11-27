#!/usr/bin/env bash
# Five paired trials. Each trial is one loadgen run straight at the mock,
# then the same run through FirePoint. Warmup is discarded by loadgen.
# One JSON file is written per trial, under bench/results.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

RUNS=${BENCH_RUNS:-5}
RATE=${BENCH_RATE:-20}
DURATION=${BENCH_DURATION:-30s}
WARMUP=${BENCH_WARMUP:-10s}
CONCURRENCY=${BENCH_CONCURRENCY:-64}
TOKENS=${BENCH_TOKENS:-50}
MOCK_ADDR=${BENCH_MOCK_ADDR:-127.0.0.1:19110}
PROXY_ADDR=${BENCH_PROXY_ADDR:-127.0.0.1:18090}
OUT=${BENCH_OUT:-bench/results}

mock_pid=""
proxy_pid=""

cleanup() {
	if [[ -n $proxy_pid ]]; then
		kill "$proxy_pid" 2>/dev/null || true
		wait "$proxy_pid" 2>/dev/null || true
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
	local i
	for i in $(seq 1 50); do
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
	return 1
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

./bin/firepoint \
	-listen "$PROXY_ADDR" \
	-upstream "http://$MOCK_ADDR" \
	-rpm 1000000 \
	-tpm 100000000 \
	>"$OUT/firepoint.log" 2>&1 &
proxy_pid=$!

wait_http "http://$MOCK_ADDR/healthz" "$mock_pid" "$OUT/mock.log"
wait_http "http://$PROXY_ADDR/healthz" "$proxy_pid" "$OUT/firepoint.log"

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
export GO_VERSION CPU_MODEL CORES OS_NAME OUT

python3 - <<'PY'
import json, os
json.dump({
    "go_version": os.environ["GO_VERSION"],
    "cpu_model": os.environ["CPU_MODEL"] or "unknown",
    "cores": int(os.environ["CORES"]),
    "os": os.environ["OS_NAME"],
}, open(os.environ["OUT"] + "/environment.json", "w"), indent=2)
open(os.environ["OUT"] + "/environment.json", "a").write("\n")
PY

direct_url="http://$MOCK_ADDR/v1/chat/completions"
proxy_url="http://$PROXY_ADDR/v1/chat/completions"

for run in $(seq 1 "$RUNS"); do
	echo "run $run/$RUNS direct"
	./bin/loadgen \
		-url "$direct_url" \
		-model bench \
		-rate "$RATE" \
		-duration "$DURATION" \
		-warmup "$WARMUP" \
		-concurrency "$CONCURRENCY" \
		-json "$OUT/run-${run}-direct.tmp"
	echo "run $run/$RUNS proxied"
	./bin/loadgen \
		-url "$proxy_url" \
		-model bench \
		-rate "$RATE" \
		-duration "$DURATION" \
		-warmup "$WARMUP" \
		-concurrency "$CONCURRENCY" \
		-json "$OUT/run-${run}-proxy.tmp"
	RUN="$run" OUT="$OUT" python3 - <<'PY'
import json, os
out = os.environ["OUT"]
run = os.environ["RUN"]
env = json.load(open(out + "/environment.json"))
direct = json.load(open(f"{out}/run-{run}-direct.tmp"))
proxied = json.load(open(f"{out}/run-{run}-proxy.tmp"))
body = {"run": int(run), "environment": env, "direct": direct, "proxied": proxied}
with open(f"{out}/run-{run}.json", "w") as fh:
    json.dump(body, fh, indent=2)
    fh.write("\n")
os.remove(f"{out}/run-{run}-direct.tmp")
os.remove(f"{out}/run-{run}-proxy.tmp")
PY
	echo "wrote $OUT/run-${run}.json"
done
