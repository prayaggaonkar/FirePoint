package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/auth"
	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/observability"
	"github.com/prayaggaonkar/firerouter/internal/proxy"
	"github.com/prayaggaonkar/firerouter/internal/ratelimit"
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

	opts := defineFlags(flag.CommandLine)
	flag.Parse()
	if opts.showVersion {
		// The value is stamped at build time with -X main.version.
		fmt.Println(version)
		return
	}

	// Trim so a trailing newline in the env value does not become part of the key.
	// The value is never written to the log.
	apiKey := strings.TrimSpace(os.Getenv("FIREWORKS_API_KEY"))
	handler, err := proxy.NewHandler(opts.upstream, proxy.NewClient(), apiKey)
	if err != nil {
		log.Fatal(err)
	}
	if opts.configPath != "" {
		rt, err := router.Load(opts.configPath)
		if err != nil {
			log.Fatal(err)
		}
		handler.SetRouter(rt)
	}
	if opts.keysPath != "" {
		keys, err := auth.Load(opts.keysPath)
		if err != nil {
			log.Fatal(err)
		}
		handler.SetKeys(keys)
	}
	handler.SetLimiter(opts.rpm, opts.tpm)
	handler.SetInjectUsage(opts.injectUsage)
	if opts.maxBody > 0 {
		handler.SetMaxBody(opts.maxBody)
	}
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
	if opts.status {
		mux.Handle("GET /", proxy.StatusHandler(metrics, proxy.StatusInfo{
			IPPerMinute: opts.ipRPM,
			MaxBody:     handler.MaxBody(),
		}))
	}

	var site http.Handler = mux
	if opts.ipRPM > 0 {
		// Health checks stay outside the budget. The address is Fly-Client-IP
		// when present, otherwise the TCP peer. X-Forwarded-For is not used.
		site = proxy.LimitByIP(ratelimit.NewIPLimiter(opts.ipRPM), nil, metrics.IPLimited, site)
	}

	srv := &http.Server{
		Addr:              opts.listen,
		Handler:           site,
		ReadHeaderTimeout: 5 * time.Second,
		// A streamed completion can run as long as the model does. A write
		// deadline would cut the response off while tokens are still arriving.
		WriteTimeout: 0,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("firerouter %s listening on %s, upstream %s, rpm %d, tpm %d, max-body %d%s%s%s%s%s%s",
			version, opts.listen, opts.upstream, opts.rpm, opts.tpm, handler.MaxBody(), configNote(opts.configPath), keysNote(opts.keysPath), keyNote(apiKey), usageNote(opts.injectUsage), ipNote(opts.ipRPM), statusNote(opts.status))
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
				reloadKeys(handler, opts.keysPath)
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

type options struct {
	listen      string
	upstream    string
	configPath  string
	keysPath    string
	rpm         int
	tpm         int
	ipRPM       int
	maxBody     int64
	injectUsage bool
	status      bool
	showVersion bool
}

func defineFlags(fs *flag.FlagSet) *options {
	opts := &options{}
	fs.StringVar(&opts.listen, "listen", envOr("FIREROUTER_LISTEN", ":8080"), "listen address")
	fs.StringVar(&opts.upstream, "upstream", envOr("FIREROUTER_UPSTREAM", "http://localhost:9000"), "upstream base URL")
	fs.StringVar(&opts.configPath, "config", "", "JSON file of model fallbacks")
	fs.StringVar(&opts.keysPath, "keys", "", "JSON file of tenant key hashes; empty allows every caller")
	fs.IntVar(&opts.rpm, "rpm", 600, "requests per minute for one client")
	fs.IntVar(&opts.tpm, "tpm", 200000, "tokens per minute for one client")
	fs.IntVar(&opts.ipRPM, "ip-rpm", 0, "requests per minute for one client address; 0 disables the per-address limit")
	fs.Int64Var(&opts.maxBody, "max-body", 1<<20, "maximum inbound request body in bytes")
	fs.BoolVar(&opts.injectUsage, "inject-usage", false, "on streamed chats, ask the upstream for a usage chunk so token refunds use the real count")
	fs.BoolVar(&opts.status, "status", false, "serve a read-only metrics page at GET /")
	fs.BoolVar(&opts.showVersion, "version", false, "print the build version and exit")
	return opts
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

func ipNote(rpm int) string {
	if rpm <= 0 {
		return ""
	}
	return ", ip-rpm " + strconv.Itoa(rpm)
}

func statusNote(on bool) string {
	if !on {
		return ""
	}
	return ", status page"
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
