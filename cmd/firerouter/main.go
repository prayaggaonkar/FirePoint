package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/proxy"
)

func main() {
	listen := flag.String("listen", envOr("FIREROUTER_LISTEN", ":8080"), "listen address")
	upstream := flag.String("upstream", envOr("FIREROUTER_UPSTREAM", "http://localhost:9000"), "upstream base URL")
	flag.Parse()

	handler, err := proxy.NewHandler(*upstream, proxy.NewClient())
	if err != nil {
		log.Fatal(err)
	}

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
		log.Printf("listening on %s, upstream %s", *listen, *upstream)
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
