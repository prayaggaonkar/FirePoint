# LiteLLM comparison

The tables in this file are the output of `python3 bench/litellm/analyze.py`. That script reads `bench/litellm/results/<scenario>/workers-<label>/rps-<rate>/run-*.json`. It does not read `bench/results`. Median across run files matches `bench/analyze.py`: an odd count takes the middle value, an even count takes the mean of the two middle values. Overhead is that median minus the direct median. A compare line says the ranges do not overlap only when one path's min and max sit entirely above or entirely below the direct min and max. No compare line in this output says LiteLLM's time to first `data:` line is lower than direct.

`bench/results` is the separate FirePoint-versus-direct run from `bench/run_bench.sh` (mock `127.0.0.1:19110`, proxy `127.0.0.1:18090`). Those files are not inputs here.

## Hardware

From `bench/litellm/results/A/environment.json`, `B/environment.json`, and `C/environment.json`:

- CPU: Apple M4
- Cores: 10
- OS: Darwin 25.6.0 arm64
- Go: go version go1.27.4 darwin/arm64
- LiteLLM version recorded in those files: 1.111.0

Load generator, both proxies, the mock, and, for scenario B, Postgres all ran on this machine.

## Versions

From `bench/litellm/VERSIONS.txt`:

- litellm 1.111.0
- prisma 0.15.0
- psutil 7.2.2
- python 3.14.7
- go version go1.27.4 darwin/arm64

## Configs

Started by `bench/litellm/run_compare.sh`:

- Scenario A: `bench/litellm/config.yaml`. Model `bench`, upstream `openai/bench` at `http://127.0.0.1:19111/v1`, `api_key: unused`. Empty callbacks, `turn_off_message_logging: true`, `num_retries: 0`. No `database_url`. `dangerously_permit_weak_or_unset_master_key: true`, `store_model_in_db: false`, `disable_spend_logs`, `disable_error_logs`, and `disable_spend_updates` true. `DISABLE_ADMIN_UI: "True"`.
- Scenario B: `bench/litellm/config-b.yaml`. Same model, plus deployment `rpm: 1000000` and `tpm: 100000000`. `master_key` and `database_url` come from the environment. Spend logs, error logs, and spend updates are disabled. The virtual key is generated with `rpm_limit` 1000000 and `tpm_limit` 100000000. Loadgen sends that key, not the master key. Postgres is `postgresql://litellm@127.0.0.1:54329/litellm`. FirePoint's key file for this run is `bench/litellm/results/B/keys.json`: tenant `bench`, `rpm` 1000000, `tpm` 100000000, and a `key_sha256`. FirePoint is started with `-keys` that file, `-rpm 1000000`, and `-tpm 100000000`.
- Scenario C: `bench/litellm/config-c.yaml` and `bench/litellm/router-c.json`. Client model `primary` falls back to `fallback`. `num_retries: 0` in both `litellm_settings` and `router_settings`. `cooldown_time: 5`. `allowed_fails` is not set. The mock is started with `-primary primary`. FirePoint is started with `-config bench/litellm/router-c.json`. `results/C/environment.json` records the FirePoint breaker as window 30s, min 20 requests, error threshold 0.5, open 15s, half-open probes 3, and the LiteLLM cooldown rule stored in that file.

Not started:

- `bench/litellm/config-b-spend.yaml`. Its comment says spend logs and spend updates are left at LiteLLM defaults. `run_compare.sh` does not start it.
- `bench/litellm/config-c-default-retries.yaml`. Its comment says `num_retries` is left at LiteLLM's default of 2. `run_compare.sh` does not start it.

LiteLLM worker labels in the result files: `default` with `litellm_worker_count` 1, and `cpu` with `litellm_worker_count` 10. `run_compare.sh` passes `--num_workers` and leaves the server on the uvicorn path. Gunicorn is not enabled. `DEFAULT_NUM_WORKERS_LITELLM_PROXY` is 1, and the `default` label passes that count explicitly.

## Method

`run_compare.sh` builds the mock, FirePoint, and loadgen. The mock listens on `127.0.0.1:19111` with `-first-token-delay 200ms`, `-token-interval 20ms`, and `-tokens 50` (`BENCH_TOKENS` default). FirePoint listens on `127.0.0.1:18091` with upstream `http://127.0.0.1:19111`, `-rpm 1000000`, and `-tpm 100000000`. LiteLLM listens on `127.0.0.1:14011`.

Each stored run has `warmup_seconds` 10. Time to first `data:` line is `ttft_ms`. The gap between tokens is `inter_token_ms`. `recorded_per_s` is requests divided by `duration_seconds`. `successful_per_s` subtracts `errors` and `status_429` before dividing. CPU and RSS come from `bench/litellm/sample_proc.py`, which sleeps through the warmup and samples the proxy process tree only during the measured window. CPU percent is psutil's since the previous call, summed over the root and its children. 100 means one core. The direct path stores `error: "direct path has no proxy process"`.

`concurrency_cap` and `error_kinds` are blank in the tables when the JSON omits them. The loadgen struct tags those fields `omitempty`, so a zero cap is absent.

The script's intended full run is 5 trials, measured 30s, rates 20, 100, and 200 for A and B, and 20 for C, for both worker labels. That run did not finish. See the next section. The duration 5s files are the `SMOKE=1` runs (1 trial, measured 5s, warmup still 10s).

## What is on disk

Duration 30s run files exist only for scenario A, `litellm_workers=default`, `count=1`, requested 20 rps: `run-1.json` and `run-2.json`. There is no `run-3.json`, `run-4.json`, or `run-5.json`. There are no duration 30s files for `workers=cpu`, for 100 or 200 rps, or for scenarios B and C.

Uncombined files in that same directory, which the tables do not fold into `run-2.json`:

- `run-2-firepoint.tmp`: requests 600, errors 0, status_codes `{"200": 600}`, duration_seconds 30. Its resource file has `cpu_percent_median` 8.1 and `cpu_percent_max` 9.1. `run-2.json` firepoint has `cpu_percent_median` 8.0 and `cpu_percent_max` 9.2. They are not the same sample.
- `run-2-litellm.tmp`: requests 599, errors 3, `concurrency_cap` 3, status_codes `{"200": 596}`, duration_seconds 30. Resource `cpu_percent_median` 88.4, `cpu_percent_max` 100.1.
- `run-3-litellm.tmp`: requests 600, errors 4, `concurrency_cap` 4, status_codes `{"200": 596}`, duration_seconds 30. Resource `cpu_percent_median` 71.25, `cpu_percent_max` 99.6.

`run_compare.sh` exits the 20 rps pass when a trial has errors. Those two LiteLLM tmp files are the trials with errors. The 30s matrix stops there.

Every other `workers-*/rps-*/run-*.json` file has `duration_seconds` 5 and one run file. Scenario A at requested 20 rps and duration 5s has `workers=cpu` only. The `workers=default` 20 rps directory holds the 30s files above, not a 5s file.

These older files have no `litellm_workers` field and are not in the tables: `bench/litellm/results/run-1.json`, `A/run-1.json`, `B/run-1.json`, and `C/run-1.json`. Each has `duration_seconds` 5 and 100 requests on direct, FirePoint, and LiteLLM.

Scenario C files have `duration_seconds` 5 and `steady` requests 0, because the split window is 5s and the measured window is 5s. Mock counts in those files include warmup. `workers=default` FirePoint `mock.models` is `{"fallback": 299, "primary": 19}`. `workers=cpu` FirePoint `mock.models` is `{"fallback": 299, "primary": 1}`. Both LiteLLM rows are `{"fallback": 299, "primary": 299}`. Both direct rows are `{"primary": 299}`. `run_compare.sh` does not restart FirePoint between the two LiteLLM worker labels, so the `workers=cpu` FirePoint count is not a separate cold start.

## Where LiteLLM is lower than direct

These are the only compare lines that say `litellm is lower`. All of them are inter-token gaps. The error count on that same LiteLLM row is next to each one. A section with one run file has min equal to max.

Zero LiteLLM errors on that row:

- Scenario A, requested 20 rps, duration 30s, `workers=default`, count 1, inter-token p50: LiteLLM min 17.855 max 20.335, direct min 20.415 max 20.431. Both run files have LiteLLM errors 0 (600 and 600 requests). On the same two files, LiteLLM TTFT p50 is higher (min 217.983 max 309.503, direct min 200.575 max 200.703) and TTFT p90 is higher (min 225.663 max 1053.695, direct min 200.831 max 201.087).
- Scenario A, requested 100 rps, duration 5s, `workers=cpu`, count 10, inter-token p50: LiteLLM 20.175, direct 20.255. LiteLLM errors 0, requests 500, `successful_per_s` 100.0.
- Scenario B, requested 20 rps, duration 5s, `workers=cpu`, count 10, inter-token p50: LiteLLM 20.927, direct 21.087. Inter-token p99.9: LiteLLM 25.903, direct 26.639. LiteLLM errors 0, requests 100.
- Scenario B, requested 100 rps, duration 5s, `workers=cpu`, count 10, inter-token p50: LiteLLM 20.159, direct 20.271. LiteLLM errors 0, requests 500, `successful_per_s` 100.0.

LiteLLM errors on that row are not zero:

- Scenario A, 100 rps, duration 5s, `workers=default`: inter-token p50 LiteLLM 0.267, direct 20.255. LiteLLM errors 375, `concurrency_cap` 375, status_codes `{"200": 125}`, `successful_per_s` 25.0.
- Scenario A, 200 rps, duration 5s, `workers=default`: inter-token p50 LiteLLM 0.274, direct 20.159. LiteLLM errors 897, `concurrency_cap` 897, status_codes `{"200": 103}`, `successful_per_s` 20.6.
- Scenario A, 200 rps, duration 5s, `workers=cpu`: inter-token p50 LiteLLM 0.409, direct 20.159; inter-token p90 LiteLLM 9.207, direct 20.767. LiteLLM requests 972, errors 350, `concurrency_cap` 350, status_codes `{"200": 622}`, `recorded_per_s` 194.4, `successful_per_s` 124.4.
- Scenario B, 100 rps, duration 5s, `workers=default`: inter-token p50 LiteLLM 0.268, direct 20.255. LiteLLM errors 354, `concurrency_cap` 354, status_codes `{"200": 146}`, `successful_per_s` 29.2.
- Scenario B, 200 rps, duration 5s, `workers=default`: inter-token p50 LiteLLM 0.275, direct 20.159. LiteLLM requests 998, errors 889, `concurrency_cap` 889, status_codes `{"200": 109}`, `recorded_per_s` 199.6, `successful_per_s` 21.8.
- Scenario B, 200 rps, duration 5s, `workers=cpu`: inter-token p50 LiteLLM 0.475, direct 20.143. LiteLLM requests 975, errors 439, `concurrency_cap` 439, status_codes `{"200": 536}`, `recorded_per_s` 195.0, `successful_per_s` 107.2.

## Tables

```bash
python3 bench/litellm/analyze.py
```

# LiteLLM comparison tables from `bench/litellm/results`

Overhead is the path median minus the direct median, in the same units as the series. A compare line says ranges do not overlap only when one path's min and max sit entirely above or entirely below the direct min and max. concurrency_cap and error_kinds are blank when the JSON omits them. CPU percent is the field stored on the run. 100 means one core.

## Environment files

### `bench/litellm/results/A/environment.json`

```json
{
  "cores": 10,
  "cpu_model": "Apple M4",
  "go_version": "go version go1.27.4 darwin/arm64",
  "litellm_version": "1.111.0",
  "os": "Darwin 25.6.0 arm64",
  "scenario": "A"
}
```

### `bench/litellm/results/B/environment.json`

```json
{
  "cores": 10,
  "cpu_model": "Apple M4",
  "go_version": "go version go1.27.4 darwin/arm64",
  "litellm_version": "1.111.0",
  "os": "Darwin 25.6.0 arm64",
  "scenario": "B"
}
```

### `bench/litellm/results/C/environment.json`

```json
{
  "cores": 10,
  "cpu_model": "Apple M4",
  "firepoint_breaker": {
    "error_threshold": 0.5,
    "half_open_probes": 3,
    "min_requests": 20,
    "open_s": 15,
    "window_s": 30
  },
  "go_version": "go version go1.27.4 darwin/arm64",
  "litellm_cooldown": {
    "allowed_fails": null,
    "cooldown_time_s": 5,
    "num_retries": 0,
    "rule": "single deployment: 100% failures and 1000 requests in the current minute, or a non-retryable status. The 50% rule is skipped."
  },
  "litellm_version": "1.111.0",
  "os": "Darwin 25.6.0 arm64",
  "scenario": "C"
}
```

## Scenario A, requested 20.0 rps, duration 5.0s

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 64

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 200.959 | 200.959 | 200.959 | 201.599 | 201.599 | 201.599 | 202.239 | 202.239 | 202.239 | 202.367 | 202.367 | 202.367 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 201.215 | 201.215 | 201.215 | 201.727 | 201.727 | 201.727 | 202.879 | 202.879 | 202.879 | 203.263 | 203.263 | 203.263 | 0.256 | 0.128 | 0.640 | 0.896 |
| cpu | 10 | litellm | 1 | 210.687 | 210.687 | 210.687 | 212.607 | 212.607 | 212.607 | 214.527 | 214.527 | 214.527 | 215.039 | 215.039 | 215.039 | 9.728 | 11.008 | 12.288 | 12.672 |

compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 201.215 max 201.215, direct min 200.959 max 200.959, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 210.687 max 210.687, direct min 200.959 max 200.959, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 201.727 max 201.727, direct min 201.599 max 201.599, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 212.607 max 212.607, direct min 201.599 max 201.599, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 202.879 max 202.879, direct min 202.239 max 202.239, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 214.527 max 214.527, direct min 202.239 max 202.239, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 203.263 max 203.263, direct min 202.367 max 202.367, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 215.039 max 215.039, direct min 202.367 max 202.367, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 20.927 | 20.927 | 20.927 | 22.095 | 22.095 | 22.095 | 22.351 | 22.351 | 22.351 | 22.591 | 22.591 | 22.591 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 20.991 | 20.991 | 20.991 | 22.111 | 22.111 | 22.111 | 22.479 | 22.479 | 22.479 | 24.863 | 24.863 | 24.863 | 0.064 | 0.016 | 0.128 | 2.272 |
| cpu | 10 | litellm | 1 | 20.943 | 20.943 | 20.943 | 22.287 | 22.287 | 22.287 | 24.303 | 24.303 | 24.303 | 57.055 | 57.055 | 57.055 | 0.016 | 0.192 | 1.952 | 34.464 |

compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 20.991 max 20.991, direct min 20.927 max 20.927, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 20.943 max 20.943, direct min 20.927 max 20.927, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 22.111 max 22.111, direct min 22.095 max 22.095, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 22.287 max 22.287, direct min 22.095 max 22.095, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 22.479 max 22.479, direct min 22.351 max 22.351, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 24.303 max 24.303, direct min 22.351 max 22.351, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 24.863 max 24.863, direct min 22.591 max 22.591, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 57.055 max 57.055, direct min 22.591 max 22.591, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| cpu | 10 | 1 | firepoint | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| cpu | 10 | 1 | litellm | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 7.3 | 8.5 | 27918336 | 28147712 |  |
| cpu | 10 | 1 | litellm | 9 | 12 | 87.39999999999999 | 91.0 | 2507997184 | 2509619200 |  |

## Scenario A, requested 20.0 rps, duration 30.0s

### litellm_workers=default count=1, 2 run file(s), warmup 10s, concurrency 64

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 2 | 200.639 | 200.575 | 200.703 | 200.959 | 200.831 | 201.087 | 206.207 | 204.287 | 208.127 | 261.055 | 226.943 | 295.167 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 2 | 201.151 | 201.087 | 201.215 | 201.471 | 201.471 | 201.471 | 223.423 | 201.855 | 244.991 | 260.991 | 204.543 | 317.439 | 0.512 | 0.512 | 17.216 | -0.064 |
| default | 1 | litellm | 2 | 263.743 | 217.983 | 309.503 | 639.679 | 225.663 | 1053.695 | 823.551 | 231.935 | 1415.167 | 862.591 | 282.367 | 1442.815 | 63.104 | 438.720 | 617.344 | 601.536 |

compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p50: firepoint min 201.087 max 201.215, direct min 200.575 max 200.703, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p50: litellm min 217.983 max 309.503, direct min 200.575 max 200.703, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p90: firepoint min 201.471 max 201.471, direct min 200.831 max 201.087, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p90: litellm min 225.663 max 1053.695, direct min 200.831 max 201.087, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p99: firepoint min 201.855 max 244.991, direct min 204.287 max 208.127, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p99: litellm min 231.935 max 1415.167, direct min 204.287 max 208.127, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p99.9: firepoint min 204.543 max 317.439, direct min 226.943 max 295.167, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 TTFT p99.9: litellm min 282.367 max 1442.815, direct min 226.943 max 295.167, ranges overlap

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 2 | 20.423 | 20.415 | 20.431 | 20.943 | 20.927 | 20.959 | 28.943 | 22.815 | 35.071 | 82.751 | 62.079 | 103.423 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 2 | 20.447 | 20.431 | 20.463 | 20.967 | 20.911 | 21.023 | 22.647 | 21.247 | 24.047 | 57.007 | 21.471 | 92.543 | 0.024 | 0.024 | -6.296 | -25.744 |
| default | 1 | litellm | 2 | 19.095 | 17.855 | 20.335 | 27.055 | 24.399 | 29.711 | 76.887 | 28.399 | 125.375 | 200.159 | 32.703 | 367.615 | -1.328 | 6.112 | 47.944 | 117.408 |

compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p50: firepoint min 20.431 max 20.463, direct min 20.415 max 20.431, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p50: litellm min 17.855 max 20.335, direct min 20.415 max 20.431, ranges do not overlap, litellm is lower
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p90: firepoint min 20.911 max 21.023, direct min 20.927 max 20.959, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p90: litellm min 24.399 max 29.711, direct min 20.927 max 20.959, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p99: firepoint min 21.247 max 24.047, direct min 22.815 max 35.071, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p99: litellm min 28.399 max 125.375, direct min 22.815 max 35.071, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p99.9: firepoint min 21.471 max 92.543, direct min 62.079 max 103.423, ranges overlap
compare scenario=A requested_rps=20.0 duration_s=30 litellm_workers=default count=1 inter-token p99.9: litellm min 32.703 max 367.615, direct min 62.079 max 103.423, ranges overlap

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 600 | 0 | 0 |  | {"200": 600} |  | 20.0 | 20.0 |
| default | 1 | 1 | firepoint | 599 | 0 | 0 |  | {"200": 599} |  | 19.966666666666665 | 19.966666666666665 |
| default | 1 | 1 | litellm | 600 | 0 | 0 |  | {"200": 600} |  | 20.0 | 20.0 |
| default | 1 | 2 | direct | 600 | 0 | 0 |  | {"200": 600} |  | 20.0 | 20.0 |
| default | 1 | 2 | firepoint | 600 | 0 | 0 |  | {"200": 600} |  | 20.0 | 20.0 |
| default | 1 | 2 | litellm | 600 | 0 | 0 |  | {"200": 600} |  | 20.0 | 20.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 58 | 1 | 7.65 | 9.6 | 24592384.0 | 25182208 |  |
| default | 1 | 1 | litellm | 58 | 1 | 89.25 | 98.9 | 566738944.0 | 566886400 |  |
| default | 1 | 2 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 2 | firepoint | 58 | 1 | 8.0 | 9.2 | 25575424.0 | 26001408 |  |
| default | 1 | 2 | litellm | 59 | 1 | 69.5 | 74.5 | 1008386048 | 1008435200 |  |

## Scenario A, requested 100.0 rps, duration 5.0s

### litellm_workers=default count=1, 1 run file(s), warmup 10s, concurrency 200

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 200.575 | 200.575 | 200.575 | 201.471 | 201.471 | 201.471 | 202.495 | 202.495 | 202.495 | 203.007 | 203.007 | 203.007 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 200.959 | 200.959 | 200.959 | 201.727 | 201.727 | 201.727 | 202.751 | 202.751 | 202.751 | 207.359 | 207.359 | 207.359 | 0.384 | 0.256 | 0.256 | 4.352 |
| default | 1 | litellm | 1 | 754.175 | 754.175 | 754.175 | 1176.575 | 1176.575 | 1176.575 | 1337.343 | 1337.343 | 1337.343 | 1339.391 | 1339.391 | 1339.391 | 553.600 | 975.104 | 1134.848 | 1136.384 |

compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p50: firepoint min 200.959 max 200.959, direct min 200.575 max 200.575, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p50: litellm min 754.175 max 754.175, direct min 200.575 max 200.575, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p90: firepoint min 201.727 max 201.727, direct min 201.471 max 201.471, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p90: litellm min 1176.575 max 1176.575, direct min 201.471 max 201.471, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99: firepoint min 202.751 max 202.751, direct min 202.495 max 202.495, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99: litellm min 1337.343 max 1337.343, direct min 202.495 max 202.495, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: firepoint min 207.359 max 207.359, direct min 203.007 max 203.007, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: litellm min 1339.391 max 1339.391, direct min 203.007 max 203.007, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 20.255 | 20.255 | 20.255 | 21.343 | 21.343 | 21.343 | 22.415 | 22.415 | 22.415 | 22.847 | 22.847 | 22.847 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 20.239 | 20.239 | 20.239 | 21.375 | 21.375 | 21.375 | 22.543 | 22.543 | 22.543 | 24.399 | 24.399 | 24.399 | -0.016 | 0.032 | 0.128 | 1.552 |
| default | 1 | litellm | 1 | 0.267 | 0.267 | 0.267 | 46.655 | 46.655 | 46.655 | 110.911 | 110.911 | 110.911 | 161.663 | 161.663 | 161.663 | -19.988 | 25.312 | 88.496 | 138.816 |

compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p50: firepoint min 20.239 max 20.239, direct min 20.255 max 20.255, ranges do not overlap, firepoint is lower
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p50: litellm min 0.267 max 0.267, direct min 20.255 max 20.255, ranges do not overlap, litellm is lower
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p90: firepoint min 21.375 max 21.375, direct min 21.343 max 21.343, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p90: litellm min 46.655 max 46.655, direct min 21.343 max 21.343, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99: firepoint min 22.543 max 22.543, direct min 22.415 max 22.415, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99: litellm min 110.911 max 110.911, direct min 22.415 max 22.415, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: firepoint min 24.399 max 24.399, direct min 22.847 max 22.847, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: litellm min 161.663 max 161.663, direct min 22.847 max 22.847, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| default | 1 | 1 | firepoint | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| default | 1 | 1 | litellm | 500 | 375 | 0 | 375 | {"200": 125} |  | 100.0 | 25.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 9 | 1 | 22.6 | 24.6 | 38748160 | 38748160 |  |
| default | 1 | 1 | litellm | 9 | 1 | 100.2 | 100.4 | 1083424768 | 1083670528 |  |

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 200

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 200.575 | 200.575 | 200.575 | 201.215 | 201.215 | 201.215 | 202.239 | 202.239 | 202.239 | 203.263 | 203.263 | 203.263 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 200.959 | 200.959 | 200.959 | 201.727 | 201.727 | 201.727 | 202.751 | 202.751 | 202.751 | 204.671 | 204.671 | 204.671 | 0.384 | 0.512 | 0.512 | 1.408 |
| cpu | 10 | litellm | 1 | 235.135 | 235.135 | 235.135 | 315.391 | 315.391 | 315.391 | 464.895 | 464.895 | 464.895 | 498.431 | 498.431 | 498.431 | 34.560 | 114.176 | 262.656 | 295.168 |

compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 200.959 max 200.959, direct min 200.575 max 200.575, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 235.135 max 235.135, direct min 200.575 max 200.575, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 201.727 max 201.727, direct min 201.215 max 201.215, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 315.391 max 315.391, direct min 201.215 max 201.215, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 202.751 max 202.751, direct min 202.239 max 202.239, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 464.895 max 464.895, direct min 202.239 max 202.239, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 204.671 max 204.671, direct min 203.263 max 203.263, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 498.431 max 498.431, direct min 203.263 max 203.263, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 20.255 | 20.255 | 20.255 | 21.375 | 21.375 | 21.375 | 22.399 | 22.399 | 22.399 | 22.767 | 22.767 | 22.767 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 20.287 | 20.287 | 20.287 | 21.263 | 21.263 | 21.263 | 22.463 | 22.463 | 22.463 | 23.263 | 23.263 | 23.263 | 0.032 | -0.112 | 0.064 | 0.496 |
| cpu | 10 | litellm | 1 | 20.175 | 20.175 | 20.175 | 25.055 | 25.055 | 25.055 | 38.783 | 38.783 | 38.783 | 64.575 | 64.575 | 64.575 | -0.080 | 3.680 | 16.384 | 41.808 |

compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 20.287 max 20.287, direct min 20.255 max 20.255, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 20.175 max 20.175, direct min 20.255 max 20.255, ranges do not overlap, litellm is lower
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 21.263 max 21.263, direct min 21.375 max 21.375, ranges do not overlap, firepoint is lower
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 25.055 max 25.055, direct min 21.375 max 21.375, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 22.463 max 22.463, direct min 22.399 max 22.399, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 38.783 max 38.783, direct min 22.399 max 22.399, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 23.263 max 23.263, direct min 22.767 max 22.767, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 64.575 max 64.575, direct min 22.767 max 22.767, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| cpu | 10 | 1 | firepoint | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| cpu | 10 | 1 | litellm | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 23.2 | 24.8 | 40108032 | 40288256 |  |
| cpu | 10 | 1 | litellm | 9 | 12 | 572.9 | 612.0 | 1912586240 | 1913995264 |  |

## Scenario A, requested 200.0 rps, duration 5.0s

### litellm_workers=default count=1, 1 run file(s), warmup 10s, concurrency 400

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 200.447 | 200.447 | 200.447 | 200.831 | 200.831 | 200.831 | 201.215 | 201.215 | 201.215 | 201.983 | 201.983 | 201.983 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 200.703 | 200.703 | 200.703 | 201.087 | 201.087 | 201.087 | 201.471 | 201.471 | 201.471 | 201.727 | 201.727 | 201.727 | 0.256 | 0.256 | 0.256 | -0.256 |
| default | 1 | litellm | 1 | 6742.015 | 6742.015 | 6742.015 | 7970.815 | 7970.815 | 7970.815 | 8552.447 | 8552.447 | 8552.447 | 8577.023 | 8577.023 | 8577.023 | 6541.568 | 7769.984 | 8351.232 | 8375.040 |

compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p50: firepoint min 200.703 max 200.703, direct min 200.447 max 200.447, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p50: litellm min 6742.015 max 6742.015, direct min 200.447 max 200.447, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p90: firepoint min 201.087 max 201.087, direct min 200.831 max 200.831, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p90: litellm min 7970.815 max 7970.815, direct min 200.831 max 200.831, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99: firepoint min 201.471 max 201.471, direct min 201.215 max 201.215, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99: litellm min 8552.447 max 8552.447, direct min 201.215 max 201.215, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: firepoint min 201.727 max 201.727, direct min 201.983 max 201.983, ranges do not overlap, firepoint is lower
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: litellm min 8577.023 max 8577.023, direct min 201.983 max 201.983, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 20.159 | 20.159 | 20.159 | 20.767 | 20.767 | 20.767 | 22.271 | 22.271 | 22.271 | 22.623 | 22.623 | 22.623 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 20.159 | 20.159 | 20.159 | 20.767 | 20.767 | 20.767 | 22.671 | 22.671 | 22.671 | 23.007 | 23.007 | 23.007 | 0.000 | 0.000 | 0.400 | 0.384 |
| default | 1 | litellm | 1 | 0.274 | 0.274 | 0.274 | 33.151 | 33.151 | 33.151 | 137.215 | 137.215 | 137.215 | 335.103 | 335.103 | 335.103 | -19.885 | 12.384 | 114.944 | 312.480 |

compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p50: firepoint min 20.159 max 20.159, direct min 20.159 max 20.159, ranges overlap
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p50: litellm min 0.274 max 0.274, direct min 20.159 max 20.159, ranges do not overlap, litellm is lower
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p90: firepoint min 20.767 max 20.767, direct min 20.767 max 20.767, ranges overlap
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p90: litellm min 33.151 max 33.151, direct min 20.767 max 20.767, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99: firepoint min 22.671 max 22.671, direct min 22.271 max 22.271, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99: litellm min 137.215 max 137.215, direct min 22.271 max 22.271, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: firepoint min 23.007 max 23.007, direct min 22.623 max 22.623, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: litellm min 335.103 max 335.103, direct min 22.623 max 22.623, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| default | 1 | 1 | firepoint | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| default | 1 | 1 | litellm | 1000 | 897 | 0 | 897 | {"200": 103} |  | 200.0 | 20.6 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 9 | 1 | 45.0 | 47.1 | 55492608 | 55820288 |  |
| default | 1 | 1 | litellm | 9 | 1 | 100.1 | 100.2 | 1088110592 | 1098383360 |  |

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 400

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 200.447 | 200.447 | 200.447 | 200.831 | 200.831 | 200.831 | 201.215 | 201.215 | 201.215 | 202.751 | 202.751 | 202.751 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 200.703 | 200.703 | 200.703 | 201.087 | 201.087 | 201.087 | 201.855 | 201.855 | 201.855 | 202.623 | 202.623 | 202.623 | 0.256 | 0.256 | 0.640 | -0.128 |
| cpu | 10 | litellm | 1 | 1474.559 | 1474.559 | 1474.559 | 2015.231 | 2015.231 | 2015.231 | 2330.623 | 2330.623 | 2330.623 | 2457.599 | 2457.599 | 2457.599 | 1274.112 | 1814.400 | 2129.408 | 2254.848 |

compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 200.703 max 200.703, direct min 200.447 max 200.447, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 1474.559 max 1474.559, direct min 200.447 max 200.447, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 201.087 max 201.087, direct min 200.831 max 200.831, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 2015.231 max 2015.231, direct min 200.831 max 200.831, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 201.855 max 201.855, direct min 201.215 max 201.215, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 2330.623 max 2330.623, direct min 201.215 max 201.215, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 202.623 max 202.623, direct min 202.751 max 202.751, ranges do not overlap, firepoint is lower
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 2457.599 max 2457.599, direct min 202.751 max 202.751, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 20.159 | 20.159 | 20.159 | 20.767 | 20.767 | 20.767 | 22.479 | 22.479 | 22.479 | 23.295 | 23.295 | 23.295 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 20.159 | 20.159 | 20.159 | 20.783 | 20.783 | 20.783 | 22.927 | 22.927 | 22.927 | 23.983 | 23.983 | 23.983 | 0.000 | 0.016 | 0.448 | 0.688 |
| cpu | 10 | litellm | 1 | 0.409 | 0.409 | 0.409 | 9.207 | 9.207 | 9.207 | 276.991 | 276.991 | 276.991 | 518.399 | 518.399 | 518.399 | -19.750 | -11.560 | 254.512 | 495.104 |

compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 20.159 max 20.159, direct min 20.159 max 20.159, ranges overlap
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 0.409 max 0.409, direct min 20.159 max 20.159, ranges do not overlap, litellm is lower
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 20.783 max 20.783, direct min 20.767 max 20.767, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 9.207 max 9.207, direct min 20.767 max 20.767, ranges do not overlap, litellm is lower
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 22.927 max 22.927, direct min 22.479 max 22.479, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 276.991 max 276.991, direct min 22.479 max 22.479, ranges do not overlap, litellm is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 23.983 max 23.983, direct min 23.295 max 23.295, ranges do not overlap, firepoint is higher
compare scenario=A requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 518.399 max 518.399, direct min 23.295 max 23.295, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| cpu | 10 | 1 | firepoint | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| cpu | 10 | 1 | litellm | 972 | 350 | 0 | 350 | {"200": 622} |  | 194.4 | 124.4 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 42.5 | 45.8 | 53739520 | 54018048 |  |
| cpu | 10 | 1 | litellm | 9 | 12 | 698.9999999999999 | 718.0999999999999 | 2226585600 | 2246754304 |  |

## Scenario B, requested 20.0 rps, duration 5.0s

### litellm_workers=default count=1, 1 run file(s), warmup 10s, concurrency 64

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 200.703 | 200.703 | 200.703 | 201.727 | 201.727 | 201.727 | 202.239 | 202.239 | 202.239 | 202.623 | 202.623 | 202.623 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 201.215 | 201.215 | 201.215 | 201.983 | 201.983 | 201.983 | 202.879 | 202.879 | 202.879 | 205.951 | 205.951 | 205.951 | 0.512 | 0.256 | 0.640 | 3.328 |
| default | 1 | litellm | 1 | 227.711 | 227.711 | 227.711 | 241.023 | 241.023 | 241.023 | 257.279 | 257.279 | 257.279 | 263.935 | 263.935 | 263.935 | 27.008 | 39.296 | 55.040 | 61.312 |

compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p50: firepoint min 201.215 max 201.215, direct min 200.703 max 200.703, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p50: litellm min 227.711 max 227.711, direct min 200.703 max 200.703, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p90: firepoint min 201.983 max 201.983, direct min 201.727 max 201.727, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p90: litellm min 241.023 max 241.023, direct min 201.727 max 201.727, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99: firepoint min 202.879 max 202.879, direct min 202.239 max 202.239, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99: litellm min 257.279 max 257.279, direct min 202.239 max 202.239, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: firepoint min 205.951 max 205.951, direct min 202.623 max 202.623, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: litellm min 263.935 max 263.935, direct min 202.623 max 202.623, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 21.007 | 21.007 | 21.007 | 22.159 | 22.159 | 22.159 | 22.319 | 22.319 | 22.319 | 22.495 | 22.495 | 22.495 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 20.975 | 20.975 | 20.975 | 22.111 | 22.111 | 22.111 | 22.783 | 22.783 | 22.783 | 27.039 | 27.039 | 27.039 | -0.032 | -0.048 | 0.464 | 4.544 |
| default | 1 | litellm | 1 | 21.055 | 21.055 | 21.055 | 25.535 | 25.535 | 25.535 | 28.879 | 28.879 | 28.879 | 32.223 | 32.223 | 32.223 | 0.048 | 3.376 | 6.560 | 9.728 |

compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p50: firepoint min 20.975 max 20.975, direct min 21.007 max 21.007, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p50: litellm min 21.055 max 21.055, direct min 21.007 max 21.007, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p90: firepoint min 22.111 max 22.111, direct min 22.159 max 22.159, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p90: litellm min 25.535 max 25.535, direct min 22.159 max 22.159, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99: firepoint min 22.783 max 22.783, direct min 22.319 max 22.319, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99: litellm min 28.879 max 28.879, direct min 22.319 max 22.319, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: firepoint min 27.039 max 27.039, direct min 22.495 max 22.495, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: litellm min 32.223 max 32.223, direct min 22.495 max 22.495, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| default | 1 | 1 | firepoint | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| default | 1 | 1 | litellm | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 9 | 1 | 6.8 | 7.6 | 24068096 | 24117248 |  |
| default | 1 | 1 | litellm | 9 | 2 | 72.9 | 77.3 | 1046593536 | 1046593536 |  |

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 64

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 200.703 | 200.703 | 200.703 | 201.471 | 201.471 | 201.471 | 202.495 | 202.495 | 202.495 | 202.495 | 202.495 | 202.495 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 201.087 | 201.087 | 201.087 | 201.727 | 201.727 | 201.727 | 202.111 | 202.111 | 202.111 | 202.495 | 202.495 | 202.495 | 0.384 | 0.256 | -0.384 | 0.000 |
| cpu | 10 | litellm | 1 | 213.631 | 213.631 | 213.631 | 220.799 | 220.799 | 220.799 | 227.071 | 227.071 | 227.071 | 227.199 | 227.199 | 227.199 | 12.928 | 19.328 | 24.576 | 24.704 |

compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 201.087 max 201.087, direct min 200.703 max 200.703, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 213.631 max 213.631, direct min 200.703 max 200.703, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 201.727 max 201.727, direct min 201.471 max 201.471, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 220.799 max 220.799, direct min 201.471 max 201.471, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 202.111 max 202.111, direct min 202.495 max 202.495, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 227.071 max 227.071, direct min 202.495 max 202.495, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 202.495 max 202.495, direct min 202.495 max 202.495, ranges overlap
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 227.199 max 227.199, direct min 202.495 max 202.495, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 21.087 | 21.087 | 21.087 | 22.047 | 22.047 | 22.047 | 22.335 | 22.335 | 22.335 | 26.639 | 26.639 | 26.639 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 21.039 | 21.039 | 21.039 | 22.111 | 22.111 | 22.111 | 22.639 | 22.639 | 22.639 | 23.775 | 23.775 | 23.775 | -0.048 | 0.064 | 0.304 | -2.864 |
| cpu | 10 | litellm | 1 | 20.927 | 20.927 | 20.927 | 22.463 | 22.463 | 22.463 | 24.223 | 24.223 | 24.223 | 25.903 | 25.903 | 25.903 | -0.160 | 0.416 | 1.888 | -0.736 |

compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 21.039 max 21.039, direct min 21.087 max 21.087, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 20.927 max 20.927, direct min 21.087 max 21.087, ranges do not overlap, litellm is lower
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 22.111 max 22.111, direct min 22.047 max 22.047, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 22.463 max 22.463, direct min 22.047 max 22.047, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 22.639 max 22.639, direct min 22.335 max 22.335, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 24.223 max 24.223, direct min 22.335 max 22.335, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 23.775 max 23.775, direct min 26.639 max 26.639, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 25.903 max 25.903, direct min 26.639 max 26.639, ranges do not overlap, litellm is lower

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| cpu | 10 | 1 | firepoint | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| cpu | 10 | 1 | litellm | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 6.8 | 7.4 | 27967488 | 28098560 |  |
| cpu | 10 | 1 | litellm | 9 | 22 | 94.2 | 97.79999999999998 | 2608267264 | 2658041856 |  |

## Scenario B, requested 100.0 rps, duration 5.0s

### litellm_workers=default count=1, 1 run file(s), warmup 10s, concurrency 200

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 200.575 | 200.575 | 200.575 | 201.343 | 201.343 | 201.343 | 202.111 | 202.111 | 202.111 | 202.239 | 202.239 | 202.239 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 200.959 | 200.959 | 200.959 | 201.599 | 201.599 | 201.599 | 202.239 | 202.239 | 202.239 | 202.751 | 202.751 | 202.751 | 0.384 | 0.256 | 0.128 | 0.512 |
| default | 1 | litellm | 1 | 4853.759 | 4853.759 | 4853.759 | 5099.519 | 5099.519 | 5099.519 | 5197.823 | 5197.823 | 5197.823 | 5197.823 | 5197.823 | 5197.823 | 4653.184 | 4898.176 | 4995.712 | 4995.584 |

compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p50: firepoint min 200.959 max 200.959, direct min 200.575 max 200.575, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p50: litellm min 4853.759 max 4853.759, direct min 200.575 max 200.575, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p90: firepoint min 201.599 max 201.599, direct min 201.343 max 201.343, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p90: litellm min 5099.519 max 5099.519, direct min 201.343 max 201.343, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99: firepoint min 202.239 max 202.239, direct min 202.111 max 202.111, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99: litellm min 5197.823 max 5197.823, direct min 202.111 max 202.111, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: firepoint min 202.751 max 202.751, direct min 202.239 max 202.239, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: litellm min 5197.823 max 5197.823, direct min 202.239 max 202.239, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 20.255 | 20.255 | 20.255 | 21.231 | 21.231 | 21.231 | 22.335 | 22.335 | 22.335 | 22.719 | 22.719 | 22.719 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 20.255 | 20.255 | 20.255 | 21.439 | 21.439 | 21.439 | 22.479 | 22.479 | 22.479 | 23.071 | 23.071 | 23.071 | 0.000 | 0.208 | 0.144 | 0.352 |
| default | 1 | litellm | 1 | 0.268 | 0.268 | 0.268 | 88.063 | 88.063 | 88.063 | 154.239 | 154.239 | 154.239 | 170.879 | 170.879 | 170.879 | -19.987 | 66.832 | 131.904 | 148.160 |

compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p50: firepoint min 20.255 max 20.255, direct min 20.255 max 20.255, ranges overlap
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p50: litellm min 0.268 max 0.268, direct min 20.255 max 20.255, ranges do not overlap, litellm is lower
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p90: firepoint min 21.439 max 21.439, direct min 21.231 max 21.231, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p90: litellm min 88.063 max 88.063, direct min 21.231 max 21.231, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99: firepoint min 22.479 max 22.479, direct min 22.335 max 22.335, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99: litellm min 154.239 max 154.239, direct min 22.335 max 22.335, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: firepoint min 23.071 max 23.071, direct min 22.719 max 22.719, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: litellm min 170.879 max 170.879, direct min 22.719 max 22.719, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| default | 1 | 1 | firepoint | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| default | 1 | 1 | litellm | 500 | 354 | 0 | 354 | {"200": 146} |  | 100.0 | 29.2 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 9 | 1 | 22.8 | 24.2 | 38191104 | 38404096 |  |
| default | 1 | 1 | litellm | 9 | 2 | 100.39999999999999 | 100.7 | 1108967424 | 1110097920 |  |

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 200

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 200.575 | 200.575 | 200.575 | 201.215 | 201.215 | 201.215 | 202.111 | 202.111 | 202.111 | 203.391 | 203.391 | 203.391 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 200.959 | 200.959 | 200.959 | 201.727 | 201.727 | 201.727 | 202.239 | 202.239 | 202.239 | 203.519 | 203.519 | 203.519 | 0.384 | 0.512 | 0.128 | 0.128 |
| cpu | 10 | litellm | 1 | 328.447 | 328.447 | 328.447 | 579.583 | 579.583 | 579.583 | 756.735 | 756.735 | 756.735 | 816.127 | 816.127 | 816.127 | 127.872 | 378.368 | 554.624 | 612.736 |

compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 200.959 max 200.959, direct min 200.575 max 200.575, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 328.447 max 328.447, direct min 200.575 max 200.575, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 201.727 max 201.727, direct min 201.215 max 201.215, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 579.583 max 579.583, direct min 201.215 max 201.215, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 202.239 max 202.239, direct min 202.111 max 202.111, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 756.735 max 756.735, direct min 202.111 max 202.111, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 203.519 max 203.519, direct min 203.391 max 203.391, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 816.127 max 816.127, direct min 203.391 max 203.391, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 20.271 | 20.271 | 20.271 | 21.279 | 21.279 | 21.279 | 22.303 | 22.303 | 22.303 | 23.071 | 23.071 | 23.071 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 20.255 | 20.255 | 20.255 | 21.263 | 21.263 | 21.263 | 22.479 | 22.479 | 22.479 | 23.103 | 23.103 | 23.103 | -0.016 | -0.016 | 0.176 | 0.032 |
| cpu | 10 | litellm | 1 | 20.159 | 20.159 | 20.159 | 26.223 | 26.223 | 26.223 | 46.111 | 46.111 | 46.111 | 65.375 | 65.375 | 65.375 | -0.112 | 4.944 | 23.808 | 42.304 |

compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 20.255 max 20.255, direct min 20.271 max 20.271, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 20.159 max 20.159, direct min 20.271 max 20.271, ranges do not overlap, litellm is lower
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 21.263 max 21.263, direct min 21.279 max 21.279, ranges do not overlap, firepoint is lower
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 26.223 max 26.223, direct min 21.279 max 21.279, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 22.479 max 22.479, direct min 22.303 max 22.303, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 46.111 max 46.111, direct min 22.303 max 22.303, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 23.103 max 23.103, direct min 23.071 max 23.071, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=100.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 65.375 max 65.375, direct min 23.071 max 23.071, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| cpu | 10 | 1 | firepoint | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |
| cpu | 10 | 1 | litellm | 500 | 0 | 0 |  | {"200": 500} |  | 100.0 | 100.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 23.3 | 24.3 | 39829504 | 39845888 |  |
| cpu | 10 | 1 | litellm | 9 | 22 | 609.8000000000002 | 621.5999999999999 | 2680373248 | 2723971072 |  |

## Scenario B, requested 200.0 rps, duration 5.0s

### litellm_workers=default count=1, 1 run file(s), warmup 10s, concurrency 400

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 200.447 | 200.447 | 200.447 | 200.831 | 200.831 | 200.831 | 201.343 | 201.343 | 201.343 | 202.367 | 202.367 | 202.367 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 200.703 | 200.703 | 200.703 | 201.087 | 201.087 | 201.087 | 202.111 | 202.111 | 202.111 | 204.287 | 204.287 | 204.287 | 0.256 | 0.256 | 0.768 | 1.920 |
| default | 1 | litellm | 1 | 8085.503 | 8085.503 | 8085.503 | 9519.103 | 9519.103 | 9519.103 | 10166.271 | 10166.271 | 10166.271 | 10239.999 | 10239.999 | 10239.999 | 7885.056 | 9318.272 | 9964.928 | 10037.632 |

compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p50: firepoint min 200.703 max 200.703, direct min 200.447 max 200.447, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p50: litellm min 8085.503 max 8085.503, direct min 200.447 max 200.447, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p90: firepoint min 201.087 max 201.087, direct min 200.831 max 200.831, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p90: litellm min 9519.103 max 9519.103, direct min 200.831 max 200.831, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99: firepoint min 202.111 max 202.111, direct min 201.343 max 201.343, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99: litellm min 10166.271 max 10166.271, direct min 201.343 max 201.343, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: firepoint min 204.287 max 204.287, direct min 202.367 max 202.367, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: litellm min 10239.999 max 10239.999, direct min 202.367 max 202.367, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 20.159 | 20.159 | 20.159 | 20.799 | 20.799 | 20.799 | 22.447 | 22.447 | 22.447 | 23.247 | 23.247 | 23.247 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 20.159 | 20.159 | 20.159 | 20.799 | 20.799 | 20.799 | 22.687 | 22.687 | 22.687 | 23.679 | 23.679 | 23.679 | 0.000 | 0.000 | 0.240 | 0.432 |
| default | 1 | litellm | 1 | 0.275 | 0.275 | 0.275 | 42.399 | 42.399 | 42.399 | 157.695 | 157.695 | 157.695 | 202.111 | 202.111 | 202.111 | -19.884 | 21.600 | 135.248 | 178.864 |

compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p50: firepoint min 20.159 max 20.159, direct min 20.159 max 20.159, ranges overlap
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p50: litellm min 0.275 max 0.275, direct min 20.159 max 20.159, ranges do not overlap, litellm is lower
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p90: firepoint min 20.799 max 20.799, direct min 20.799 max 20.799, ranges overlap
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p90: litellm min 42.399 max 42.399, direct min 20.799 max 20.799, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99: firepoint min 22.687 max 22.687, direct min 22.447 max 22.447, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99: litellm min 157.695 max 157.695, direct min 22.447 max 22.447, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: firepoint min 23.679 max 23.679, direct min 23.247 max 23.247, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: litellm min 202.111 max 202.111, direct min 23.247 max 23.247, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| default | 1 | 1 | firepoint | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| default | 1 | 1 | litellm | 998 | 889 | 0 | 889 | {"200": 109} |  | 199.6 | 21.8 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 9 | 1 | 42.3 | 44.9 | 55525376 | 55558144 |  |
| default | 1 | 1 | litellm | 9 | 2 | 99.9 | 100.4 | 1126973440 | 1140047872 |  |

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 400

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 200.447 | 200.447 | 200.447 | 200.831 | 200.831 | 200.831 | 201.215 | 201.215 | 201.215 | 201.599 | 201.599 | 201.599 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 200.575 | 200.575 | 200.575 | 201.087 | 201.087 | 201.087 | 201.855 | 201.855 | 201.855 | 202.495 | 202.495 | 202.495 | 0.128 | 0.256 | 0.640 | 0.896 |
| cpu | 10 | litellm | 1 | 1712.127 | 1712.127 | 1712.127 | 2637.823 | 2637.823 | 2637.823 | 3813.375 | 3813.375 | 3813.375 | 3938.303 | 3938.303 | 3938.303 | 1511.680 | 2436.992 | 3612.160 | 3736.704 |

compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 200.575 max 200.575, direct min 200.447 max 200.447, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 1712.127 max 1712.127, direct min 200.447 max 200.447, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 201.087 max 201.087, direct min 200.831 max 200.831, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 2637.823 max 2637.823, direct min 200.831 max 200.831, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 201.855 max 201.855, direct min 201.215 max 201.215, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 3813.375 max 3813.375, direct min 201.215 max 201.215, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 202.495 max 202.495, direct min 201.599 max 201.599, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 3938.303 max 3938.303, direct min 201.599 max 201.599, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 20.143 | 20.143 | 20.143 | 20.751 | 20.751 | 20.751 | 22.175 | 22.175 | 22.175 | 22.655 | 22.655 | 22.655 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 20.159 | 20.159 | 20.159 | 20.799 | 20.799 | 20.799 | 22.367 | 22.367 | 22.367 | 22.735 | 22.735 | 22.735 | 0.016 | 0.048 | 0.192 | 0.080 |
| cpu | 10 | litellm | 1 | 0.475 | 0.475 | 0.475 | 28.207 | 28.207 | 28.207 | 337.663 | 337.663 | 337.663 | 598.527 | 598.527 | 598.527 | -19.668 | 7.456 | 315.488 | 575.872 |

compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 20.159 max 20.159, direct min 20.143 max 20.143, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 0.475 max 0.475, direct min 20.143 max 20.143, ranges do not overlap, litellm is lower
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 20.799 max 20.799, direct min 20.751 max 20.751, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 28.207 max 28.207, direct min 20.751 max 20.751, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 22.367 max 22.367, direct min 22.175 max 22.175, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 337.663 max 337.663, direct min 22.175 max 22.175, ranges do not overlap, litellm is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 22.735 max 22.735, direct min 22.655 max 22.655, ranges do not overlap, firepoint is higher
compare scenario=B requested_rps=200.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 598.527 max 598.527, direct min 22.655 max 22.655, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| cpu | 10 | 1 | firepoint | 1000 | 0 | 0 |  | {"200": 1000} |  | 200.0 | 200.0 |
| cpu | 10 | 1 | litellm | 975 | 439 | 0 | 439 | {"200": 536} |  | 195.0 | 107.2 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 41.4 | 45.6 | 53526528 | 53526528 |  |
| cpu | 10 | 1 | litellm | 9 | 22 | 679.1999999999999 | 704.5 | 2584870912 | 2593505280 |  |

## Scenario C, requested 20.0 rps, duration 5.0s

### litellm_workers=default count=1, 1 run file(s), warmup 10s, concurrency 64

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 201.087 | 201.087 | 201.087 | 201.727 | 201.727 | 201.727 | 202.751 | 202.751 | 202.751 | 202.879 | 202.879 | 202.879 | 201.087 | 201.727 | 202.751 | 202.879 |
| default | 1 | litellm | 1 | 247.423 | 247.423 | 247.423 | 287.999 | 287.999 | 287.999 | 304.639 | 304.639 | 304.639 | 334.847 | 334.847 | 334.847 | 247.423 | 287.999 | 304.639 | 334.847 |

compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p50: firepoint min 201.087 max 201.087, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p50: litellm min 247.423 max 247.423, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p90: firepoint min 201.727 max 201.727, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p90: litellm min 287.999 max 287.999, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99: firepoint min 202.751 max 202.751, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99: litellm min 304.639 max 304.639, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: firepoint min 202.879 max 202.879, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 TTFT p99.9: litellm min 334.847 max 334.847, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| default | 1 | direct | 1 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 |
| default | 1 | firepoint | 1 | 21.167 | 21.167 | 21.167 | 22.063 | 22.063 | 22.063 | 22.399 | 22.399 | 22.399 | 22.623 | 22.623 | 22.623 | 21.167 | 22.063 | 22.399 | 22.623 |
| default | 1 | litellm | 1 | 20.927 | 20.927 | 20.927 | 29.807 | 29.807 | 29.807 | 37.887 | 37.887 | 37.887 | 43.455 | 43.455 | 43.455 | 20.927 | 29.807 | 37.887 | 43.455 |

compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p50: firepoint min 21.167 max 21.167, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p50: litellm min 20.927 max 20.927, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p90: firepoint min 22.063 max 22.063, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p90: litellm min 29.807 max 29.807, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99: firepoint min 22.399 max 22.399, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99: litellm min 37.887 max 37.887, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: firepoint min 22.623 max 22.623, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=default count=1 inter-token p99.9: litellm min 43.455 max 43.455, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| default | 1 | 1 | direct | 100 | 100 | 0 |  | {"503": 100} |  | 20.0 | 0.0 |
| default | 1 | 1 | firepoint | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| default | 1 | 1 | litellm | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| default | 1 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| default | 1 | 1 | firepoint | 9 | 1 | 7.0 | 7.1 | 24231936 | 24379392 |  |
| default | 1 | 1 | litellm | 9 | 1 | 93.6 | 96.8 | 1011236864 | 1011433472 |  |

### Failover counters

Mock counts are the `mock.models` object stored on the run. `first_5s` and `steady` are the loadgen split windows stored on the run.

| litellm_workers | count | run | path | mock.models | first_5s requests | first_5s errors | first_5s status_429 | steady requests | steady errors | steady status_429 |
|---|---:|---:|---|---|---:|---:|---:|---:|---:|---:|
| default | 1 | 1 | direct | {"primary": 299} | 100 | 100 | 0 | 0 | 0 | 0 |
| default | 1 | 1 | firepoint | {"fallback": 299, "primary": 19} | 100 | 0 | 0 | 0 | 0 | 0 |
| default | 1 | 1 | litellm | {"fallback": 299, "primary": 299} | 100 | 0 | 0 | 0 | 0 | 0 |

### litellm_workers=cpu count=10, 1 run file(s), warmup 10s, concurrency 64

### TTFT

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 201.599 | 201.599 | 201.599 | 202.751 | 202.751 | 202.751 | 203.391 | 203.391 | 203.391 | 205.055 | 205.055 | 205.055 | 201.599 | 202.751 | 203.391 | 205.055 |
| cpu | 10 | litellm | 1 | 224.127 | 224.127 | 224.127 | 226.559 | 226.559 | 226.559 | 235.007 | 235.007 | 235.007 | 236.927 | 236.927 | 236.927 | 224.127 | 226.559 | 235.007 | 236.927 |

compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: firepoint min 201.599 max 201.599, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p50: litellm min 224.127 max 224.127, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: firepoint min 202.751 max 202.751, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p90: litellm min 226.559 max 226.559, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: firepoint min 203.391 max 203.391, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99: litellm min 235.007 max 235.007, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: firepoint min 205.055 max 205.055, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 TTFT p99.9: litellm min 236.927 max 236.927, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher

### inter-token

| litellm_workers | count | path | run files | p50 median | p50 min | p50 max | p90 median | p90 min | p90 max | p99 median | p99 min | p99 max | p99.9 median | p99.9 min | p99.9 max | overhead p50 | overhead p90 | overhead p99 | overhead p99.9 |
|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cpu | 10 | direct | 1 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 |
| cpu | 10 | firepoint | 1 | 20.975 | 20.975 | 20.975 | 22.127 | 22.127 | 22.127 | 23.855 | 23.855 | 23.855 | 34.623 | 34.623 | 34.623 | 20.975 | 22.127 | 23.855 | 34.623 |
| cpu | 10 | litellm | 1 | 20.831 | 20.831 | 20.831 | 22.239 | 22.239 | 22.239 | 28.143 | 28.143 | 28.143 | 32.623 | 32.623 | 32.623 | 20.831 | 22.239 | 28.143 | 32.623 |

compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: firepoint min 20.975 max 20.975, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p50: litellm min 20.831 max 20.831, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: firepoint min 22.127 max 22.127, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p90: litellm min 22.239 max 22.239, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: firepoint min 23.855 max 23.855, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99: litellm min 28.143 max 28.143, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: firepoint min 34.623 max 34.623, direct min 0.000 max 0.000, ranges do not overlap, firepoint is higher
compare scenario=C requested_rps=20.0 duration_s=5 litellm_workers=cpu count=10 inter-token p99.9: litellm min 32.623 max 32.623, direct min 0.000 max 0.000, ranges do not overlap, litellm is higher

### Throughput and errors

| litellm_workers | count | run | path | requests | errors | status_429 | concurrency_cap | status_codes | error_kinds | recorded_per_s | successful_per_s |
|---|---:|---:|---|---:|---:|---:|---:|---|---|---:|---:|
| cpu | 10 | 1 | direct | 100 | 100 | 0 |  | {"503": 100} |  | 20.0 | 0.0 |
| cpu | 10 | 1 | firepoint | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |
| cpu | 10 | 1 | litellm | 100 | 0 | 0 |  | {"200": 100} |  | 20.0 | 20.0 |

### CPU and RSS

| litellm_workers | count | run | path | samples | process_count_max | cpu_percent_median | cpu_percent_max | rss_bytes_median | rss_bytes_max | error |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| cpu | 10 | 1 | direct | 0 | 0 |  |  |  |  | direct path has no proxy process |
| cpu | 10 | 1 | firepoint | 9 | 1 | 6.5 | 7.2 | 23150592 | 23265280 |  |
| cpu | 10 | 1 | litellm | 9 | 12 | 119.60000000000002 | 123.30000000000001 | 2327609344 | 2330279936 |  |

### Failover counters

Mock counts are the `mock.models` object stored on the run. `first_5s` and `steady` are the loadgen split windows stored on the run.

| litellm_workers | count | run | path | mock.models | first_5s requests | first_5s errors | first_5s status_429 | steady requests | steady errors | steady status_429 |
|---|---:|---:|---|---|---:|---:|---:|---:|---:|---:|
| cpu | 10 | 1 | direct | {"primary": 299} | 100 | 100 | 0 | 0 | 0 | 0 |
| cpu | 10 | 1 | firepoint | {"fallback": 299, "primary": 1} | 100 | 0 | 0 | 0 | 0 | 0 |
| cpu | 10 | 1 | litellm | {"fallback": 299, "primary": 299} | 100 | 0 | 0 | 0 | 0 | 0 |

## Run files that were not combined

- `bench/litellm/results/A/workers-default/rps-20/run-2-firepoint.resource` samples 58 process_count_max 1 cpu_percent_median 8.1 cpu_percent_max 9.1 rss_bytes_median 24166400.0 rss_bytes_max 24264704 error ""
- `bench/litellm/results/A/workers-default/rps-20/run-2-firepoint.tmp` requests 600 errors 0 status_429 0 concurrency_cap  status_codes {"200": 600} error_kinds  duration_seconds 30
- `bench/litellm/results/A/workers-default/rps-20/run-2-litellm.resource` samples 58 process_count_max 1 cpu_percent_median 88.4 cpu_percent_max 100.1 rss_bytes_median 572350464.0 rss_bytes_max 572882944 error ""
- `bench/litellm/results/A/workers-default/rps-20/run-2-litellm.tmp` requests 599 errors 3 status_429 0 concurrency_cap 3 status_codes {"200": 596} error_kinds  duration_seconds 30
- `bench/litellm/results/A/workers-default/rps-20/run-3-litellm.resource` samples 58 process_count_max 1 cpu_percent_median 71.25 cpu_percent_max 99.6 rss_bytes_median 1009467392.0 rss_bytes_max 1018707968 error ""
- `bench/litellm/results/A/workers-default/rps-20/run-3-litellm.tmp` requests 600 errors 4 status_429 0 concurrency_cap 4 status_codes {"200": 596} error_kinds  duration_seconds 30

## Run files outside workers/rps directories

These files are not in the tables above.

- `bench/litellm/results/run-1.json` scenario "A" duration_seconds 5 litellm_workers null requests direct 100 firepoint 100 litellm 100
- `bench/litellm/results/A/run-1.json` scenario "A" duration_seconds 5 litellm_workers null requests direct 100 firepoint 100 litellm 100
- `bench/litellm/results/B/run-1.json` scenario "B" duration_seconds 5 litellm_workers null requests direct 100 firepoint 100 litellm 100
- `bench/litellm/results/C/run-1.json` scenario "C" duration_seconds 5 litellm_workers null requests direct 100 firepoint 100 litellm 100

## Limitations

- One machine. The load generator, FirePoint, LiteLLM, the mock, and scenario B's Postgres shared it. This does not include a network hop between the tester and the proxy.
- The upstream is the mock, not live Fireworks.
- The numbers are for the versions in `bench/litellm/VERSIONS.txt` and the environment files above.
- LiteLLM was run at two worker counts: 1 (`default`, the documented `DEFAULT_NUM_WORKERS_LITELLM_PROXY`) and 10 (`cpu`, the logical CPU count on this machine). The server stayed on uvicorn. Gunicorn was not enabled. Callbacks were empty, the admin UI was off, and `num_retries` was 0 on the configs that were started. Scenario B disabled spend logs and spend updates. `config-b-spend.yaml` and `config-c-default-retries.yaml` were not started. No other worker counts or proxy flags were swept.
- The five-trial, 30s run did not finish. It stopped in scenario A at 20 rps with `workers=default` after LiteLLM trials recorded `concurrency_cap` errors (3 and 4). The duration 5s cells are one trial each.
- When a section has one run file, min equals max. "Ranges do not overlap" then means the two stored numbers differ.
- `bench/results` is a different benchmark and is not included.
