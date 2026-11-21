package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestChatBodyOmitsMaxTokensUnlessSet(t *testing.T) {
	raw, err := chatBodyJSON("mock", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("max_tokens")) {
		t.Fatalf("uncapped body contains max_tokens: %s", raw)
	}

	raw, err = chatBodyJSON("mock", 16)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MaxTokens *int `json:"max_tokens"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 16 {
		t.Fatalf("max_tokens = %v, want 16", got.MaxTokens)
	}
}
