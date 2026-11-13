package proxy

import (
	"net"
	"net/http"
	"time"
)

// NewTransport is the process-wide connection pool. Reusing it keeps TLS
// connections open so each chat request does not pay to dial again.
func NewTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		// Compression would buffer the SSE body before the proxy can flush it.
		DisableCompression: true,
		// HTTP/2 multiplexes many streams on the connections this pool keeps.
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: 3 * time.Second,
		}).DialContext,
	}
}

// NewClient returns the single client that should be built at startup and
// shared by every request. It has no overall timeout: a timeout here would
// cut off a stream that is still sending tokens.
func NewClient() *http.Client {
	return &http.Client{Transport: NewTransport()}
}
