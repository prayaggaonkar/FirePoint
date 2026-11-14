package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/ratelimit"
	"github.com/prayaggaonkar/firerouter/internal/router"
)

const maxRequestBody = 1 << 20 // 1 MB

// servedModelHeader names the candidate that produced the committed response.
const servedModelHeader = "X-FireRouter-Model"

// copyBufPool holds 16 KB scratch buffers for the response stream. The buffer
// is only for the read/write loop; it is not a place to accumulate the body.
var copyBufPool = sync.Pool{
	New: func() any {
		return make([]byte, 16*1024)
	},
}

// Handler forwards /v1/chat/completions and flushes each chunk as it arrives.
// A failing candidate is replaced only before response headers are written.
type Handler struct {
	upstream *url.URL
	client   *http.Client
	// apiKey is the gateway secret. When set, it replaces any client
	// Authorization header so apps never hold the upstream key.
	apiKey string
	// limiter is per client. It stays nil in tests that are not checking
	// quotas. The process always sets one before serving traffic.
	limiter *ratelimit.Limiter
	// router lists fallbacks. Nil means the requested model is the only try.
	router *router.Router
	// breakers is one circuit per model. Nil skips the check.
	breakers *breaker.Registry
}

// NewHandler parses the upstream base URL. client is the shared client from
// startup; a nil client uses NewClient. apiKey comes from FIREWORKS_API_KEY
// and may be empty.
func NewHandler(upstream string, client *http.Client, apiKey string) (*Handler, error) {
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
	return &Handler{
		upstream: u,
		client:   client,
		apiKey:   apiKey,
		breakers: breaker.NewRegistry(breaker.DefaultConfig()),
	}, nil
}

// SetRouter installs the fallback list. A nil router tries only the model
// named in the request.
func (h *Handler) SetRouter(r *router.Router) {
	h.router = r
}

// SetBreakers replaces the per-model breakers. Tests use this to trip a
// model without waiting for the default sample size.
func (h *Handler) SetBreakers(reg *breaker.Registry) {
	h.breakers = reg
}

// SetLimiter turns on per-client limits. requestsPerMinute and tokensPerMinute
// are the burst and the sustained rate for one Authorization value.
func (h *Handler) SetLimiter(requestsPerMinute, tokensPerMinute int) {
	h.limiter = ratelimit.NewLimiter(requestsPerMinute, tokensPerMinute)
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

	requested, err := requestedModel(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
		return
	}

	// The raw credential is the tenant id. Only its hash is kept, so a limit
	// bucket cannot be turned back into a key and nothing here is logged.
	tenant := tenantID(r.Header.Get("Authorization"))
	reserved := 0
	if h.limiter != nil {
		reserved = ratelimit.EstimateTokens(body)
		ok, retryAfter := h.limiter.Allow(tenant, reserved, time.Now())
		if !ok {
			writeRateLimited(w, retryAfter)
			return
		}
	}

	// Try each candidate until one answers with headers. A later failure
	// cannot switch models: the client may already be reading the body.
	for _, model := range h.candidates(requested) {
		tryNext := h.attempt(w, r, tenant, reserved, model, body)
		if !tryNext {
			return
		}
	}
	h.refundTokens(tenant, reserved)
	if r.Context().Err() != nil {
		return
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, "No upstream model is available.", "server_error")
}

// attempt sends one candidate. It returns true when the caller should try
// the next model, and false when the response is finished or the client left.
func (h *Handler) attempt(w http.ResponseWriter, r *http.Request, tenant string, reserved int, model string, body []byte) (tryNext bool) {
	report, err := h.allow(model)
	if err != nil {
		log.Printf("model %s skipped: circuit breaker open", model)
		return true
	}
	payload, err := bodyForModel(body, model)
	if err != nil {
		report.Skip()
		h.refundTokens(tenant, reserved)
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
		return false
	}
	target, err := h.upstreamRequestURL(r)
	if err != nil {
		report.Skip()
		h.refundTokens(tenant, reserved)
		writeOpenAIError(w, http.StatusBadGateway, "The upstream request failed.", "server_error")
		return false
	}
	// The incoming context is cancelled when the client disconnects, which
	// cancels this upstream call so a closed tab stops the paid request.
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		report.Skip()
		h.refundTokens(tenant, reserved)
		writeOpenAIError(w, http.StatusBadGateway, "The upstream request failed.", "server_error")
		return false
	}
	h.copyRequestHeaders(upReq.Header, r.Header)
	upReq.ContentLength = int64(len(payload))

	resp, err := h.client.Do(upReq)
	if err != nil {
		if r.Context().Err() != nil {
			report.Skip()
			return false
		}
		log.Printf("upstream model %s: %v", model, err)
		report.Failure()
		return true
	}
	if resp.StatusCode >= 500 {
		// Headers never reached the client, so another candidate can still serve.
		log.Printf("upstream model %s: status %d", model, resp.StatusCode)
		report.Failure()
		discardBody(resp.Body)
		return true
	}
	report.Success()
	h.forward(w, r, resp, model, tenant, reserved)
	return false
}

func (h *Handler) allow(model string) (*breaker.Report, error) {
	if h.breakers == nil {
		return nil, nil
	}
	return h.breakers.Allow(model, time.Now())
}

func (h *Handler) candidates(model string) []string {
	if h.router == nil {
		return []string{model}
	}
	return h.router.Candidates(model)
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, resp *http.Response, model, tenant string, reserved int) {
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if isEventStream(resp.Header.Get("Content-Type")) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	// Writing headers commits this model. The body is streamed as it arrives
	// and is not replaced if the upstream fails halfway through.
	w.Header().Set(servedModelHeader, model)
	w.WriteHeader(resp.StatusCode)

	tail, err := streamBody(w, resp.Body)
	if err != nil && r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
		log.Printf("streaming response: %v", err)
	}
	if actual, ok := actualTokens(tail); ok {
		h.refundTokens(tenant, reserved-actual)
	}
}

func discardBody(rc io.ReadCloser) {
	defer rc.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, maxRequestBody))
}

func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	// Cap applies to the inbound request only. The upstream response is streamed.
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
}

// upstreamRequestURL appends the request path to the configured base.
// Replacing URL.Path would drop a prefix such as /inference, so
// https://api.fireworks.ai/inference/v1/chat/completions would be wrong.
func (h *Handler) upstreamRequestURL(r *http.Request) (string, error) {
	joined, err := url.JoinPath(h.upstream.String(), r.URL.EscapedPath())
	if err != nil {
		return "", err
	}
	u, err := url.Parse(joined)
	if err != nil {
		return "", err
	}
	u.RawQuery = r.URL.RawQuery
	return u.String(), nil
}

// copyRequestHeaders forwards the headers the upstream needs and leaves out
// hop-by-hop headers (Connection, Keep-Alive, Transfer-Encoding, Upgrade),
// which describe this connection rather than the chat request.
// The gateway key, when configured, replaces the client Authorization header.
// The key itself is never logged.
func (h *Handler) copyRequestHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Accept"} {
		if v := src.Get(name); v != "" {
			dst.Set(name, v)
		}
	}
	if h.apiKey != "" {
		dst.Set("Authorization", "Bearer "+h.apiKey)
		return
	}
	if v := src.Get("Authorization"); v != "" {
		dst.Set("Authorization", v)
	}
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// streamBody copies upstream bytes to the client one read at a time. Flush
// pushes those bytes onto the network immediately; without it the server
// waits to fill a buffer and the user sees the whole answer at once.
// The returned tail is only the last 4 KB, which is where the usage object sits.
func streamBody(dst http.ResponseWriter, src io.Reader) ([]byte, error) {
	buf := copyBufPool.Get().([]byte)
	defer copyBufPool.Put(buf)
	var tail tailBuf

	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return tail.bytes(), werr
			}
			if ferr := http.NewResponseController(dst).Flush(); ferr != nil {
				return tail.bytes(), ferr
			}
			tail.write(buf[:n])
		}
		if rerr == io.EOF {
			return tail.bytes(), nil
		}
		if rerr != nil {
			return tail.bytes(), rerr
		}
	}
}

func (h *Handler) refundTokens(tenant string, tokens int) {
	if h.limiter == nil || tokens <= 0 {
		return
	}
	h.limiter.Refund(tenant, tokens)
}

func tenantID(authorization string) string {
	sum := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(sum[:])
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
	writeOpenAIError(w, http.StatusTooManyRequests, "Rate limit exceeded.", "rate_limit_exceeded")
}

func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return secs
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
