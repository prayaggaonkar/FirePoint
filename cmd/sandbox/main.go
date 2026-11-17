// Command sandbox runs the mock and firerouter in one process group.
// The mock listens on 127.0.0.1 only. The public listener is firerouter.
// The process exits before either child starts when FIREWORKS_API_KEY is set.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// version is replaced at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(0)
	if fireworksKeySet(os.Environ()) {
		log.Fatal("sandbox: refusing to start because FIREWORKS_API_KEY is set")
	}
	mockPath, err := findBin("mockupstream")
	if err != nil {
		log.Fatal(err)
	}
	routerPath, err := findBin("firerouter")
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mock := exec.CommandContext(ctx, mockPath, mockArgs()...)
	mock.Env = childEnv(os.Environ())
	mock.Stdout = os.Stdout
	mock.Stderr = os.Stderr
	if err := mock.Start(); err != nil {
		log.Fatal(err)
	}
	if err := waitReady(ctx, "http://"+mockAddr+"/healthz"); err != nil {
		cancel()
		_ = mock.Wait()
		log.Fatal(err)
	}

	router := exec.CommandContext(ctx, routerPath, routerArgs()...)
	router.Env = childEnv(os.Environ())
	router.Stdout = os.Stdout
	router.Stderr = os.Stderr
	if err := router.Start(); err != nil {
		cancel()
		_ = mock.Wait()
		log.Fatal(err)
	}
	log.Printf("sandbox %s: mock on %s, firerouter on %s", version, mockAddr, listenAddr)

	errCh := make(chan error, 2)
	go func() { errCh <- mock.Wait() }()
	go func() { errCh <- router.Wait() }()
	select {
	case <-ctx.Done():
		<-errCh
		<-errCh
	case err := <-errCh:
		cancel()
		<-errCh
		if err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	}
}

func findBin(name string) (string, error) {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	candidate := filepath.Join("/usr/local/bin", name)
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("sandbox: %s was not found next to this program or in /usr/local/bin", name)
}

func waitReady(ctx context.Context, rawURL string) error {
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("mock upstream not ready: %w", last)
		case <-tick.C:
		}
	}
}
