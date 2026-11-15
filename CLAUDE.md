- Request bodies are forwarded as raw JSON. Parse only the fields we need
  (model, stream, max_tokens, messages for estimation). Never drop or
  reorder unknown fields.
- When rewriting the model field for fallback, change only that field.
- Every new endpoint goes through the same auth, rate limit, breaker and
  transport as chat completions.
