package proxy

import (
	"net/http"
	"testing"
)

func TestCopySafeResponseHeadersSkipsHopByHop(t *testing.T) {
	src := make(http.Header)
	src.Set("Content-Type", "application/json")
	src.Set("X-Request-Id", "mock-req")
	src.Set("Upgrade", "nope")
	src.Set("Content-Length", "10")
	src.Set("Connection", "X-Scratch")
	src.Set("X-Scratch", "drop-me")

	dst := make(http.Header)
	copySafeResponseHeaders(dst, src)

	if dst.Get("Content-Type") != "application/json" || dst.Get("X-Request-Id") != "mock-req" {
		t.Fatalf("safe headers = %v", dst)
	}
	if dst.Get("Upgrade") != "" || dst.Get("Content-Length") != "" || dst.Get("X-Scratch") != "" || dst.Get("Connection") != "" {
		t.Fatalf("hop-by-hop headers were copied: %v", dst)
	}
}
