#!/usr/bin/env bash
# Capped Fireworks check. Refuses to run unless LIVE_CONFIRM=1.
# FIREWORKS_API_KEY is read from the environment and is never printed.
set -euo pipefail

if [[ "${LIVE_CONFIRM:-}" != "1" ]]; then
	echo "refusing: set LIVE_CONFIRM=1 to spend Fireworks credits" >&2
	exit 1
fi
if [[ -z "${FIREWORKS_API_KEY:-}" ]]; then
	echo "refusing: FIREWORKS_API_KEY is not set" >&2
	exit 1
fi

ROOT=$(cd "$(dirname "$0")/.." && pwd)
exec python3 "$ROOT/bench/live_check.py"
