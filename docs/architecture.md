# Architecture

FireRouter is a middleman. An app sends a chat to FireRouter. FireRouter sends that chat to a model server, then copies the answer back one piece at a time.

```text
app  --POST /v1/chat/completions-->  firerouter :8080
                                        |  check the caller's key
                                        |  check that caller's budget
                                        |  pick a model, skip one that is failing
                                        |  copy each chunk as it arrives
                                        v
                                   model server
                                   (mock on :9000, or Fireworks)
```

The model server in production is Fireworks over HTTPS: `https://api.fireworks.ai/inference/v1/chat/completions`. FireRouter does not run the model. `mockupstream` is a stand-in for tests.

## What a request looks like

The app sends JSON:

```json
{"model":"small","stream":true,"messages":[{"role":"user","content":"hi"}]}
```

`stream: true` means the answer should arrive piece by piece. Each piece is a line starting with `data:`. The last line is `data: [DONE]`.

A bad request gets JSON and a status code instead of those lines: 400 for a body the proxy cannot read, 401 for an unknown key, 429 when the caller is over budget, 503 when no model can answer.

## The steps, in order

1. If a key file was passed with `-keys`, the `Authorization: Bearer fr-…` value is hashed and compared with the sha256 hashes in that file. The comparison does not stop at the first different byte. The tenant name next to the matching hash is the budget name and the `tenant` label on rate-limit rejections. A missing or unknown key is rejected with 401 before the prompt is read. `allowed_models`, when present, rejects any other model with 403. With no key file, every caller is allowed and the budget name is a hash of whatever header they sent. Use a key file whenever the process holds a real Fireworks key. `firerouter keygen` prints a new key once and the hash to put in the file.
2. The body is read, up to 1 MB.
3. The caller's budget is checked. With a key file, each tenant's `rpm` and `tpm` come from that file. Without one, the process flags apply (`-rpm` default 600, `-tpm` default 200000). A token is a small piece of text. The proxy guesses the cost before the model runs (about 4 characters per token, plus `max_tokens`). At the end of the stream it reads the `usage` object and corrects the guess.
4. The model list is the requested name, then the backups in the JSON config, in order. Backups of backups are not followed.
5. Each model has a health switch. After 20 samples in 30 seconds, if half or more failed, the switch opens for 15 seconds and new chats skip that model. Then 3 trial chats are allowed. Success closes the switch. A failure is a dropped connection, headers that never arrive within 10 seconds, or status 500, 502, 503, or 504. A 400-class status is the caller's fault and does not count. An upstream 429 does not count as a failure either.
6. The proxy sends the body upstream with the model field set to the candidate it is trying. `FIREWORKS_API_KEY`, when set, is the `Authorization` header the model server sees. The app's key is never copied onto that request.
7. Headers are the commit point. A failure before headers, or a 5xx, tries the next model. The first good response is the one the app sees. The header `X-FireRouter-Model` names it. After that, bytes are copied and flushed immediately. A break in the middle of the answer is not retried.
8. If the app disconnects, the upstream request is cancelled.

One HTTP client is shared for the life of the process. It reuses connections, allows HTTP/2, and does not compress the stream. Compression would hold bytes back. There is no overall timeout on a stream.

## Other doors

`GET /healthz` answers `ok`. It does not call the model.

`GET /metrics` is a Prometheus page: time to the first response byte, time the proxy itself added, open streams, upstream status codes, rate-limit rejections, and each model's switch position. Prompts and keys are not labels.

On SIGHUP the key file is read again and swapped in. Streams that already started are left alone. A file that does not parse keeps the previous keys. On SIGINT or SIGTERM the process stops accepting new calls and waits up to 30 seconds for streams to finish.

## Programs

| Program | Role |
|---|---|
| `firerouter` | The proxy. |
| `mockupstream` | A fake model. A model name containing `broken` returns 503. |
| `loadgen` | Sends chats on a fixed timer and reports wait times. |

`loadgen` measures from the moment it sends until the first `data:` line. The proxy's own metric measures until the first response byte, which is a few bytes earlier. Both are "how long until the answer starts." They are not the same stopwatch.
