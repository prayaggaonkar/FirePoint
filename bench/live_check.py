#!/usr/bin/env python3
"""Capped live Fireworks check. The API key is read from the environment and never written."""

import hashlib
import json
import os
import platform
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MODEL = "accounts/fireworks/models/nemotron-lightning-3p5-30b-a3b"
UPSTREAM = "https://api.fireworks.ai/inference"
MAX_TOKENS = 16
GEN_CAP = 25
PROMPT = "Reply with the single word ok"
BIN = Path("/tmp/firerouter-livecheck")
OUT = ROOT / "bench" / "results" / "live-check.json"

generated = 0
summary = {
    "date": "2026-11-20",
    "model": MODEL,
    "max_tokens": MAX_TOKENS,
    "machine": {
        "system": platform.system(),
        "release": platform.release(),
        "machine": platform.machine(),
        "python": platform.python_version(),
    },
    "calls": [],
}


class Stop(Exception):
    pass


def note(name, **fields):
    row = {"name": name}
    row.update(fields)
    summary["calls"].append(row)
    print(json.dumps(row), flush=True)


def spend():
    global generated
    generated += 1
    if generated > GEN_CAP:
        raise Stop(f"generation cap {GEN_CAP} exceeded")


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def headers_of(resp_headers):
    keep = {}
    for k, v in resp_headers.items():
        lk = k.lower()
        if lk in ("authorization", "set-cookie", "cookie"):
            continue
        if any(s in lk for s in ("key", "token", "secret")):
            continue
        if lk.startswith("x-") or lk == "retry-after":
            keep[k] = v
    return keep


def request(method, url, body=None, auth=None, timeout=90):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if auth:
        req.add_header("Authorization", auth)
    t0 = time.perf_counter()
    first = None
    raw = b""
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            code = resp.status
            hdrs = headers_of(resp.headers)
            if "text/event-stream" in resp.headers.get("Content-Type", ""):
                while True:
                    line = resp.readline()
                    if not line:
                        break
                    if first is None and line.startswith(b"data:"):
                        first = time.perf_counter() - t0
                    raw += line
            else:
                raw = resp.read()
    except urllib.error.HTTPError as e:
        code = e.code
        hdrs = headers_of(e.headers)
        raw = e.read()
    total = time.perf_counter() - t0
    return code, hdrs, raw, first, total


def parse_json(raw):
    try:
        return json.loads(raw)
    except Exception:
        return None


def usage_of(obj):
    if isinstance(obj, dict) and isinstance(obj.get("usage"), dict):
        return obj["usage"]
    return None


def error_of(obj):
    if isinstance(obj, dict) and isinstance(obj.get("error"), dict):
        err = obj["error"]
        return err.get("type"), err.get("message")
    return None, None


def stream_info(raw):
    text = raw.decode("utf-8", "replace")
    saw_done = "data: [DONE]" in text
    first_data = None
    usage = None
    data_lines = 0
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("data:"):
            continue
        data_lines += 1
        if line == "data: [DONE]":
            continue
        try:
            obj = json.loads(line[5:].strip())
        except Exception:
            continue
        if isinstance(obj, dict) and obj.get("usage"):
            usage = obj["usage"]
    return saw_done, data_lines, usage


def counter(text, name):
    """Sum one Prometheus counter. Ignores help text and _created timestamps."""
    total = 0.0
    found = False
    for line in text.splitlines():
        if not line.startswith(name):
            continue
        rest = line[len(name):]
        if rest.startswith("_"):
            continue
        if rest[:1] not in ("{", " ", "\t"):
            continue
        total += float(line.split()[-1])
        found = True
    return found, total


def wait_healthy(base, proc):
    deadline = time.time() + 10
    while time.time() < deadline:
        if proc.poll() is not None:
            raise Stop("proxy exited during startup")
        try:
            code, _, raw, _, _ = request("GET", base + "/healthz")
            if code == 200:
                return
        except Exception:
            pass
        time.sleep(0.05)
    raise Stop("proxy did not become healthy")


class Proxy:
    def __init__(self, extra):
        self.port = free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        self.log_path = Path(f"/tmp/firerouter-livecheck-{self.port}.log")
        self.log = open(self.log_path, "w")
        cmd = [
            str(BIN),
            "-listen",
            f"127.0.0.1:{self.port}",
            "-upstream",
            UPSTREAM,
            *extra,
        ]
        self.proc = subprocess.Popen(cmd, stdout=self.log, stderr=subprocess.STDOUT)
        wait_healthy(self.base, self.proc)

    def stop(self):
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self.log.close()

    def upstream_total(self):
        code, _, raw, _, _ = request("GET", self.base + "/metrics")
        if code != 200:
            return None
        _, total = counter(raw.decode(), "firerouter_upstream_status_total")
        return total

    def sample(self, name):
        text = request("GET", self.base + "/metrics")[2].decode()

        def count_line(prefix):
            found, total = counter(text, prefix)
            return total if found else 0.0

        note(
            name,
            ttft_count=count_line("firerouter_ttft_seconds_count"),
            overhead_count=count_line("firerouter_proxy_overhead_seconds_count"),
        )


def chat_body(model, stream):
    body = {
        "model": model,
        "max_tokens": MAX_TOKENS,
        "messages": [{"role": "user", "content": PROMPT}],
    }
    if stream:
        body["stream"] = True
    return body


def record_chat(name, code, hdrs, raw, first, total, stream):
    obj = parse_json(raw) if not stream else None
    usage = None
    saw_done = False
    data_lines = None
    err_type, err_msg = (None, None)
    if stream:
        saw_done, data_lines, usage = stream_info(raw)
    else:
        usage = usage_of(obj)
        err_type, err_msg = error_of(obj)
    generating = code == 200
    if generating:
        spend()
    note(
        name,
        status=code,
        seconds=round(total, 3),
        first_data_seconds=None if first is None else round(first, 3),
        first_before_end=first is not None and (total - first) > 0.001,
        saw_done=saw_done,
        data_lines=data_lines,
        served=hdrs.get("X-Firerouter-Model") or hdrs.get("X-FireRouter-Model"),
        has_usage=usage is not None,
        usage=usage,
        error_type=err_type,
        error_message=err_msg,
        generating=generating,
    )
    if code == 401 and name != "missing_virtual_key":
        raise Stop("401; stopping")
    return usage


def main():
    if not os.environ.get("FIREWORKS_API_KEY", "").strip():
        raise Stop("FIREWORKS_API_KEY is not set")
    subprocess.check_call(["go", "build", "-o", str(BIN), "./cmd/firerouter"], cwd=ROOT)
    key = "fr-livecheck-virtual"
    key_hash = hashlib.sha256(key.encode()).hexdigest()
    keys_path = Path("/tmp/firerouter-livecheck-keys.json")
    keys_path.write_text(json.dumps({
        "tenants": [{
            "name": "live",
            "key_sha256": key_hash,
            "rpm": 10,
            "tpm": 100000,
            "allowed_models": [MODEL],
        }]
    }))

    proxies = []
    try:
        proxy = Proxy(["-rpm", "100000", "-tpm", "10000000"])
        proxies.append(proxy)
        code, hdrs, raw, first, total = request("GET", proxy.base + "/v1/models")
        text = raw.decode("utf-8", "replace")
        if code == 401:
            raise Stop("401 on model list")
        note("models", status=code, contains_model=MODEL in text, body_bytes=len(raw), generating=False)

        record_chat("chat_nonstream", *request("POST", proxy.base + "/v1/chat/completions", chat_body(MODEL, False)), stream=False)
        record_chat("chat_stream", *request("POST", proxy.base + "/v1/chat/completions", chat_body(MODEL, True)), stream=True)

        before = proxy.upstream_total()
        code, hdrs, raw, first, total = request(
            "POST",
            proxy.base + "/v1/completions",
            {"model": MODEL, "prompt": "ok", "max_tokens": MAX_TOKENS},
        )
        after = proxy.upstream_total()
        if code == 200:
            spend()
        note("completions", status=code, upstream_delta=None if before is None or after is None else after - before, error_message=error_of(parse_json(raw))[1], generating=code == 200)

        before = proxy.upstream_total()
        code, hdrs, raw, first, total = request(
            "POST",
            proxy.base + "/v1/embeddings",
            {"model": MODEL, "input": "ok"},
        )
        after = proxy.upstream_total()
        if code == 200:
            spend()
        note("embeddings", status=code, upstream_delta=None if before is None or after is None else after - before, error_message=error_of(parse_json(raw))[1], generating=code == 200)

        proxy.stop()
        proxies.remove(proxy)

        inj = Proxy(["-rpm", "100000", "-tpm", "10000000", "-inject-usage"])
        proxies.append(inj)
        record_chat("inject_usage_stream", *request("POST", inj.base + "/v1/chat/completions", chat_body(MODEL, True)), stream=True)
        inj.stop()
        proxies.remove(inj)

        keyed = Proxy(["-rpm", "100000", "-tpm", "10000000", "-keys", str(keys_path)])
        proxies.append(keyed)
        before = keyed.upstream_total()
        code, hdrs, raw, first, total = request("POST", keyed.base + "/v1/chat/completions", chat_body(MODEL, False))
        after = keyed.upstream_total()
        record_chat("missing_virtual_key", code, hdrs, raw, first, total, stream=False)
        note("missing_virtual_key_upstream", upstream_delta=None if before is None or after is None else after - before)

        before = keyed.upstream_total()
        code, hdrs, raw, first, total = request(
            "POST", keyed.base + "/v1/chat/completions", chat_body("not-allowed-model", False), auth="Bearer " + key
        )
        after = keyed.upstream_total()
        record_chat("model_not_allowed", code, hdrs, raw, first, total, stream=False)
        note("model_not_allowed_upstream", upstream_delta=None if before is None or after is None else after - before)

        record_chat(
            "virtual_key_chat",
            *request("POST", keyed.base + "/v1/chat/completions", chat_body(MODEL, False), auth="Bearer " + key),
            stream=False,
        )
        keyed.stop()
        proxies.remove(keyed)

        limited = Proxy(["-rpm", "1", "-tpm", "1000000"])
        proxies.append(limited)
        before = limited.upstream_total()
        record_chat("rpm_first", *request("POST", limited.base + "/v1/chat/completions", chat_body(MODEL, False)), stream=False)
        code, hdrs, raw, first, total = request("POST", limited.base + "/v1/chat/completions", chat_body(MODEL, False))
        after = limited.upstream_total()
        record_chat("rpm_second", code, hdrs, raw, first, total, stream=False)
        note(
            "rpm_upstream",
            before=before,
            after=after,
            upstream_delta=None if before is None or after is None else after - before,
            retry_after=hdrs.get("Retry-After"),
        )
        limited.stop()
        proxies.remove(limited)

        measured = Proxy(["-rpm", "100000", "-tpm", "10000000"])
        proxies.append(measured)

        def percentile(values, p):
            if not values:
                return None
            ordered = sorted(values)
            index = round((p / 100) * (len(ordered) - 1))
            index = max(0, min(len(ordered) - 1, index))
            return round(ordered[index] * 1000, 1)

        direct = []
        via = []
        direct_tokens = 0
        via_tokens = 0
        for i in range(8):
            code, hdrs, raw, first, total = request(
                "POST",
                UPSTREAM + "/v1/chat/completions",
                chat_body(MODEL, True),
                auth="Bearer " + os.environ["FIREWORKS_API_KEY"],
            )
            usage = record_chat(f"direct_{i}", code, hdrs, raw, first, total, stream=True)
            if code != 200:
                raise Stop(f"direct sample {i} returned {code}")
            if first is not None:
                direct.append(first)
            if usage:
                direct_tokens += int(usage.get("total_tokens") or 0)
        for i in range(8):
            code, hdrs, raw, first, total = request(
                "POST", measured.base + "/v1/chat/completions", chat_body(MODEL, True)
            )
            usage = record_chat(f"proxy_{i}", code, hdrs, raw, first, total, stream=True)
            if code != 200:
                raise Stop(f"proxy sample {i} returned {code}")
            if first is not None:
                via.append(first)
            if usage:
                via_tokens += int(usage.get("total_tokens") or 0)
        note(
            "ttft_sample",
            n_direct=len(direct),
            n_proxy=len(via),
            direct_p50_ms=percentile(direct, 50),
            direct_p90_ms=percentile(direct, 90),
            proxy_p50_ms=percentile(via, 50),
            proxy_p90_ms=percentile(via, 90),
            overhead_p50_ms=None
            if not direct or not via
            else round(percentile(via, 50) - percentile(direct, 50), 1),
            direct_total_tokens=direct_tokens,
            proxy_total_tokens=via_tokens,
        )
        measured.sample("metrics")
    finally:
        for p in proxies:
            p.stop()
        summary["generated"] = generated
        OUT.parent.mkdir(parents=True, exist_ok=True)
        OUT.write_text(json.dumps(summary, indent=2) + "\n")
        print(f"wrote {OUT}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Stop as exc:
        summary["stopped"] = str(exc)
        OUT.parent.mkdir(parents=True, exist_ok=True)
        OUT.write_text(json.dumps(summary, indent=2) + "\n")
        print(f"stopped: {exc}", flush=True)
        raise SystemExit(1)
