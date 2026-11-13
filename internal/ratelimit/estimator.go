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
