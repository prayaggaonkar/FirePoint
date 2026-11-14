package proxy

import "encoding/json"

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
// field already matches, the original bytes are kept so key order and spacing
// stay as the client sent them.
func bodyForModel(body []byte, model string) ([]byte, error) {
	current, err := requestedModel(body)
	if err != nil {
		return nil, err
	}
	if current == model {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	obj["model"] = encoded
	return json.Marshal(obj)
}
