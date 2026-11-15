#!/usr/bin/env python3
"""One chat completion. The demo changes only BASE_URL between runs."""

import os
import sys

from openai import OpenAI


def main():
    base = os.environ.get("BASE_URL", "").rstrip("/")
    key = os.environ.get("OPENAI_API_KEY", "")
    if not base or not key:
        sys.exit("BASE_URL and OPENAI_API_KEY are required")
    client = OpenAI(base_url=base, api_key=key)
    resp = client.chat.completions.create(
        model=os.environ.get("MODEL", "primary"),
        messages=[{"role": "user", "content": "Say hi."}],
    )
    text = resp.choices[0].message.content or ""
    print(f"base_url={base}")
    print(f"model={resp.model}")
    print(f"text={text}")


if __name__ == "__main__":
    main()
