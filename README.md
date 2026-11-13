# FireRouter

An open-source, low-latency streaming proxy that sits in front of Fireworks AI's OpenAI-compatible API.

Status: early development

## Quick start

Build the programs:

```bash
make build
```

### Mock upstream

In one terminal, start the fake Fireworks server:

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
