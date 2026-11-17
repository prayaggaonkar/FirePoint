package proxy

import (
	"net"
	"net/http"
	"time"
)

// DefaultHeaderTimeout is how long the upstream may take to send response
// headers. A non-streaming completion sends no headers until generation is
// finished, and a large or cold model can take a while to start streaming,
// so a short value would report a healthy model as down.
const DefaultHeaderTimeout = 60 * time.Second

// NewTransport is the process-wide connection pool. Reusing it keeps TLS
// connections open so each chat request does not pay to dial again.
// headerTimeout of zero or less uses DefaultHeaderTimeout.
func NewTransport(headerTimeout time.Duration) *http.Transport {
	if headerTimeout <= 0 {
		headerTimeout = DefaultHeaderTimeout
	}
	return &http.Transport{
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		// Compression would buffer the SSE body before the proxy can flush it.
		DisableCompression: true,
		// HTTP/2 multiplexes many streams on the connections this pool keeps.
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: headerTimeout,
		DialContext: (&net.Dialer{
			Timeout: 3 * time.Second,
		}).DialContext,
	}
}

// NewClient returns the single client that should be built at startup and
// shared by every request. It has no overall timeout: a timeout here would
// cut off a stream that is still sending tokens.
func NewClient() *http.Client {
	return NewClientWithHeaderTimeout(DefaultHeaderTimeout)
}

// NewClientWithHeaderTimeout is NewClient with a chosen header wait.
func NewClientWithHeaderTimeout(headerTimeout time.Duration) *http.Client {
	return &http.Client{Transport: NewTransport(headerTimeout)}
}
