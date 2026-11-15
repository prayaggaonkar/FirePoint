# Contributing

FireRouter is Apache-2.0. See [LICENSE](LICENSE).

Issues and pull requests: [github.com/prayaggaonkar/FireRouter](https://github.com/prayaggaonkar/FireRouter). Report vulnerabilities the way [SECURITY.md](SECURITY.md) describes, not in a public issue.

## Build

Go 1.22.2 or newer. The module line is `go 1.22.2`.

```bash
make build
```

That writes `bin/firerouter`, `bin/mockupstream`, and `bin/loadgen`.

```bash
./bin/firerouter --version
```

A release build stamps the version with `-ldflags "-X main.version=..."`. A local `make build` prints `dev`.

## Test

```bash
make test
```

That is `go vet ./...` and `go test -race ./...`. CI also runs staticcheck 2024.1.1, `make compat`, and `docker build -f deploy/Dockerfile`.

`make compat` builds the mock and the proxy, then runs the Python and JavaScript OpenAI SDK suites against them. It needs `python3` (with `venv`), Node, npm, and curl. The first run creates `compat/python/.venv` and installs `compat/js` dependencies.

Do not put prompts or API keys in logs, tests, or commit messages. Read keys from the environment. The sample digest in `deploy/keys.example.json` is not a real key.

## Demo

No Fireworks key. From the repo root:

```bash
make demo-up
./demo/demo.sh
make demo-down
```

`make demo-up` installs the pinned OpenAI package into `demo/.venv` and starts `deploy/docker-compose.yml`. The mock is on port 9000, FireRouter on 8080, Prometheus on 9090, and Grafana on [http://localhost:3000](http://localhost:3000) with anonymous viewing.

`./demo/demo.sh` reads the virtual keys from `deploy/demo.env` (a root `.env` overrides them), then walks four acts and waits for Enter between them. The mock admin token defaults to `demo`.

The same stack without the venv install:

```bash
docker compose -f deploy/docker-compose.yml up --build
```

## Change guidelines

Keep the standard library unless a dependency is agreed first. One shared HTTP client per process. Forward each SSE chunk and flush it. Pass the request context to the upstream call. Inject time into anything that depends on the clock so tests do not sleep for it.

A pull request should say what changed and which commands you ran. `go vet ./...` and `go test -race ./...` are the minimum.
