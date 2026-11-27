#!/usr/bin/env python3
"""Print markdown tables from bench/litellm/results.

Reads run-*.json written by run_compare.sh under
results/<scenario>/workers-<label>/rps-<rate>/. One section per scenario,
requested rate, and measured duration, so a 5s file is not combined with a
30s file. Median across run files matches bench/analyze.py: odd counts take
the middle value, even counts take the mean of the two middle values.
"""

import json
import sys
from pathlib import Path

PERCENTILES = ("p50", "p90", "p99", "p99.9")
PATHS = ("direct", "firepoint", "litellm")
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


def num(value):
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        return json.dumps(value)
    return json.dumps(value, sort_keys=True)


def ms(value):
    # Same 3 decimal places as bench/analyze.py. Inputs are already on that grid.
    return f"{value:.3f}"


def present(report, key):
    if key not in report or report[key] is None:
        return ""
    return num(report[key])


def load_completed(root):
    runs = []
    for path in sorted(root.glob("*/workers-*/rps-*/run-*.json")):
        body = json.loads(path.read_text())
        body["_path"] = str(path)
        runs.append(body)
    return runs


def group_key(run):
    direct = run["direct"]
    return (
        run.get("scenario", ""),
        float(run.get("requested_rps", direct.get("rate_per_sec"))),
        float(direct["duration_seconds"]),
        run.get("litellm_workers", ""),
        int(run.get("litellm_worker_count", 0)),
    )


def section_key(key):
    scenario, rate, duration, _label, _count = key
    return (scenario, rate, duration)


def percentile_values(runs, path, series, name):
    return [run[path][series][name] for run in runs]


def ranges_line(run, metric, name, path, left, direct):
    left_min, left_max = min(left), max(left)
    direct_min, direct_max = min(direct), max(direct)
    overlap = left_min <= direct_max and direct_min <= left_max
    base = (
        "compare scenario={scenario} requested_rps={rate} duration_s={duration} "
        "litellm_workers={label} count={count} {metric} {name}: "
        "{path} min {left_min} max {left_max}, "
        "direct min {direct_min} max {direct_max}".format(
            scenario=run.get("scenario", ""),
            rate=num(run.get("requested_rps")),
            duration=num(run["direct"]["duration_seconds"]),
            label=run.get("litellm_workers", ""),
            count=num(run.get("litellm_worker_count")),
            metric=metric,
            name=name,
            path=path,
            left_min=ms(left_min),
            left_max=ms(left_max),
            direct_min=ms(direct_min),
            direct_max=ms(direct_max),
        )
    )
    if overlap:
        return base + ", ranges overlap"
    if left_max < direct_min:
        relation = "lower"
    else:
        relation = "higher"
    return base + f", ranges do not overlap, {path} is {relation}"


def latency_table(runs):
    label = runs[0].get("litellm_workers", "")
    count = runs[0].get("litellm_worker_count", "")
    lines = []
    for metric, series in SERIES:
        lines.append(f"### {metric}")
        lines.append("")
        lines.append(
            "| litellm_workers | count | path | run files | "
            "p50 median | p50 min | p50 max | "
            "p90 median | p90 min | p90 max | "
            "p99 median | p99 min | p99 max | "
            "p99.9 median | p99.9 min | p99.9 max | "
            "overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |"
        )
        lines.append(
            "|---|---:|---|---:|"
            "---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"
            "---:|---:|---:|---:|"
        )
        direct_medians = {}
        for name in PERCENTILES:
            direct_medians[name] = median(percentile_values(runs, "direct", series, name))
        for path in PATHS:
            cells = [label, num(count), path, str(len(runs))]
            overheads = []
            for name in PERCENTILES:
                samples = percentile_values(runs, path, series, name)
                mid = median(samples)
                cells.extend([ms(mid), ms(min(samples)), ms(max(samples))])
                overheads.append(ms(mid - direct_medians[name]))
            cells.extend(overheads)
            lines.append("| " + " | ".join(cells) + " |")
        lines.append("")
        for name in PERCENTILES:
            direct_samples = percentile_values(runs, "direct", series, name)
            for path in ("firepoint", "litellm"):
                lines.append(
                    ranges_line(
                        runs[0],
                        metric,
                        name,
                        path,
                        percentile_values(runs, path, series, name),
                        direct_samples,
                    )
                )
        lines.append("")
    return lines


def trial_table(runs):
    lines = [
        "### Throughput and errors",
        "",
        "| litellm_workers | count | run | path | requests | errors | status_429 | "
        "concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |",
        "|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|",
    ]
    for run in runs:
        for path in PATHS:
            report = run[path]
            lines.append(
                "| {label} | {count} | {run_n} | {path} | {requests} | {errors} | {denied} | "
                "{cap} | {codes} | {kinds} | {recorded} | {successful} |".format(
                    label=run.get("litellm_workers", ""),
                    count=num(run.get("litellm_worker_count")),
                    run_n=run.get("run", ""),
                    path=path,
                    requests=present(report, "requests"),
                    errors=present(report, "errors"),
                    denied=present(report, "status_429"),
                    cap=present(report, "concurrency_cap"),
                    codes=present(report, "status_codes"),
                    kinds=present(report, "error_kinds"),
                    recorded=present(report, "recorded_per_s"),
                    successful=present(report, "successful_per_s"),
                )
            )
    lines.append("")
    return lines


def resource_table(runs):
    lines = [
        "### CPU and RSS",
        "",
        "| litellm_workers | count | run | path | samples | process_count_max | "
        "cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |",
        "|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|",
    ]
    for run in runs:
        for path in PATHS:
            resource = run[path].get("resource") or {}
            lines.append(
                "| {label} | {count} | {run_n} | {path} | {samples} | {procs} | "
                "{cpu_med} | {cpu_max} | {rss_med} | {rss_max} | {error} |".format(
                    label=run.get("litellm_workers", ""),
                    count=num(run.get("litellm_worker_count")),
                    run_n=run.get("run", ""),
                    path=path,
                    samples=num(resource.get("samples")),
                    procs=num(resource.get("process_count_max")),
                    cpu_med=num(resource.get("cpu_percent_median")),
                    cpu_max=num(resource.get("cpu_percent_max")),
                    rss_med=num(resource.get("rss_bytes_median")),
                    rss_max=num(resource.get("rss_bytes_max")),
                    error=resource.get("error") or "",
                )
            )
    lines.append("")
    return lines


def failover_table(runs):
    if not any("mock" in run.get(path, {}) for run in runs for path in PATHS):
        return []
    lines = [
        "### Failover counters",
        "",
        "Mock counts are the `mock.models` object stored on the run. "
        "`first_5s` and `steady` are the loadgen split windows stored on the run.",
        "",
        "| litellm_workers | count | run | path | mock.models | "
        "first_5s requests | first_5s errors | first_5s status_429 | "
        "steady requests | steady errors | steady status_429 |",
        "|---|---:|---:|---|---|---:|---:|---:|---:|---:|---:|",
    ]
    for run in runs:
        for path in PATHS:
            report = run[path]
            window_first = report.get("first_5s") or {}
            window_steady = report.get("steady") or {}
            models = (report.get("mock") or {}).get("models")
            lines.append(
                "| {label} | {count} | {run_n} | {path} | {models} | "
                "{first_requests} | {first_errors} | {first_denied} | "
                "{steady_requests} | {steady_errors} | {steady_denied} |".format(
                    label=run.get("litellm_workers", ""),
                    count=num(run.get("litellm_worker_count")),
                    run_n=run.get("run", ""),
                    path=path,
                    models=num(models),
                    first_requests=num(window_first.get("requests")),
                    first_errors=num(window_first.get("errors")),
                    first_denied=num(window_first.get("status_429")),
                    steady_requests=num(window_steady.get("requests")),
                    steady_errors=num(window_steady.get("errors")),
                    steady_denied=num(window_steady.get("status_429")),
                )
            )
    lines.append("")
    return lines


def render_groups(runs):
    grouped = {}
    for run in runs:
        grouped.setdefault(group_key(run), []).append(run)
    sections = {}
    for key, members in grouped.items():
        sections.setdefault(section_key(key), []).append((key, members))
    lines = []
    for sec in sorted(sections):
        scenario, rate, duration = sec
        lines.append(
            f"## Scenario {scenario}, requested {num(rate)} rps, duration {num(duration)}s"
        )
        lines.append("")
        for key, members in sorted(sections[sec], key=lambda item: ({"default": 0, "cpu": 1}.get(item[0][3], 9), item[0][3], item[0][4])):
            members = sorted(members, key=lambda run: run.get("run", 0))
            label = key[3]
            count = key[4]
            warmup = members[0]["direct"].get("warmup_seconds")
            concurrency = members[0].get("concurrency")
            lines.append(
                f"### litellm_workers={label} count={count}, "
                f"{len(members)} run file(s), warmup {num(warmup)}s, concurrency {num(concurrency)}"
            )
            lines.append("")
            lines.extend(latency_table(members))
            lines.extend(trial_table(members))
            lines.extend(resource_table(members))
            lines.extend(failover_table(members))
    return lines


def incomplete(root):
    lines = ["## Run files that were not combined", ""]
    found = False
    for path in sorted(root.glob("*/workers-*/rps-*/*")):
        name = path.name
        if not (name.endswith(".tmp") or name.endswith(".resource") or name.endswith(".counts")):
            continue
        found = True
        if name.endswith(".tmp") or name.endswith(".resource") or name.endswith(".counts"):
            try:
                body = json.loads(path.read_text())
            except json.JSONDecodeError:
                body = None
            if isinstance(body, dict) and name.endswith(".tmp"):
                lines.append(
                    "- `{path}` requests {requests} errors {errors} status_429 {denied} "
                    "concurrency_cap {cap} status_codes {codes} error_kinds {kinds} "
                    "duration_seconds {duration}".format(
                        path=path.as_posix(),
                        requests=num(body.get("requests")),
                        errors=num(body.get("errors")),
                        denied=num(body.get("status_429")),
                        cap=present(body, "concurrency_cap"),
                        codes=present(body, "status_codes"),
                        kinds=present(body, "error_kinds"),
                        duration=num(body.get("duration_seconds")),
                    )
                )
            elif isinstance(body, dict) and name.endswith(".resource"):
                lines.append(
                    "- `{path}` samples {samples} process_count_max {procs} "
                    "cpu_percent_median {cpu_med} cpu_percent_max {cpu_max} "
                    "rss_bytes_median {rss_med} rss_bytes_max {rss_max} error {error}".format(
                        path=path.as_posix(),
                        samples=num(body.get("samples")),
                        procs=num(body.get("process_count_max")),
                        cpu_med=num(body.get("cpu_percent_median")),
                        cpu_max=num(body.get("cpu_percent_max")),
                        rss_med=num(body.get("rss_bytes_median")),
                        rss_max=num(body.get("rss_bytes_max")),
                        error=json.dumps(body.get("error") or ""),
                    )
                )
            else:
                lines.append(f"- `{path.as_posix()}`")
    if not found:
        lines.append("None.")
    lines.append("")
    return lines


def skipped(root):
    lines = ["## Run files outside workers/rps directories", ""]
    paths = []
    for path in sorted(root.glob("run-*.json")) + sorted(root.glob("*/run-*.json")):
        if "workers-" in path.as_posix():
            continue
        paths.append(path)
    if not paths:
        lines.append("None.")
        lines.append("")
        return lines
    lines.append("These files are not in the tables above.")
    lines.append("")
    for path in paths:
        body = json.loads(path.read_text())
        direct = body.get("direct") or {}
        lines.append(
            "- `{path}` scenario {scenario} duration_seconds {duration} "
            "litellm_workers {workers} requests direct {direct_n} firepoint {fire_n} litellm {lite_n}".format(
                path=path.as_posix(),
                scenario=json.dumps(body.get("scenario")),
                duration=num(direct.get("duration_seconds")),
                workers=json.dumps(body.get("litellm_workers")),
                direct_n=num(direct.get("requests")),
                fire_n=num((body.get("firepoint") or {}).get("requests")),
                lite_n=num((body.get("litellm") or {}).get("requests")),
            )
        )
    lines.append("")
    return lines


def environments(root):
    lines = ["## Environment files", ""]
    paths = sorted(root.glob("*/environment.json"))
    if not paths:
        lines.append("None.")
        lines.append("")
        return lines
    for path in paths:
        body = json.loads(path.read_text())
        lines.append(f"### `{path.as_posix()}`")
        lines.append("")
        lines.append("```json")
        lines.append(json.dumps(body, indent=2, sort_keys=True))
        lines.append("```")
        lines.append("")
    return lines


def render(root):
    runs = load_completed(root)
    if not runs:
        raise SystemExit(f"no workers/rps run-*.json files in {root}")
    lines = [f"# LiteLLM comparison tables from `{root.as_posix()}`", ""]
    lines.append(
        "Overhead is the path median minus the direct median, in the same units as the series. "
        "A compare line says ranges do not overlap only when one path's min and max sit entirely "
        "above or entirely below the direct min and max. "
        "concurrency_cap and error_kinds are blank when the JSON omits them. "
        "CPU percent is the field stored on the run. 100 means one core."
    )
    lines.append("")
    lines.extend(environments(root))
    lines.extend(render_groups(runs))
    lines.extend(incomplete(root))
    lines.extend(skipped(root))
    return "\n".join(lines)


def main(argv):
    directory = argv[1] if len(argv) > 1 else "bench/litellm/results"
    print(render(Path(directory)), end="")


if __name__ == "__main__":
    main(sys.argv)
