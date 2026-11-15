# Benchmark

Measured on 14 Nov 2026, 4:27 PM to 4:34 PM Pacific. The load generator, the proxy, and the mock all ran on one machine. A second machine was not available, so this number does not include a network hop between the tester and the proxy.

## Hardware

- KVM virtual machine, Linux 6.12.113+
- 4 vCPU, `Intel(R) Xeon(R) Processor`, family 6, model 207, 1 thread per core
- `MemTotal` 16387328 kB (about 16 GB)
- Go 1.22.2 linux/amd64

## Method

The mock waits 200 ms before its first token, then sends 50 tokens with 20 ms between them.

```bash
./bin/mockupstream -addr 127.0.0.1:19110 -first-token-delay 200ms -token-interval 20ms -tokens 50
./bin/firerouter -listen 127.0.0.1:18090 -upstream http://127.0.0.1:19110 -rpm 1000000 -tpm 100000000
```

The request and token budgets were raised so a rejection would not be mistaken for proxy delay. No tenant key file was used.

Each trial sends 20 requests per second for 30 seconds, after a 10 second warmup that is thrown away. The concurrency cap is 64. A stream lasts about 1.2 seconds, so 20 per second needs about 24 at once, under that cap.

```bash
./bin/loadgen -url http://127.0.0.1:19110/v1/chat/completions -rate 20 -duration 30s -warmup 10s -concurrency 64
./bin/loadgen -url http://127.0.0.1:18090/v1/chat/completions -rate 20 -duration 30s -warmup 10s -concurrency 64
```

Direct and proxy trials alternated, five of each. Time to first token is the wait from send until the first `data:` line. The reported percentiles come from an HdrHistogram with 3 significant figures, so near 201 ms the values move in steps of about 0.128 ms. The overhead column is the proxy median minus the direct median.

## Time to first token

Every trial recorded 600 requests, 0 errors, and 0 responses with status 429.

|  | Direct median | Proxy median | Overhead |
|---|---:|---:|---:|
| p50 | 200.703 ms | 200.831 ms | 0.128 ms |
| p90 | 201.343 ms | 201.471 ms | 0.128 ms |
| p99 | 201.599 ms | 201.727 ms | 0.128 ms |
| p99.9 | 201.599 ms | 201.855 ms | 0.256 ms |

The direct p50 was 200.703 ms on all five trials. The proxy p50 was 200.831 ms on four trials and 200.959 ms on one.

The gap between tokens was about 20.6 ms on both paths. The proxy median was within one histogram step of the direct median, including two steps where the proxy number was 0.016 ms lower. That is the bucket size, not a claim that the proxy is faster.

## What this does not show

The default load-generator cap is 8 chats at once. At 100 requests per second that cap refuses most of the schedule before it is sent, and the proxy's default budget of 600 requests per minute would reject a real 100 per second after a short burst. Those knobs were opened here so the table is the extra wait on a chat that was actually served.
