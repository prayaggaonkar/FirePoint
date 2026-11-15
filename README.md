# FireRouter

An open-source, low-latency streaming proxy that sits in front of Fireworks AI's OpenAI-compatible API.

## Quick start

Build the programs:

```bash
make build
```

### Mock upstream

In one terminal, start the makeshift Fireworks server:

```bash
./bin/mockupstream
```

`-admin-token` opens a small control API. Without the flag those routes stay closed. Send the token as `Authorization: Bearer`. `POST /admin/fail` with `{"model_contains":"primary","rate":1.0}` fails models whose name contains that text. `rate` is from 0 to 1, and the first matching rule wins. `GET /admin/state` lists the rules. `POST /admin/reset` clears them.

```bash
./bin/mockupstream -admin-token secret
curl -X POST localhost:9000/admin/fail \
  -H 'Authorization: Bearer secret' \
  -H 'Content-Type: application/json' \
  -d '{"model_contains":"primary","rate":1.0}'
```

In another, start the proxy. It listens on `:8080` and forwards to `http://localhost:9000`:

```bash
./bin/firerouter
```

```bash
curl -N -X POST localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

`-N` makes curl print each token as it arrives. Leave out `"stream": true` for one JSON answer instead of a stream. The same proxy also forwards `POST /v1/completions`, `POST /v1/embeddings`, and `GET /v1/models`.

`-inject-usage` is off unless you set it. When it is on, a streamed chat is forwarded with `stream_options.include_usage` set to true. The upstream then sends one extra usage chunk after the words, and the token refund uses that count. When the flag is off, the refund stays at the estimate made before the call, and the client does not get that extra chunk from FireRouter.

```bash
./bin/firerouter -inject-usage
```

### Real Fireworks

`FIREWORKS_API_KEY` is optional. When it is set, that is the only `Authorization` value sent upstream. The caller's key is never forwarded, whether or not the variable is set.

```bash
export FIREWORKS_API_KEY=your-key
./bin/firerouter -upstream https://api.fireworks.ai/inference
```

```bash
curl -N -X POST localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"accounts/fireworks/models/llama-v3p1-8b-instruct","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

The proxy calls `https://api.fireworks.ai/inference/v1/chat/completions`. The app does not need the real key.

### Tenant keys

`-keys` points at a JSON file. Each tenant has a name, the sha256 hash of a virtual key, a request limit, a token limit, and an optional list of models. A virtual key is `fr-` plus 32 random characters. The file stores the hash, not the key.

```bash
./bin/firerouter keygen --tenant alice --rpm 600 --tpm 200000
```

That prints the key once, then a JSON entry with the hash. Put the entry under `tenants`. The plaintext key is not saved.

```bash
./bin/firerouter -keys keys.json
```

A request sends `Authorization: Bearer fr-...`. FireRouter hashes it and looks it up. An unknown or missing key gets 401, and the body is not read. The tenant name is the rate-limit bucket and the `tenant` label on rate-limit rejections. `rpm` and `tpm` in the file are that tenant's limits. `allowed_models`, when set, rejects any other model with 403.

SIGHUP reads the file again. Streams that already started keep going. A file that does not parse leaves the previous keys in place.

With no `-keys` file, every caller is allowed, and `-rpm` / `-tpm` apply to each caller. Use a key file when the process holds a real Fireworks key. See `deploy/keys.example.json`. That sample digest is sha256 of the placeholder text `example-not-a-key` and will not authenticate a request. Do not commit real keys.

### Fallbacks

`-config` points at a JSON file. Each requested model is tried first. The names under `fallbacks` are tried in order when that model is skipped by its circuit breaker, errors before response headers, or returns a 5xx status. The response header `X-FireRouter-Model` is the model that served. After headers are sent, a mid-stream failure is not retried.

```json
{"models":{"big-broken":{"fallbacks":["small"]}}}
```

```bash
./bin/firerouter -config firerouter.json
```

The mock returns 503 when the model name contains `broken`, so `big-broken` is served by `small`.

### Metrics

`GET /metrics` is Prometheus text. It reports time-to-first-byte, proxy overhead, active event streams, upstream status codes, rate-limit rejections, and each model's breaker state. Prompts and API keys are not labels.

```bash
curl -s localhost:8080/metrics
```

### Load generator

`loadgen` sends streaming requests on a fixed rate, without waiting for the previous response. The first `-warmup` (default 10s) is discarded. TTFT is the time from sending the request until the first `data:` line.

```bash
./bin/loadgen -url http://127.0.0.1:8080/v1/chat/completions -rate 5 -duration 30s -warmup 10s -concurrency 8 -json loadgen.json
```

### Metrics stack

`docker compose up` builds the proxy and the mock, then starts Prometheus on port 9090 and Grafana on port 3000. Grafana's user and password are both `admin`. The FireRouter dashboard is already loaded. This compose file is for a laptop.

How the pieces fit is in [docs/architecture.md](docs/architecture.md). A measured overhead run is in [docs/benchmark.md](docs/benchmark.md).
