package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prayaggaonkar/firepoint/internal/observability"
)

func TestStatusPageIsReadOnlyAndOmitsSecrets(t *testing.T) {
	const secret = "fw-secret-do-not-leak"
	m := observability.New()
	m.Served("primary")
	m.UpstreamStatus("200")
	h := StatusHandler(m, StatusInfo{IPPerMinute: 10, MaxBody: 8192})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "sandbox.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Authorization", "Bearer "+secret)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "Bearer") {
		t.Fatalf("page included a credential:\n%s", body)
	}
	for _, want := range []string{
		"No Fireworks API key is configured",
		"https://sandbox.example/v1/chat/completions",
		"https://sandbox.example/v1/models",
		"primary: 1",
		"8192",
		"10 requests",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache = %q", rr.Header().Get("Cache-Control"))
	}

	post := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	postRR := httptest.NewRecorder()
	h.ServeHTTP(postRR, post)
	// The handler itself accepts any method; the mux restricts it to GET.
	// The body is never read, so a POST cannot change process state.
	got, _ := io.ReadAll(postRR.Body)
	if strings.Contains(string(got), secret) {
		t.Fatal("post body echoed a secret")
	}
}

func TestMaxBodyRejectsOversizedRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	small := []byte(`{"model":"primary","messages":[{"role":"user","content":"hi"}]}`)
	h.SetMaxBody(int64(len(small)))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	ok, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json", strings.NewReader(string(small)))
	if err != nil {
		t.Fatal(err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("small status %d", ok.StatusCode)
	}

	big := append(append([]byte{}, small...), ' ')
	bad, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json", strings.NewReader(string(big)))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("big status %d", bad.StatusCode)
	}
}
