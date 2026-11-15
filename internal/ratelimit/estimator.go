package ratelimit

import "encoding/json"

const defaultMaxTokens = 1024

// EstimateTokens reserves a worst case before the model runs. The real count
// arrives only in the final usage chunk, which is too late to refuse the call.
// Prompt text is about 4 bytes per token, plus a few tokens of message
// overhead, plus whatever the caller asked to generate.
func EstimateTokens(body []byte) int {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens *int `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return defaultMaxTokens
	}

	chars := 0
	for _, m := range req.Messages {
		chars += contentBytes(m.Content)
	}
	maxTokens := defaultMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	return chars/4 + 4*len(req.Messages) + maxTokens
}

// EstimateEmbeddingTokens counts input text at about 4 bytes per token.
// input may be one string or a list of strings. A list of token numbers
// uses the raw JSON length, since those items are not text.
func EstimateEmbeddingTokens(body []byte) int {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	return embeddingInputLen(req.Input) / 4
}

func embeddingInputLen(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return len(text)
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return len(raw)
	}
	n := 0
	for _, part := range parts {
		var item string
		if err := json.Unmarshal(part, &item); err == nil {
			n += len(item)
			continue
		}
		n += len(part)
	}
	return n
}

func contentBytes(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0
	}
	return len(text)
}
