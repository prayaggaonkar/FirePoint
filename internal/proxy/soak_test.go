package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/observability"
)

// Run with -race. Many tenants send requests while keys reload and idle
// state is evicted; nothing may race, hang, or return a surprising status.
func TestConcurrentTenantsReloadsAndEviction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: x\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	h.SetLimiter(1000000, 100000000)
	h.SetBreakers(breaker.NewRegistry(breaker.DefaultConfig()))
	h.SetMetrics(observability.New())
	keysPath := filepath.Join(t.TempDir(), "keys.json")
	writeKeys(t, keysPath, "t", "fr-soak-key")
	if err := h.ReloadKeys(keysPath); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	stop := make(chan struct{})
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = h.ReloadKeys(keysPath)
				h.EvictIdle()
			}
		}
	}()

	var wg sync.WaitGroup
	statuses := make(chan int, 400)
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				resp := postChat(t, proxy.URL, "Bearer fr-soak-key", `{"model":"m`+string(rune('a'+w))+`","stream":true,"max_tokens":1}`)
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				statuses <- resp.StatusCode
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	bg.Wait()
	close(statuses)
	for s := range statuses {
		if s != http.StatusOK {
			t.Fatalf("status %d during reload", s)
		}
	}
}
