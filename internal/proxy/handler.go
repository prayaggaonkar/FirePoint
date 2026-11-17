package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"sync/atomic"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/auth"
	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/observability"
	"github.com/prayaggaonkar/firerouter/internal/ratelimit"
	"github.com/prayaggaonkar/firerouter/internal/router"
)

const maxRequestBody = 1 << 20 // 1 MB

// modelsBreaker is the circuit for GET /v1/models. It is not a model name.
const modelsBreaker = "/v1/models"

type routeKind int

const (
	routeChat routeKind = iota
	routeCompletions
	routeEmbeddings
	routeModels
)

// servedModelHeader names the candidate that produced the committed response.
const servedModelHeader = "X-FireRouter-Model"

// copyBufPool holds 16 KB scratch buffers for the response stream. The buffer
// is only for the read/write loop; it is not a place to accumulate the body.
var copyBufPool = sync.Pool{
	New: func() any {
		// A pointer, so Put does not allocate a new slice header.
		buf := make([]byte, 16*1024)
		return &buf
	},
}

// Handler forwards chat completions, completions, embeddings, and the model
// list. A streamed body is flushed as each chunk arrives. A failing candidate
// is replaced only before response headers are written.
type Handler struct {
	upstream *url.URL
	client   *http.Client
	// apiKey is FIREWORKS_API_KEY. It is the only Authorization value ever
	// sent upstream. The caller's virtual key is not forwarded.
	apiKey string
	// limiter is per client. It stays nil in tests that are not checking
	// quotas. The process always sets one before serving traffic.
	limiter *ratelimit.Limiter
	// keys, when set, requires a known Bearer key and names the tenant.
	// Nil keeps the open mode used by local tests: any caller is allowed,
	// and the tenant id is a hash of whatever Authorization header they sent.
	// The pointer is swapped on SIGHUP. A request reads it once, so a reload
	// does not touch a stream that has already started.
	keys atomic.Pointer[auth.Store]
	// router lists fallbacks. Nil means the requested model is the only try.
	router *router.Router
	// breakers is one circuit per model. Nil falls through to breaker.
	breakers *breaker.Registry
	// breaker is one circuit for every model. Tests that are not about
	// fallback use it. Nil skips the check.
	breaker *breaker.Breaker
	// metrics is nil in tests that are not checking /metrics.
	metrics *observability.Metrics
	// injectUsage asks the upstream for a usage chunk on streamed chats so
	// the token refund can use the real count. Off by default.
	injectUsage bool
	// maxBody caps the inbound request. Zero keeps maxRequestBody.
	// The upstream response is still streamed and is not held to this cap.
	maxBody int64
	// clock is the time source for latency samples and breaker decisions.
	clock func() time.Time
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
	return &Handler{upstream: u, client: client, apiKey: apiKey, clock: time.Now}, nil
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
// are the burst and the sustained rate when no key file is loaded. A key
// file uses the rpm and tpm on each tenant instead.
func (h *Handler) SetLimiter(requestsPerMinute, tokensPerMinute int) {
	h.limiter = ratelimit.NewLimiter(requestsPerMinute, tokensPerMinute)
}

// SetKeys requires a key from the store. The tenant name in that file is the
// rate-limit bucket and the metrics label. Nil leaves the proxy open.
func (h *Handler) SetKeys(keys *auth.Store) {
	h.keys.Store(keys)
}

// ReloadKeys reads the key file and swaps it in. A bad file leaves the
// previous store in place. In-flight requests already copied their tenant
// and are not interrupted.
func (h *Handler) ReloadKeys(path string) error {
	keys, err := auth.Load(path)
	if err != nil {
		return err
	}
	h.keys.Store(keys)
	return nil
}

// SetBreaker installs the upstream circuit breaker.
func (h *Handler) SetBreaker(b *breaker.Breaker) {
	h.breaker = b
}

// SetMetrics installs the Prometheus series. Nil leaves recording as a no-op.
func (h *Handler) SetMetrics(m *observability.Metrics) {
	h.metrics = m
}

// SetInjectUsage turns on stream_options.include_usage for streamed requests.
func (h *Handler) SetInjectUsage(on bool) {
	h.injectUsage = on
}

// SetMaxBody caps the inbound request body. Zero and negative values keep
// the built-in 1 MB cap. The response stream is not capped.
func (h *Handler) SetMaxBody(n int64) {
	if n > 0 {
		h.maxBody = n
	}
}

// MaxBody is the inbound cap currently in effect.
func (h *Handler) MaxBody() int64 {
	return h.maxBodyLimit()
}

func (h *Handler) maxBodyLimit() int64 {
	if h.maxBody > 0 {
		return h.maxBody
	}
	return maxRequestBody
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	kind, ok := routeOf(r)
	if !ok {
		w.Header().Set("Allow", allowHeader(r.URL.Path))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Check the key before reading the body so a stranger's prompt is never stored.
	who, ok := h.authenticate(r.Header.Get("Authorization"))
	if !ok {
		writeOpenAIError(w, http.StatusUnauthorized, "Invalid API key.", "invalid_api_key")
		return
	}
	// The id is the only request detail written to the log. Prompts and keys stay out.
	reqID := requestID(r)
	log.Printf("request %s %s %s", reqID, r.Method, r.URL.Path)
	// Latency starts when the request is accepted, before the body is read.
	received := h.now()

	var body []byte
	if kind != routeModels {
		var err error
		body, err = readRequestBody(w, r, h.maxBodyLimit())
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
			return
		}
	}

	// The model list has no model field. Its breaker is the route itself, so
	// a down list does not open the breaker for a chat model.
	requested := modelsBreaker
	if kind != routeModels {
		var err error
		requested, err = requestedModel(body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
			return
		}
	}

	if kind != routeModels && !who.allows(requested) {
		writeCodedError(w, http.StatusForbidden, "The model is not allowed for this API key.", "invalid_request_error", "model_not_allowed")
		return
	}

	reserved := 0
	if h.limiter != nil {
		reserved = estimateTokens(kind, body)
		var retryAfter time.Duration
		if who.configured {
			ok, retryAfter = h.limiter.AllowTenant(who.name, who.rpm, who.tpm, reserved, h.now())
		} else {
			ok, retryAfter = h.limiter.Allow(who.name, reserved, h.now())
		}
		if !ok {
			h.metrics.RateLimited(who.metricName())
			writeRateLimited(w, retryAfter)
			return
		}
	}

	// Try each candidate until one answers with headers. A later failure
	// cannot switch models: the client may already be reading the body.
	// A fallback the tenant is not allowed to call is skipped.
	sawFailure := false
	for _, model := range h.candidates(requested) {
		if kind != routeModels && !who.allows(model) {
			continue
		}
		switch h.attempt(w, r, who.name, reserved, model, body, received, reqID, kind != routeModels) {
		case attemptDone:
			return
		case attemptFailed:
			sawFailure = true
		}
	}
	h.refundTokens(who.name, reserved)
	if r.Context().Err() != nil {
		return
	}
	// An open breaker never reached an upstream. A failed dial or a 5xx did.
	message := "The upstream is unavailable."
	if sawFailure {
		message = "No upstream model is available."
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, message, "server_error")
}

type attemptResult int

const (
	// attemptDone means the client already has a response, or the client left.
	attemptDone attemptResult = iota
	// attemptFailed means this model did not answer before headers.
	attemptFailed
	// attemptSkipped means the breaker was open, so this model was not called.
	attemptSkipped
)

// attempt sends one candidate. Headers are the commit point: a 5xx or a dial
// error can still try the next model, and anything else is streamed as-is.
func (h *Handler) attempt(w http.ResponseWriter, r *http.Request, tenant string, reserved int, model string, body []byte, received time.Time, reqID string, rewriteModel bool) attemptResult {
	defer h.recordBreaker(model)
	report, err := h.allow(model)
	if err != nil {
		// Ask before dialing. An open breaker fails in this process instead of
		// making the client wait out an upstream timeout.
		log.Printf("request %s model %s skipped: circuit breaker open", reqID, model)
		return attemptSkipped
	}
	payload := body
	if rewriteModel {
		var err error
		payload, err = bodyForModel(body, model)
		if err != nil {
			report(breaker.Ignored)
			h.refundTokens(tenant, reserved)
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
			return attemptDone
		}
	}
	if h.injectUsage && len(payload) > 0 {
		var err error
		payload, err = injectIncludeUsage(payload)
		if err != nil {
			report(breaker.Ignored)
			h.refundTokens(tenant, reserved)
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
			return attemptDone
		}
	}
	target, err := h.upstreamRequestURL(r)
	if err != nil {
		report(breaker.Ignored)
		h.refundTokens(tenant, reserved)
		writeOpenAIError(w, http.StatusBadGateway, "The upstream request failed.", "server_error")
		return attemptDone
	}
	// The incoming context is cancelled when the client disconnects, which
	// cancels this upstream call so a closed tab stops the paid request.
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(payload))
	if err != nil {
		report(breaker.Ignored)
		h.refundTokens(tenant, reserved)
		writeOpenAIError(w, http.StatusBadGateway, "The upstream request failed.", "server_error")
		return attemptDone
	}
	h.copyRequestHeaders(upReq.Header, r.Header, reqID)
	upReq.ContentLength = int64(len(payload))

	// Stamp the send before Do returns. Do blocks on upstream headers, and
	// that wait is the model's time, not the proxy's.
	sent := h.now()
	resp, err := h.client.Do(upReq)
	if err != nil {
		if r.Context().Err() != nil {
			// The client hung up. That is not an upstream outage.
			report(breaker.Ignored)
			return attemptDone
		}
		log.Printf("request %s upstream model %s: %v", reqID, model, err)
		// A dial error or a timeout before headers means this model is down.
		h.metrics.UpstreamStatus("error")
		report(breaker.Failed)
		return attemptFailed
	}
	h.metrics.UpstreamStatus(strconv.Itoa(resp.StatusCode))
	outcome := upstreamOutcome(resp.StatusCode)
	if outcome == breaker.Failed {
		// Headers never reached the client, so another candidate can still serve.
		log.Printf("request %s upstream model %s: status %d", reqID, model, resp.StatusCode)
		report(breaker.Failed)
		discardBody(resp.Body)
		return attemptFailed
	}
	// 429 is Backoff, not a failure: the body and Retry-After go to the client,
	// and another model is not tried.
	if outcome != breaker.Succeeded {
		report(outcome)
		h.forward(w, r, resp, model, tenant, reserved, received, sent, reqID)
		return attemptDone
	}
	// A stream that dies halfway is an upstream failure even though its
	// headers were fine, so the sample is taken after the body ends.
	if h.forward(w, r, resp, model, tenant, reserved, received, sent, reqID) {
		report(breaker.Failed)
	} else {
		report(breaker.Succeeded)
	}
	return attemptDone
}

func (h *Handler) allow(model string) (func(breaker.Outcome), error) {
	if h.breakers != nil {
		return h.breakers.Allow(model, h.now())
	}
	if h.breaker != nil {
		return h.breaker.Allow(h.now())
	}
	return func(breaker.Outcome) {}, nil
}

// upstreamOutcome classifies a response that already has headers. 400, 401,
// and 404 are the caller's problem. 429 asks the caller to wait. Only
// 500/502/503/504 say the upstream itself failed.
func upstreamOutcome(status int) breaker.Outcome {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return breaker.Failed
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound:
		return breaker.Ignored
	case http.StatusTooManyRequests:
		return breaker.Backoff
	default:
		return breaker.Succeeded
	}
}

func (h *Handler) candidates(model string) []string {
	if h.router == nil {
		return []string{model}
	}
	return h.router.Candidates(model)
}

// forward copies the response to the client. It returns true when the upstream
// body failed before it ended; a client that left does not count.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, resp *http.Response, model, tenant string, reserved int, received, sent time.Time, reqID string) (upstreamFailed bool) {
	defer resp.Body.Close()

	streaming := isEventStream(resp.Header.Get("Content-Type"))
	// Non-streaming responses copy status, ordinary headers, and the body.
	// Hop-by-hop headers describe this connection and are not forwarded.
	copySafeResponseHeaders(w.Header(), resp.Header)
	if streaming {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		h.metrics.AddStreams(1)
		defer h.metrics.AddStreams(-1)
	}
	// Writing headers commits this model. The body is streamed as it arrives
	// and is not replaced if the upstream fails halfway through.
	// The model list has no model name to report.
	if model != modelsBreaker {
		w.Header().Set(servedModelHeader, model)
		h.metrics.Served(model)
	}
	if reqID != "" {
		w.Header().Set("X-FireRouter-Request-ID", reqID)
	}
	w.WriteHeader(resp.StatusCode)

	var mark byteTimes
	tail, err := streamBody(w, resp.Body, &mark, h.now)
	if mark.ok {
		h.metrics.ObserveTTFT(mark.forwarded.Sub(received))
		h.metrics.ObserveOverhead(observability.Overhead(received, sent, mark.upstream, mark.forwarded))
	}
	if err != nil && r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
		log.Printf("streaming response: %v", err)
	}
	if err != nil && r.Context().Err() == nil {
		var re *upstreamReadError
		if errors.As(err, &re) {
			upstreamFailed = true
			h.metrics.UpstreamStatus("stream_error")
			if streaming {
				// Headers are gone, so no other model can serve. An error event
				// lets the SDK raise instead of treating a cut stream as complete.
				writeStreamError(w)
			}
		}
	}
	// With usage injection the upstream sends a real count, so the estimate can
	// be corrected. Without it the reserved estimate stands, even if a usage
	// object happens to be in the body.
	if h.injectUsage {
		if actual, ok := actualTokens(tail); ok {
			h.refundTokens(tenant, reserved-actual)
		}
	}
	return upstreamFailed
}

// upstreamReadError marks a failure reading the upstream body, as opposed to
// writing to a client that has gone away.
type upstreamReadError struct{ err error }

func (e *upstreamReadError) Error() string { return "upstream body: " + e.err.Error() }
func (e *upstreamReadError) Unwrap() error { return e.err }

const streamErrorEvent = "data: {\"error\":{\"message\":\"The upstream stream ended unexpectedly.\",\"type\":\"server_error\",\"code\":\"upstream_stream_error\"}}\n\n"

func writeStreamError(w http.ResponseWriter) {
	if _, err := io.WriteString(w, streamErrorEvent); err != nil {
		return
	}
	_ = http.NewResponseController(w).Flush()
}

func discardBody(rc io.ReadCloser) {
	defer rc.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, maxRequestBody))
}

func readRequestBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	defer r.Body.Close()
	// Cap applies to the inbound request only. The upstream response is streamed.
	return io.ReadAll(http.MaxBytesReader(w, r.Body, max))
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
// Authorization is only the Fireworks key from the environment. The caller's
// virtual key is never copied, even when that environment variable is empty.
// The key itself is never logged.
func (h *Handler) copyRequestHeaders(dst, src http.Header, requestID string) {
	for _, name := range []string{"Content-Type", "Accept"} {
		if v := src.Get(name); v != "" {
			dst.Set(name, v)
		}
	}
	if h.apiKey != "" {
		dst.Set("Authorization", "Bearer "+h.apiKey)
	}
	if requestID != "" {
		dst.Set("X-Request-ID", requestID)
	}
}

// requestID keeps a caller-supplied X-Request-ID. Otherwise it makes one.
// Newlines are removed so the value cannot forge extra log lines.
func requestID(r *http.Request) string {
	id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	id = strings.Map(func(c rune) rune {
		if c == '\n' || c == '\r' {
			return -1
		}
		return c
	}, id)
	if id != "" {
		return id
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 16)))
	}
	return hex.EncodeToString(buf[:])
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

func routeOf(r *http.Request) (routeKind, bool) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		return routeChat, true
	case r.Method == http.MethodPost && r.URL.Path == "/v1/completions":
		return routeCompletions, true
	case r.Method == http.MethodPost && r.URL.Path == "/v1/embeddings":
		return routeEmbeddings, true
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		return routeModels, true
	default:
		return 0, false
	}
}

func allowHeader(path string) string {
	if path == "/v1/models" {
		return http.MethodGet
	}
	return http.MethodPost
}

func estimateTokens(kind routeKind, body []byte) int {
	if kind == routeEmbeddings {
		return ratelimit.EstimateEmbeddingTokens(body)
	}
	if kind == routeModels {
		return 0
	}
	return ratelimit.EstimateTokens(body)
}

// hopByHop headers belong to a single connection. Content-Length is left
// off because the proxy writes the body itself.
var hopByHop = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"content-length":      {},
}

func copySafeResponseHeaders(dst, src http.Header) {
	named := map[string]struct{}{}
	for _, item := range src.Values("Connection") {
		for _, part := range strings.Split(item, ",") {
			named[strings.ToLower(strings.TrimSpace(part))] = struct{}{}
		}
	}
	for name, values := range src {
		low := strings.ToLower(name)
		if _, skip := hopByHop[low]; skip {
			continue
		}
		if _, skip := named[low]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// byteTimes holds the first response byte, taken inside the flush loop.
type byteTimes struct {
	upstream  time.Time
	forwarded time.Time
	ok        bool
}

// streamBody copies upstream bytes to the client one read at a time. Flush
// pushes those bytes onto the network immediately; without it the server
// waits to fill a buffer and the user sees the whole answer at once.
// The returned tail is only the last 4 KB, which is where the usage object sits.
// mark records the first byte only. The flush itself is unchanged.
func streamBody(dst http.ResponseWriter, src io.Reader, mark *byteTimes, now func() time.Time) ([]byte, error) {
	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	buf := *bufp
	var tail tailBuf
	if now == nil {
		now = time.Now
	}

	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			var upstreamFirst time.Time
			if mark != nil && !mark.ok {
				upstreamFirst = now()
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return tail.bytes(), werr
			}
			if ferr := http.NewResponseController(dst).Flush(); ferr != nil {
				return tail.bytes(), ferr
			}
			if mark != nil && !mark.ok {
				mark.upstream = upstreamFirst
				mark.forwarded = now()
				mark.ok = true
			}
			tail.write(buf[:n])
		}
		if rerr == io.EOF {
			return tail.bytes(), nil
		}
		if rerr != nil {
			return tail.bytes(), &upstreamReadError{err: rerr}
		}
	}
}

func (h *Handler) now() time.Time {
	if h == nil || h.clock == nil {
		return time.Now()
	}
	return h.clock()
}

func (h *Handler) recordBreaker(model string) {
	if h.metrics == nil {
		return
	}
	var b *breaker.Breaker
	switch {
	case h.breakers != nil:
		b = h.breakers.Get(model)
	case h.breaker != nil:
		b = h.breaker
	default:
		return
	}
	// State values match the breaker enum: 0 closed, 1 open, 2 half-open.
	h.metrics.SetBreakerState(model, float64(b.State()))
}

func (h *Handler) refundTokens(tenant string, tokens int) {
	if h.limiter == nil || tokens <= 0 {
		return
	}
	h.limiter.Refund(tenant, tokens)
}

// caller is the tenant for this request. configured is false in open mode,
// where rpm and tpm come from the process flags instead of the key file.
type caller struct {
	name       string
	rpm        int
	tpm        int
	configured bool
	allows     func(string) bool
}

func allowAny(string) bool { return true }

// metricName is the tenant label. In open mode the name is a hash of the
// caller's header, which is unbounded, so those callers share one label.
func (c caller) metricName() string {
	if !c.configured {
		return "anonymous"
	}
	return c.name
}

// EvictIdle drops rate-limit state for tenants that have gone quiet.
func (h *Handler) EvictIdle() {
	if h.limiter != nil {
		h.limiter.EvictIdle(h.now())
	}
}

// authenticate names the tenant. With a key file, only a listed Bearer key
// passes, and the name is the one in the file. Without a key file, every
// caller passes and the name is a hash of the header so two callers do not
// share a budget and the raw header is not kept.
func (h *Handler) authenticate(authorization string) (caller, bool) {
	if keys := h.keys.Load(); keys != nil {
		found, ok := keys.Lookup(authorization)
		if !ok {
			return caller{}, false
		}
		return caller{
			name:       found.Name,
			rpm:        found.RPM,
			tpm:        found.TPM,
			configured: true,
			allows:     found.Allows,
		}, true
	}
	sum := sha256.Sum256([]byte(authorization))
	return caller{name: hex.EncodeToString(sum[:]), allows: allowAny}, true
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
	writeCodedError(w, status, message, typ, "")
}

func writeCodedError(w http.ResponseWriter, status int, message, typ, code string) {
	var codePtr *string
	if code != "" {
		codePtr = &code
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{
		Error: apiErrorBody{Message: message, Type: typ, Code: codePtr},
	})
}
