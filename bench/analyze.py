#!/usr/bin/env python3
"""Print a markdown summary of bench/run_bench.sh result files."""

import json
import sys
from pathlib import Path

PERCENTILES = ("p50", "p90", "p99", "p99.9")
SERIES = (("TTFT", "ttft_ms"), ("inter-token", "inter_token_ms"))


def median(values):
    ordered = sorted(values)
    count = len(ordered)
    if count == 0:
        raise SystemExit("no runs to summarize")
    mid = count // 2
    if count % 2 == 1:
        return ordered[mid]
    return (ordered[mid - 1] + ordered[mid]) / 2


def load_runs(directory):
    paths = sorted(Path(directory).glob("run-*.json"))
    if not paths:
        raise SystemExit(f"no run-*.json files in {directory}")
    return [json.loads(path.read_text()) for path in paths]


def cell(value):
    return f"{value:.3f}"


def series_rows(runs, key):
    rows = []
    for name in PERCENTILES:
        direct = [run["direct"][key][name] for run in runs]
        proxied = [run["proxied"][key][name] for run in runs]
        direct_mid = median(direct)
        proxied_mid = median(proxied)
        rows.append(
            (
                name,
                direct_mid,
                min(direct),
                max(direct),
                proxied_mid,
                min(proxied),
                max(proxied),
                proxied_mid - direct_mid,
            )
        )
    return rows


def environment_lines(runs):
    seen = []
    for run in runs:
        env = run.get("environment")
        if env and env not in seen:
            seen.append(env)
    if not seen:
        return ["Environment was not recorded with these runs."]
    lines = []
    if len(seen) > 1:
        lines.append("Runs were not all recorded on the same machine.")
    for env in seen:
        lines.append(f"- Go: {env.get('go_version', 'unknown')}")
        lines.append(f"- CPU: {env.get('cpu_model', 'unknown')}")
        lines.append(f"- Cores: {env.get('cores', 'unknown')}")
        lines.append(f"- OS: {env.get('os', 'unknown')}")
    return lines


def problems(runs):
    notes = []
    for run in runs:
        for label in ("direct", "proxied"):
            report = run[label]
            if report.get("errors") or report.get("status_429"):
                notes.append(
                    f"run {run.get('run', '?')} {label}: "
                    f"{report.get('errors', 0)} errors, {report.get('status_429', 0)} 429s"
                )
    return notes


def render(runs):
    lines = ["# Benchmark", "", "## Machine", ""]
    lines.extend(environment_lines(runs))
    lines.append("")
    lines.append(
        "Delta is the proxied median minus the direct median, in milliseconds. "
        "Min and max are across runs."
    )
    lines.append("")
    header = (
        "| metric | percentile | direct median | direct min | direct max "
        "| proxied median | proxied min | proxied max | delta |"
    )
    rule = "|---|---|---:|---:|---:|---:|---:|---:|---:|"
    lines.extend([header, rule])
    for label, key in SERIES:
        for row in series_rows(runs, key):
            name, d_mid, d_min, d_max, p_mid, p_min, p_max, delta = row
            lines.append(
                f"| {label} | {name} | {cell(d_mid)} | {cell(d_min)} | {cell(d_max)} "
                f"| {cell(p_mid)} | {cell(p_min)} | {cell(p_max)} | {cell(delta)} |"
            )
    notes = problems(runs)
    if notes:
        lines.extend(["", "## Run problems", ""])
        lines.extend(f"- {note}" for note in notes)
    lines.append("")
    return "\n".join(lines)


def main(argv):
    directory = argv[1] if len(argv) > 1 else "bench/results"
    print(render(load_runs(directory)), end="")


if __name__ == "__main__":
    main(sys.argv)
