package proxy

import (
	"bytes"
	"strings"
	"testing"
)

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
}
