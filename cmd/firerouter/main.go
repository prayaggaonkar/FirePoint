package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/auth"
	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/observability"
	"github.com/prayaggaonkar/firerouter/internal/proxy"
	"github.com/prayaggaonkar/firerouter/internal/router"
)

// version is replaced by the image build: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		if err := keygen(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	listen := flag.String("listen", envOr("FIREROUTER_LISTEN", ":8080"), "listen address")
	upstream := flag.String("upstream", envOr("FIREROUTER_UPSTREAM", "http://localhost:9000"), "upstream base URL")
	configPath := flag.String("config", "", "JSON file of model fallbacks")
	keysPath := flag.String("keys", "", "JSON file of tenant key hashes; empty allows every caller")
	rpm := flag.Int("rpm", 600, "requests per minute for one client")
	tpm := flag.Int("tpm", 200000, "tokens per minute for one client")
	injectUsage := flag.Bool("inject-usage", false, "on streamed chats, ask the upstream for a usage chunk so token refunds use the real count")
	flag.Parse()

	// Trim so a trailing newline in the env value does not become part of the key.
	// The value is never written to the log.
	apiKey := strings.TrimSpace(os.Getenv("FIREWORKS_API_KEY"))
	handler, err := proxy.NewHandler(*upstream, proxy.NewClient(), apiKey)
	if err != nil {
		log.Fatal(err)
	}
	if *configPath != "" {
		rt, err := router.Load(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		handler.SetRouter(rt)
	}
	if *keysPath != "" {
		keys, err := auth.Load(*keysPath)
		if err != nil {
			log.Fatal(err)
		}
		handler.SetKeys(keys)
	}
	handler.SetLimiter(*rpm, *tpm)
	handler.SetInjectUsage(*injectUsage)
	// One breaker per model, so an open primary does not block its fallback.
	handler.SetBreakers(breaker.NewRegistry(breaker.DefaultConfig()))
	metrics := observability.New()
	handler.SetMetrics(metrics)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("POST /v1/chat/completions", handler)
	mux.Handle("POST /v1/completions", handler)
	mux.Handle("POST /v1/embeddings", handler)
	mux.Handle("GET /v1/models", handler)
	mux.Handle("GET /metrics", metrics.Handler())

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// A streamed completion can run as long as the model does. A write
		// deadline would cut the response off while tokens are still arriving.
		WriteTimeout: 0,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("firerouter %s listening on %s, upstream %s, rpm %d, tpm %d%s%s%s%s",
			version, *listen, *upstream, *rpm, *tpm, configNote(*configPath), keysNote(*keysPath), keyNote(apiKey), usageNote(*injectUsage))
		errCh <- srv.ListenAndServe()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
			return
		case s := <-sig:
			if s == syscall.SIGHUP {
				// Reload swaps the key map. Shutdown is not called, so
				// streams that already started keep their connection.
				reloadKeys(handler, *keysPath)
				continue
			}
			// Drain in-flight streams, then stop. 30s is the maximum wait.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				log.Printf("shutdown: %v", err)
			}
			return
		}
	}
}

func reloadKeys(h *proxy.Handler, path string) {
	if path == "" {
		log.Print("SIGHUP: no key file to reload")
		return
	}
	if err := h.ReloadKeys(path); err != nil {
		log.Printf("SIGHUP: key reload failed: %v", err)
		return
	}
	log.Printf("SIGHUP: reloaded tenant keys from %s", path)
}

func configNote(path string) string {
	if path == "" {
		return ""
	}
	return ", fallbacks " + path
}

func usageNote(on bool) string {
	if !on {
		return ""
	}
	return ", inject-usage"
}

func keysNote(path string) string {
	if path == "" {
		return ""
	}
	return ", tenant keys " + path
}

func keyNote(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	return ", using FIREWORKS_API_KEY"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
