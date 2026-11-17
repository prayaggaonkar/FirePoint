package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The README promises HTTP/2 to the upstream. This runs the shared transport
// against a TLS server and checks both the protocol and that chunks still
// reach the client while the upstream is mid-stream.
func TestUpstreamUsesHTTP2AndStreamsEachChunk(t *testing.T) {
	release := make(chan struct{})
	var proto int
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto = r.ProtoMajor
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()

	pool := x509.NewCertPool()
	pool.AddCert(upstream.Certificate())
	tr := NewTransport(0)
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	h, err := NewHandler(upstream.URL, &http.Client{Transport: tr}, "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "", `{"model":"m","stream":true}`)
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := rd.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.HasPrefix(line, "data: one") {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first chunk was held back while the upstream was still streaming")
	}
	close(release)
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("rest %q", rest)
	}
	if proto != 2 {
		t.Fatalf("upstream saw HTTP/%d, want HTTP/2", proto)
	}
}
