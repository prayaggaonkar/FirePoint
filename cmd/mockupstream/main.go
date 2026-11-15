package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// version is replaced by the image build: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	firstTokenDelay := flag.Duration("first-token-delay", 200*time.Millisecond, "wait before the first streamed token")
	tokenInterval := flag.Duration("token-interval", 20*time.Millisecond, "wait between streamed tokens")
	tokens := flag.Int("tokens", 50, "number of completion tokens to emit")
	failRate := flag.Float64("fail-rate", 0.0, "fraction of requests that return HTTP 503")
	adminToken := flag.String("admin-token", "", "bearer token required by /admin; empty disables the admin API")
	flag.Parse()

	h := newHandler(config{
		firstTokenDelay: *firstTokenDelay,
		tokenInterval:   *tokenInterval,
		tokens:          *tokens,
		failRate:        *failRate,
	})
	h.setAdminToken(*adminToken)
	log.Printf("mockupstream %s listening on %s", version, *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}

type config struct {
	firstTokenDelay time.Duration
	tokenInterval   time.Duration
	tokens          int
	failRate        float64
}

// totalDelay matches the streaming schedule: one first-token wait, then an
// interval before every token after the first. The non-streaming response
// waits this long so both modes take the same amount of time.
func (c config) totalDelay() time.Duration {
	if c.tokens < 1 {
		return 0
	}
	d := c.firstTokenDelay
	if c.tokens > 1 {
		d += time.Duration(c.tokens-1) * c.tokenInterval
	}
	return d
}

type handler struct {
	cfg config
	now func() time.Time
	// adminSum is the sha256 of the admin token. hasAdmin is false when the
	// flag is empty, so the admin API stays closed.
	adminSum [sha256.Size]byte
	hasAdmin bool

	// mu guards rng and rules. math/rand is not safe for concurrent use,
	// and a failure roll must see a stable rule list.
	mu    sync.Mutex
	rng   *rand.Rand
	rules []failRule
}

// failRule is one live failure. Models whose name contains ModelContains
// fail with probability Rate. The first matching rule wins.
type failRule struct {
	ModelContains string  `json:"model_contains"`
	Rate          float64 `json:"rate"`
}

func newHandler(cfg config) *handler {
	if cfg.tokens < 0 {
		cfg.tokens = 0
	}
	return &handler{
		cfg: cfg,
		now: time.Now,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		h.completions(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/completions":
		h.textCompletions(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/embeddings":
		h.embeddings(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		h.models(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin"):
		h.admin(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) setAdminToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		h.hasAdmin = false
		return
	}
	h.adminSum = sha256.Sum256([]byte(token))
	h.hasAdmin = true
}

func (h *handler) admin(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "Invalid admin token.", "invalid_request_error")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/admin/fail":
		h.adminFail(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/reset":
		h.adminReset(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/state":
		h.adminState(w, r)
	default:
		w.Header().Set("Allow", adminAllow(r.URL.Path))
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
	}
}

func adminAllow(path string) string {
	switch path {
	case "/admin/fail", "/admin/reset":
		return http.MethodPost
	case "/admin/state":
		return http.MethodGet
	default:
		return http.MethodGet + ", " + http.MethodPost
	}
}

func (h *handler) adminAuthorized(r *http.Request) bool {
	if !h.hasAdmin {
		return false
	}
	sum := sha256.Sum256([]byte(bearerToken(r.Header.Get("Authorization"))))
	// Compare the hashes so a shorter guess is not rejected faster than a
	// longer one, and the compare itself does not stop at the first byte.
	return subtle.ConstantTimeCompare(sum[:], h.adminSum[:]) == 1
}

func bearerToken(authorization string) string {
	const prefix = "bearer "
	if len(authorization) < len(prefix) || !strings.EqualFold(authorization[:len(prefix)], prefix) {
		return ""
	}
	token := strings.TrimSpace(authorization[len(prefix):])
	if token == "" || strings.ContainsAny(token, " \t") {
		return ""
	}
	return token
}

func (h *handler) adminFail(w http.ResponseWriter, r *http.Request) {
	var rule failRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
		return
	}
	rule.ModelContains = strings.TrimSpace(rule.ModelContains)
	if rule.ModelContains == "" || math.IsNaN(rule.Rate) || rule.Rate < 0 || rule.Rate > 1 {
		writeError(w, http.StatusBadRequest, "model_contains is required and rate must be from 0 to 1", "invalid_request_error")
		return
	}
	h.mu.Lock()
	replaced := false
	for i := range h.rules {
		if h.rules[i].ModelContains == rule.ModelContains {
			h.rules[i].Rate = rule.Rate
			replaced = true
			break
		}
	}
	if !replaced {
		h.rules = append(h.rules, rule)
	}
	state := h.stateLocked()
	h.mu.Unlock()
	writeJSON(w, state)
}

func (h *handler) adminReset(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.rules = nil
	h.mu.Unlock()
	writeJSON(w, adminState{Rules: []failRule{}})
}

func (h *handler) adminState(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	state := h.stateLocked()
	h.mu.Unlock()
	writeJSON(w, state)
}

type adminState struct {
	Rules []failRule `json:"rules"`
}

// stateLocked copies the rules so the response does not share the live slice.
// The caller holds h.mu.
func (h *handler) stateLocked() adminState {
	rules := make([]failRule, len(h.rules))
	copy(rules, h.rules)
	return adminState{Rules: rules}
}

type chatRequest struct {
	Stream bool   `json:"stream"`
	Model  string `json:"model"`
}

func (h *handler) completions(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	// Decode only the two fields this mock needs. Extra JSON is ignored.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
		return
	}
	// A model whose name contains "broken" is the down primary in a fallback test.
	if strings.Contains(req.Model, "broken") {
		writeError(w, http.StatusServiceUnavailable, "The server is overloaded or not ready yet.", "server_error")
		return
	}
	if h.shouldFail(req.Model) {
		writeError(w, http.StatusServiceUnavailable, "The server is overloaded or not ready yet.", "server_error")
		return
	}
	if req.Model == "" {
		req.Model = "mock"
	}
	if req.Stream {
		h.stream(w, r, req.Model)
		return
	}
	h.complete(w, r, req.Model)
}

// shouldFail reports whether this model should return 503. A matching admin
// rule replaces the process-wide -fail-rate. No match keeps that rate.
func (h *handler) shouldFail(model string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	rate := h.cfg.failRate
	for _, rule := range h.rules {
		if strings.Contains(model, rule.ModelContains) {
			rate = rule.Rate
			break
		}
	}
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return h.rng.Float64() < rate
}

func (h *handler) stream(w http.ResponseWriter, r *http.Request, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported", "server_error")
		return
	}
	// Send headers before the first token so the client can tell the
	// connection is open while that delay is still running.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	created := h.now().Unix()
	const id = "chatcmpl-mock"
	for i := 0; i < h.cfg.tokens; i++ {
		delay := h.cfg.tokenInterval
		if i == 0 {
			delay = h.cfg.firstTokenDelay
		}
		if !h.wait(r.Context(), delay) {
			return
		}
		chunk := tokenChunk(id, created, model, tokenText(i))
		if !h.writeSSE(w, flusher, r.Context(), chunk) {
			return
		}
	}
	if !h.writeSSE(w, flusher, r.Context(), usageChunk(id, created, model, h.cfg.tokens)) {
		return
	}
	h.writeSSERaw(w, flusher, r.Context(), "data: [DONE]\n\n")
}

func (h *handler) complete(w http.ResponseWriter, r *http.Request, model string) {
	if !h.wait(r.Context(), h.cfg.totalDelay()) {
		return
	}
	var content strings.Builder
	for i := 0; i < h.cfg.tokens; i++ {
		content.WriteString(tokenText(i))
	}
	body := completion{
		ID:      "chatcmpl-mock",
		Object:  "chat.completion",
		Created: h.now().Unix(),
		Model:   model,
		Choices: []completionChoice{{
			Index:        0,
			Message:      message{Role: "assistant", Content: content.String()},
			FinishReason: "stop",
		}},
		Usage: usageObject(h.cfg.tokens),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "mock-req")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil && r.Context().Err() != nil {
		log.Print("client disconnected")
	}
}

func (h *handler) textCompletions(w http.ResponseWriter, r *http.Request) {
	model, ok := h.ready(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{
		"id":      "cmpl-mock",
		"object":  "text_completion",
		"created": h.now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"text":          "ok",
			"index":         0,
			"finish_reason": "stop",
		}},
	})
}

func (h *handler) embeddings(w http.ResponseWriter, r *http.Request) {
	model, ok := h.ready(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"model":  model,
		"data": []map[string]any{{
			"object":    "embedding",
			"index":     0,
			"embedding": []float64{0.1, 0.2, 0.3},
		}},
	})
}

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	// The model list has no model name, so only the process-wide rate applies.
	if h.shouldFail("") {
		writeError(w, http.StatusServiceUnavailable, "The server is overloaded or not ready yet.", "server_error")
		return
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       "mock",
			"object":   "model",
			"owned_by": "firerouter",
		}},
	})
}

// ready reads the model and applies the same failure rules as chat.
// Unknown JSON fields are ignored here. The proxy is what must keep them.
func (h *handler) ready(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error")
		return "", false
	}
	if strings.Contains(req.Model, "broken") || h.shouldFail(req.Model) {
		writeError(w, http.StatusServiceUnavailable, "The server is overloaded or not ready yet.", "server_error")
		return "", false
	}
	if req.Model == "" {
		req.Model = "mock"
	}
	return req.Model, true
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "mock-req")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// wait returns false after the client disconnects. A timer is used instead of
// time.Sleep so the wait stops when the request context is cancelled.
func (h *handler) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		if ctx.Err() != nil {
			log.Print("client disconnected")
			return false
		}
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		log.Print("client disconnected")
		return false
	case <-timer.C:
		return true
	}
}

func (h *handler) writeSSE(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, v any) bool {
	payload, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return h.writeSSERaw(w, flusher, ctx, "data: "+string(payload)+"\n\n")
}

func (h *handler) writeSSERaw(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, frame string) bool {
	if ctx.Err() != nil {
		log.Print("client disconnected")
		return false
	}
	if _, err := io.WriteString(w, frame); err != nil {
		if ctx.Err() != nil {
			log.Print("client disconnected")
		}
		return false
	}
	// Flush every event. Leaving bytes in the buffer would hold tokens until
	// the stream finished, which is the opposite of a streaming mock.
	flusher.Flush()
	return true
}

func tokenText(i int) string {
	return fmt.Sprintf("t%d", i)
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func usageObject(n int) usage {
	return usage{
		PromptTokens:     10,
		CompletionTokens: n,
		TotalTokens:      10 + n,
	}
}

type delta struct {
	Content string `json:"content"`
}

type streamChoice struct {
	Index        int     `json:"index"`
	Delta        *delta  `json:"delta,omitempty"`
	FinishReason *string `json:"finish_reason"`
}

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usage         `json:"usage,omitempty"`
}

func tokenChunk(id string, created int64, model, content string) streamChunk {
	return streamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []streamChoice{{
			Index: 0,
			Delta: &delta{Content: content},
		}},
	}
}

func usageChunk(id string, created int64, model string, n int) streamChunk {
	u := usageObject(n)
	return streamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []streamChoice{},
		Usage:   &u,
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionChoice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   usage              `json:"usage"`
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

func writeError(w http.ResponseWriter, status int, message, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{
		Error: apiErrorBody{Message: message, Type: typ},
	})
}
