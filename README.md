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

In another, start the proxy. It listens on `:8080` and forwards to `http://localhost:9000`:

```bash
./bin/firerouter
```

```bash
curl -N -X POST localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

`-N` makes curl print each token as it arrives.

### Real Fireworks

`FIREWORKS_API_KEY` is optional. When it is set, FireRouter sends that key upstream and ignores any `Authorization` header from the app.

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

`-keys` points at a JSON file. Each entry is a tenant name and the secret that tenant's app sends as `Authorization: Bearer <key>`. An unknown key gets 401, and the prompt is not read. The tenant name is the rate-limit bucket. With no `-keys` file, every caller is allowed. Use a key file when the process holds a real Fireworks key. See `deploy/keys.example.json`. Do not commit real keys.

```bash
./bin/firerouter -keys keys.json
```

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
