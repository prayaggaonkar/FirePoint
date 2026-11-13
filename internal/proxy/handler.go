package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const maxRequestBody = 1 << 20 // 1 MB

// copyBufPool holds 16 KB scratch buffers for the response stream. The buffer
// is only for the read/write loop; it is not a place to accumulate the body.
var copyBufPool = sync.Pool{
	New: func() any {
		return make([]byte, 16*1024)
	},
}

// Handler forwards /v1/chat/completions to one upstream and flushes each
// chunk as it arrives.
type Handler struct {
	upstream *url.URL
	client   *http.Client
}

// NewHandler parses the upstream base URL. client is the shared client from
// startup; a nil client uses NewClient.
func NewHandler(upstream string, client *http.Client) (*Handler, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("upstream must be an absolute URL")
	}
	if client == nil {
		client = NewClient()
	}
	return &Handler{upstream: u, client: client}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := readRequestBody(w, r)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
		return
	}

	upstreamURL := *h.upstream
	upstreamURL.Path = r.URL.Path
	upstreamURL.RawQuery = r.URL.RawQuery
	// The incoming context is cancelled when the client disconnects, which
	// cancels this upstream call so a closed tab stops the paid request.
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "The upstream request failed.", "server_error")
		return
	}
	copyRequestHeaders(upReq.Header, r.Header)
	upReq.ContentLength = int64(len(body))

	resp, err := h.client.Do(upReq)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		log.Printf("upstream request failed: %v", err)
		writeOpenAIError(w, http.StatusBadGateway, "The upstream request failed.", "server_error")
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if isEventStream(resp.Header.Get("Content-Type")) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)

	if err := streamBody(w, resp.Body); err != nil && r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
		log.Printf("streaming response: %v", err)
	}
}

func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	// Cap applies to the inbound request only. The upstream response is streamed.
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
}

// copyRequestHeaders forwards the headers the upstream needs and leaves out
// hop-by-hop headers (Connection, Keep-Alive, Transfer-Encoding, Upgrade),
// which describe this connection rather than the chat request.
func copyRequestHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Accept", "Authorization"} {
		if v := src.Get(name); v != "" {
			dst.Set(name, v)
		}
	}
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// streamBody copies upstream bytes to the client one read at a time. Flush
// pushes those bytes onto the network immediately; without it the server
// waits to fill a buffer and the user sees the whole answer at once.
func streamBody(dst http.ResponseWriter, src io.Reader) error {
	buf := copyBufPool.Get().([]byte)
	defer copyBufPool.Put(buf)

	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if ferr := http.NewResponseController(dst).Flush(); ferr != nil {
				return ferr
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

func writeOpenAIError(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{
		Error: apiErrorBody{Message: message, Type: typ},
	})
}
