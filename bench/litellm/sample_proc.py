#!/usr/bin/env python3
"""Sample a process tree during the measured window only.

The caller starts this at the same moment as loadgen. This process sleeps
through the warmup, then records. CPU percent is psutil's since the previous
call on the same Process object, summed over the root and its children.
A new Process object reports 0 until it has been sampled once, so objects
are kept. 100 means one core.
"""
import argparse
import json
import time

import psutil


def median(values):
    if not values:
        return None
    ordered = sorted(values)
    mid = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[mid]
    return (ordered[mid - 1] + ordered[mid]) / 2


def live(pid, known):
    """Return the current tree, reusing Process objects so CPU deltas work."""
    try:
        root = psutil.Process(pid)
    except psutil.Error as exc:
        known.clear()
        return [], str(exc)
    found = [root]
    try:
        found.extend(root.children(recursive=True))
    except psutil.Error:
        pass
    current = {}
    for proc in found:
        current[proc.pid] = known.get(proc.pid, proc)
    known.clear()
    known.update(current)
    return list(current.values()), ""


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", type=int, required=True)
    parser.add_argument("--warmup", type=float, required=True)
    parser.add_argument("--duration", type=float, required=True)
    parser.add_argument("--interval", type=float, default=0.5)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    known = {}
    time.sleep(args.warmup)
    procs, err = live(args.pid, known)
    for proc in procs:
        try:
            proc.cpu_percent(interval=None)
        except psutil.Error:
            pass

    samples = []
    deadline = time.time() + args.duration
    while time.time() < deadline:
        time.sleep(min(args.interval, max(0.0, deadline - time.time())))
        if time.time() > deadline:
            break
        procs, err = live(args.pid, known)
        cpu = 0.0
        rss = 0
        pids = []
        for proc in procs:
            try:
                # The first call on a process that appeared this sample is
                # only a baseline. It is included and can be zero.
                cpu += proc.cpu_percent(interval=None)
                rss += proc.memory_info().rss
                pids.append(proc.pid)
            except psutil.Error:
                continue
        samples.append({"cpu_percent": cpu, "rss_bytes": rss, "pids": pids})

    cpus = [sample["cpu_percent"] for sample in samples]
    rsss = [sample["rss_bytes"] for sample in samples]
    counts = [len(sample["pids"]) for sample in samples]
    body = {
        "root_pid": args.pid,
        "samples": len(samples),
        "process_count_max": max(counts) if counts else 0,
        "cpu_percent_median": median(cpus),
        "cpu_percent_max": max(cpus) if cpus else None,
        "rss_bytes_median": median(rsss),
        "rss_bytes_max": max(rsss) if rsss else None,
        "error": err,
    }
    with open(args.out, "w") as fh:
        json.dump(body, fh)
        fh.write("\n")


if __name__ == "__main__":
    main()
