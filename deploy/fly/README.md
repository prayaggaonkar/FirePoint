# Public sandbox

This deploys FireRouter and the mock upstream on one Fly machine. The mock listens on `127.0.0.1:9000` inside that machine. Only FireRouter is published, on port 8080. There is no Fireworks API key in the image, the config, or the process environment. If `FIREWORKS_API_KEY` is set, the sandbox process exits before it listens.

`GET /` is a read-only page. It shows a summary of this process's metrics and three curl examples that call the mock. It refreshes every 5 seconds. Each client address may send 10 requests per minute, including loads of that page. `GET /healthz` is not counted. Request bodies are capped at 8192 bytes. The per-address limit uses `Fly-Client-IP` when Fly sets it, and otherwise the TCP peer. `X-Forwarded-For` is ignored.

The mock admin routes are closed. There is no admin token.

## Deploy

From the repository root, with [flyctl](https://fly.io/docs/flyctl/) logged in:

```bash
fly apps create your-sandbox-name
```

Set `app` in `deploy/fly/fly.toml` to that name, then:

```bash
fly deploy . --config deploy/fly/fly.toml --dockerfile deploy/fly/Dockerfile --ha=false
```

The `.` is the build context. The Dockerfile copies the Go module from the repository root.

Open `https://your-sandbox-name.fly.dev/`. The curl examples on that page use the host you opened.

Do not run `fly secrets set FIREWORKS_API_KEY`. The public app must stay on the mock. A private deploy that should call Fireworks uses `firerouter` directly, with the key only in that process's environment, and does not use this sandbox image.
