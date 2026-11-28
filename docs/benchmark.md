# Benchmark

Run on 27 Nov 2026. The load generator, the proxy, and the mock all ran on one machine, so the numbers do not include a network hop between the tester and the proxy.

## Machine

- Apple M4, 10 cores
- Darwin 25.6.0 arm64
- Go 1.27.4 darwin/arm64

## Method

The mock waits 200 ms before its first token, then sends 50 tokens with 20 ms between them, a final chunk with `finish_reason` `stop`, and `data: [DONE]`.

`bench/run_bench.sh` builds the binaries, starts the mock and FirePoint with the request and token budgets raised (so a rejection is not mistaken for proxy delay and no tenant key file is used), and runs five paired trials, alternating direct and proxied. Each trial sends 20 requests per second for 30 seconds after a 10 second warmup that is thrown away, with a concurrency cap of 64. A stream lasts about 1.2 seconds, so 20 per second needs about 24 at once, under the cap.

Time to first token is the wait from send until the first `data:` line. Percentiles come from an HdrHistogram with 3 significant figures. Delta is the proxied median minus the direct median. Min and max are across the five trials.

Every trial recorded 600 requests, 0 errors, and 0 responses with status 429.

## Results

| metric | percentile | direct median | direct min | direct max | proxied median | proxied min | proxied max | delta (ms) |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| TTFT | p50 | 200.575 | 200.575 | 200.703 | 201.087 | 201.087 | 201.215 | 0.512 |
| TTFT | p90 | 200.831 | 200.703 | 200.959 | 201.471 | 201.343 | 201.599 | 0.640 |
| TTFT | p99 | 201.215 | 201.087 | 201.343 | 201.855 | 201.599 | 201.855 | 0.640 |
| TTFT | p99.9 | 201.343 | 201.215 | 201.471 | 202.111 | 201.727 | 209.535 | 0.768 |
| inter-token | p50 | 20.447 | 20.447 | 20.479 | 20.447 | 20.431 | 20.479 | 0.000 |
| inter-token | p90 | 20.895 | 20.879 | 20.927 | 20.927 | 20.895 | 20.943 | 0.032 |
| inter-token | p99 | 21.215 | 21.199 | 21.247 | 21.279 | 21.247 | 21.295 | 0.064 |
| inter-token | p99.9 | 21.439 | 21.343 | 21.567 | 21.583 | 21.439 | 22.015 | 0.144 |

## Reading the numbers

- The proxy adds about half a millisecond to time to first token at p50. The direct and proxied p50 ranges across trials do not overlap, so this is a real cost and not noise.
- Inter-token spacing is the same through the proxy at p50 and within 0.144 ms at p99.9.
- One proxied trial had a p99.9 TTFT of 209.535 ms. The other four were between 201.727 and 202.239 ms. The cause was not investigated.

## Reproduce

```bash
./bench/run_bench.sh
python3 bench/analyze.py bench/results
```

Environment variables such as `BENCH_RATE` and `BENCH_DURATION` change the run; see the top of `bench/run_bench.sh`.

## What this does not show

This is a mock upstream on one machine, not live Fireworks. The default load-generator cap is 8 chats at once, and the proxy's default budget of 600 requests per minute would reject sustained load, so those knobs were opened here so the table is the extra wait on a chat that was actually served.
