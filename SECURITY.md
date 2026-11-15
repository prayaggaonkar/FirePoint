# Security

Report a vulnerability in private. Use a [GitHub security advisory](https://github.com/prayaggaonkar/FireRouter/security/advisories/new) for this repository.

A public GitHub issue, pull request, or discussion is the wrong place. Those are visible before a fix exists.

Include:

- The version (`firerouter --version`) or the image tag and commit.
- What an attacker can do, and what they already need (a virtual key, a network position, a crafted body).
- A way to reproduce it against the mock upstream. Leave out live Fireworks keys, tenant keys, and prompts.

The proxy is meant to keep prompts and API keys out of logs and out of metric labels. A report that shows a key or a prompt in a log line, a metric, or an upstream request that should not have carried the caller's key is in scope.

There is no published response window.
