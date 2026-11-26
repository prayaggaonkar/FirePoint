package main

import (
	"strconv"
	"strings"
)

// Limits for the public mock-backed process. They are stricter than the
// defaults used by a private firepoint, and they are the values the status
// page reports because the sandbox passes them as flags.
const (
	mockAddr    = "127.0.0.1:9000"
	listenAddr  = ":8080"
	sandboxRPM  = 30
	sandboxTPM  = 8000
	sandboxIP   = 10
	sandboxBody = 8192
)

// fireworksKeySet reports whether the environment carries a non-empty
// Fireworks key. The sandbox refuses to start in that case so a public
// app cannot be pointed at the real API by a secret.
func fireworksKeySet(environ []string) bool {
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == "FIREWORKS_API_KEY" && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// childEnv is the parent environment with FIREWORKS_API_KEY removed, so a
// child process cannot forward a key even if the parent check is bypassed.
func childEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if ok && name == "FIREWORKS_API_KEY" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func routerArgs() []string {
	return []string{
		"-listen", listenAddr,
		"-upstream", "http://" + mockAddr,
		"-config", "/config/router.json",
		"-rpm", strconv.Itoa(sandboxRPM),
		"-tpm", strconv.Itoa(sandboxTPM),
		"-ip-rpm", strconv.Itoa(sandboxIP),
		"-max-body", strconv.Itoa(sandboxBody),
		"-status",
	}
}

func mockArgs() []string {
	// No -admin-token: the admin routes stay closed.
	return []string{
		"-addr", mockAddr,
		"-first-token-delay", "50ms",
		"-token-interval", "10ms",
		"-tokens", "8",
	}
}
