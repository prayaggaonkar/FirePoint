package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
)

// requestedModel reads the model field. The rest of the body is left for the
// upstream call. Invalid JSON is rejected here so a fallback rewrite cannot
// invent a body the client did not send.
func requestedModel(body []byte) (string, error) {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", err
	}
	return probe.Model, nil
}

// bodyForModel returns the upstream body for one candidate. When the model
// field already matches, the original bytes are kept. When it does not, only
// that key's value is replaced. Rebuilding the whole object would sort keys
// and escape characters the client wrote.
func bodyForModel(body []byte, model string) ([]byte, error) {
	// Parsing into RawMessage checks that the body is an object and keeps
	// every field, including ones this proxy does not know about.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	current, err := requestedModel(body)
	if err != nil {
		return nil, err
	}
	if current == model {
		return body, nil
	}
	encoded, err := marshalJSONString(model)
	if err != nil {
		return nil, err
	}
	return replaceTopLevelValue(body, "model", encoded)
}

// injectIncludeUsage sets stream_options.include_usage to true when the client
// asked for a stream. Other fields stay as sent. A non-stream body is unchanged,
// so a caller who did not ask for tokens does not suddenly get a usage object.
func injectIncludeUsage(body []byte) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if !bytes.Equal(bytes.TrimSpace(obj["stream"]), []byte("true")) {
		return body, nil
	}
	raw := bytes.TrimSpace(obj["stream_options"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return replaceTopLevelValue(body, "stream_options", []byte(`{"include_usage":true}`))
	}
	updated, err := replaceTopLevelValue(raw, "include_usage", []byte("true"))
	if err != nil {
		return nil, err
	}
	return replaceTopLevelValue(body, "stream_options", updated)
}

// marshalJSONString encodes one JSON string without HTML escapes, so a model
// name is written as the client would write it.
func marshalJSONString(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// replaceTopLevelValue swaps the raw value of one top-level key. Nested keys
// of the same name are left alone. A missing key is inserted before the
// closing brace.
func replaceTopLevelValue(body []byte, key string, encoded []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("request body must be a JSON object")
	}

	var start, end int
	found := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := keyTok.(string)
		if !ok {
			return nil, errors.New("request body must be a JSON object")
		}
		afterKey := int(dec.InputOffset())
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if name != key {
			continue
		}
		// The last duplicate key wins, matching encoding/json.
		start = valueStart(body, afterKey, int(dec.InputOffset()))
		end = int(dec.InputOffset())
		found = true
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if !found {
		return insertTopLevelValue(body, int(dec.InputOffset()), key, encoded)
	}
	out := make([]byte, 0, len(body)-(end-start)+len(encoded))
	out = append(out, body[:start]...)
	out = append(out, encoded...)
	out = append(out, body[end:]...)
	return out, nil
}

// valueStart is the first byte of a JSON value. afterKey points just past
// the key, so the colon and any spaces still sit in front of the value.
func valueStart(body []byte, afterKey, valEnd int) int {
	i := afterKey
	for i < valEnd && isJSONSpace(body[i]) {
		i++
	}
	if i < valEnd && body[i] == ':' {
		i++
	}
	for i < valEnd && isJSONSpace(body[i]) {
		i++
	}
	return i
}

func insertTopLevelValue(body []byte, afterObject int, key string, encoded []byte) ([]byte, error) {
	closeAt := afterObject - 1
	for closeAt >= 0 && body[closeAt] != '}' {
		closeAt--
	}
	if closeAt < 0 {
		return nil, errors.New("request body must be a JSON object")
	}
	prev := closeAt - 1
	for prev >= 0 && isJSONSpace(body[prev]) {
		prev--
	}
	if prev < 0 {
		return nil, errors.New("request body must be a JSON object")
	}
	var insert []byte
	if body[prev] == '{' {
		insert = append([]byte(`"`+key+`":`), encoded...)
	} else {
		insert = append([]byte(`,"`+key+`":`), encoded...)
	}
	out := make([]byte, 0, len(body)+len(insert))
	out = append(out, body[:closeAt]...)
	out = append(out, insert...)
	out = append(out, body[closeAt:]...)
	return out, nil
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
