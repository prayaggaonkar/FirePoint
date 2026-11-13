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

	"github.com/prayaggaonkar/firerouter/internal/proxy"
)

func main() {
	listen := flag.String("listen", envOr("FIREROUTER_LISTEN", ":8080"), "listen address")
	upstream := flag.String("upstream", envOr("FIREROUTER_UPSTREAM", "http://localhost:9000"), "upstream base URL")
	rpm := flag.Int("rpm", 600, "requests per minute for one client")
	tpm := flag.Int("tpm", 200000, "tokens per minute for one client")
	flag.Parse()

	// Trim so a trailing newline in the env value does not become part of the key.
	// The value is never written to the log.
	apiKey := strings.TrimSpace(os.Getenv("FIREWORKS_API_KEY"))
	handler, err := proxy.NewHandler(*upstream, proxy.NewClient(), apiKey)
	if err != nil {
		log.Fatal(err)
	}
	handler.SetLimiter(*rpm, *tpm)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("POST /v1/chat/completions", handler)

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
		if apiKey != "" {
			log.Printf("listening on %s, upstream %s, rpm %d, tpm %d, using FIREWORKS_API_KEY", *listen, *upstream, *rpm, *tpm)
		} else {
			log.Printf("listening on %s, upstream %s, rpm %d, tpm %d", *listen, *upstream, *rpm, *tpm)
		}
		errCh <- srv.ListenAndServe()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-sig:
		// Drain in-flight streams, then stop. 30s is the maximum wait.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
