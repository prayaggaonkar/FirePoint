#!/usr/bin/env python3
"""Official OpenAI SDK against FireRouter and the mock. No real API key."""

import os
import sys
import time

from openai import OpenAI

MODEL = "test"
MESSAGES = [{"role": "user", "content": "hi"}]
TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "lookup",
            "description": "Look up a value",
            "parameters": {
                "type": "object",
                "properties": {"q": {"type": "string"}},
                "required": ["q"],
            },
        },
    }
]


def env_int(name):
    raw = os.environ.get(name, "")
    if not raw:
        fail(f"{name} is required")
    return int(raw)


def expected_text():
    return "".join(f"t{i}" for i in range(env_int("MOCK_TOKENS")))


def full_stream_seconds():
    tokens = env_int("MOCK_TOKENS")
    first = env_int("MOCK_FIRST_MS") / 1000
    interval = env_int("MOCK_INTERVAL_MS") / 1000
    if tokens < 1:
        return first
    return first + max(tokens - 1, 0) * interval


def delta_text(chunk):
    if not chunk.choices:
        return ""
    delta = chunk.choices[0].delta
    if delta is None or not delta.content:
        return ""
    return delta.content


def fail(message):
    print(f"FAIL {message}", file=sys.stderr)
    sys.exit(1)


def disconnects():
    path = os.environ.get("MOCK_LOG", "")
    if not path:
        fail("MOCK_LOG is required")
    with open(path, encoding="utf-8", errors="replace") as fh:
        return fh.read().count("client disconnected")


def wait_disconnect(before):
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        if disconnects() > before:
            return True
        time.sleep(0.05)
    return False


def test_non_stream(client):
    resp = client.chat.completions.create(model=MODEL, messages=MESSAGES)
    content = resp.choices[0].message.content
    if content != expected_text():
        fail(f"non-stream content {content!r}")
    if resp.choices[0].finish_reason != "stop":
        fail(f"non-stream finish_reason {resp.choices[0].finish_reason!r}")
    if resp.model != MODEL:
        fail(f"non-stream model {resp.model!r}")
    print("ok non-stream")


def test_stream(client):
    parts = []
    # Count content chunks so a buffered body cannot pass as a stream.
    for chunk in client.chat.completions.create(model=MODEL, messages=MESSAGES, stream=True):
        text = delta_text(chunk)
        if text:
            parts.append(text)
    joined = "".join(parts)
    if joined != expected_text():
        fail(f"stream content {joined!r}")
    if len(parts) != env_int("MOCK_TOKENS"):
        fail(f"stream chunks {len(parts)}, want {env_int('MOCK_TOKENS')}")
    print("ok stream")


def test_tools(client):
    resp = client.chat.completions.create(
        model=MODEL,
        messages=MESSAGES,
        tools=TOOLS,
        tool_choice="auto",
    )
    content = resp.choices[0].message.content
    if content != expected_text():
        fail(f"tools content {content!r}")
    print("ok tools")


def test_cancel(client):
    before = disconnects()
    started = time.monotonic()
    parts = []
    stream = client.chat.completions.create(model=MODEL, messages=MESSAGES, stream=True)
    try:
        for chunk in stream:
            text = delta_text(chunk)
            if not text:
                continue
            parts.append(text)
            # Drop the connection while later tokens are still scheduled.
            stream.close()
    except Exception as exc:
        if not parts:
            raise
        print(f"ok cancel closed the stream ({type(exc).__name__})")
    else:
        print("ok cancel closed the stream")
    elapsed = time.monotonic() - started
    joined = "".join(parts)
    full = expected_text()
    if not joined or not full.startswith(joined) or joined == full:
        fail(f"cancel received {joined!r}, full stream is {full!r}")
    # The mock spaces tokens out. A real cancel returns before that schedule ends.
    limit = full_stream_seconds() * 0.75
    if elapsed >= limit:
        fail(f"cancel took {elapsed:.3f}s, full stream is {full_stream_seconds():.3f}s")
    if not wait_disconnect(before):
        fail("mock did not see the client disconnect")
    print(f"ok cancel after {joined!r} in {elapsed:.3f}s")


def main():
    base = os.environ.get("FIREROUTER_BASE_URL", "")
    if not base:
        fail("FIREROUTER_BASE_URL is required")
    client = OpenAI(base_url=base, api_key="firerouter-compat", max_retries=0, timeout=10.0)
    try:
        test_non_stream(client)
        test_stream(client)
        test_tools(client)
        test_cancel(client)
    finally:
        client.close()
    print("python compat ok")


if __name__ == "__main__":
    main()
