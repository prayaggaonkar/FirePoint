package proxy

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// richBody is a chat request with fields the proxy does not interpret.
// Spacing and unicode are intentional: a rewrite must not reformat them.
const richBody = `{
  "model": "big-broken",
  "stream": true,
  "max_tokens": 32,
  "messages": [{"role": "user", "content": "こんにちは — café"}],
  "tools": [{"type": "function", "function": {"name": "lookup", "parameters": {"type": "object"}}}],
  "tool_choice": {"type": "function", "function": {"name": "lookup"}},
  "response_format": {"type": "json_schema", "json_schema": {"name": "out", "schema": {"type": "object"}}},
  "future_field": {"keep": true, "model": "not-the-top-level", "note": "do not drop", "nested": ["a", 1, null]}
}`

func TestBodyForModelKeepsOriginalBytes(t *testing.T) {
	in := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`)
	got, err := bodyForModel(in, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("body = %s, want the original bytes", got)
	}
}

func TestBodyForModelRewritesOnlyTheName(t *testing.T) {
	in := []byte(`{"model":"big-broken","messages":[{"role":"user","content":"hi"}]}`)
	got, err := bodyForModel(in, "small")
	if err != nil {
		t.Fatal(err)
	}
	model, err := requestedModel(got)
	if err != nil {
		t.Fatal(err)
	}
	if model != "small" {
		t.Fatalf("model = %q, want small", model)
	}
	if !strings.Contains(string(got), `"content":"hi"`) {
		t.Fatalf("body lost the message: %s", got)
	}
	// Everything except the model value is the client's original bytes.
	if strings.Replace(string(got), `"small"`, `"big-broken"`, 1) != string(in) {
		t.Fatalf("rewrite changed more than the model value:\n got %s\nwant %s", got, in)
	}
}

func TestInjectIncludeUsageOnlyOnStreams(t *testing.T) {
	plain := []byte(`{"model":"m","stream":false,"future_field":{"keep":true}}`)
	got, err := injectIncludeUsage(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("non-stream body changed: %s", got)
	}

	in := []byte(`{"model":"m","stream":true,"future_field":{"keep":true},"messages":[{"content":"こんにちは"}]}`)
	got, err = injectIncludeUsage(in)
	if err != nil {
		t.Fatal(err)
	}
	var have map[string]any
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	opts, _ := have["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options = %#v", have["stream_options"])
	}
	if !strings.Contains(string(got), `"content":"こんにちは"`) || !strings.Contains(string(got), `"future_field":{"keep":true}`) {
		t.Fatalf("inject rewrote other fields: %s", got)
	}

	withOpts := []byte(`{"stream":true,"stream_options":{"foo":1}}`)
	got, err = injectIncludeUsage(withOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"foo":1`) || !strings.Contains(string(got), `"include_usage":true`) {
		t.Fatalf("existing stream_options lost a field: %s", got)
	}
}

func assertSameJSONExceptModel(t *testing.T, original, got []byte, model string) {
	t.Helper()
	var want, have map[string]any
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	want["model"] = model
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("decoded body = %#v\nwant %#v", have, want)
	}
}

func TestBodyForModelKeepsUnknownFields(t *testing.T) {
	in := []byte(richBody)
	got, err := bodyForModel(in, "small")
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSONExceptModel(t, in, got, "small")
	for _, keep := range []string{
		`"content": "こんにちは — café"`,
		`"tools": [{"type": "function"`,
		`"tool_choice": {"type": "function"`,
		`"response_format": {"type": "json_schema"`,
		`"future_field": {"keep": true, "model": "not-the-top-level", "note": "do not drop", "nested": ["a", 1, null]}`,
	} {
		if !strings.Contains(string(got), keep) {
			t.Fatalf("body lost %s\n%s", keep, got)
		}
	}
}
