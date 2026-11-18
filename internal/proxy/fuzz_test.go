package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"unicode/utf8"
)

// topLevel returns the keys of a JSON object in order, with each raw value.
func topLevel(t testing.TB, body []byte) ([]string, map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, false
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		key := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, false
		}
		if _, dup := vals[key]; dup {
			return nil, nil, false
		}
		keys = append(keys, key)
		vals[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, false
	}
	return keys, vals, true
}

// FuzzBodyForModel checks the forwarding rule: only "model" changes. Every
// other field keeps its bytes and its position.
func FuzzBodyForModel(f *testing.F) {
	f.Add([]byte(`{"model":"a","messages":[{"role":"user","content":"x"}],"stream":true}`), "b")
	f.Add([]byte(`{"x_unknown":{"k":[1,2,3]},"model":"a","temperature":0.10}`), "b/c")
	f.Add([]byte(`{ "model" : "a" , "n":1 }`), `q"uote`)
	f.Add([]byte(`{"messages":[],"user":"model"}`), "b")
	f.Fuzz(func(t *testing.T, body []byte, model string) {
		if !utf8.ValidString(model) {
			// JSON strings are UTF-8; the encoder rewrites invalid bytes.
			t.Skip()
		}
		beforeKeys, beforeVals, ok := topLevel(t, body)
		if !ok {
			t.Skip()
		}
		out, err := bodyForModel(body, model)
		if err != nil {
			return
		}
		afterKeys, afterVals, ok := topLevel(t, out)
		if !ok {
			t.Fatalf("output is not a JSON object: %q", out)
		}
		// A body with no model field reads as "", so asking for "" is a no-op.
		if _, has := afterVals["model"]; !has {
			if model != "" {
				t.Fatalf("model %q was not written", model)
			}
		} else {
			var got string
			if err := json.Unmarshal(afterVals["model"], &got); err != nil || got != model {
				t.Fatalf("model = %q (err %v), want %q", got, err, model)
			}
		}
		for _, k := range beforeKeys {
			if k == "model" {
				continue
			}
			if !bytes.Equal(beforeVals[k], afterVals[k]) {
				t.Fatalf("field %q changed: %s -> %s", k, beforeVals[k], afterVals[k])
			}
		}
		strip := func(keys []string) []string {
			var o []string
			for _, k := range keys {
				if k != "model" {
					o = append(o, k)
				}
			}
			return o
		}
		a, b := strip(beforeKeys), strip(afterKeys)
		if len(a) != len(b) {
			t.Fatalf("fields added or dropped: %v -> %v", a, b)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("fields reordered: %v -> %v", a, b)
			}
		}
	})
}

func FuzzRequestedModelDoesNotPanic(f *testing.F) {
	f.Add([]byte(`{"model":"a"}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, body []byte) {
		_, _ = requestedModel(body)
	})
}
