package main

import (
	"strings"
	"testing"
)

func TestSandboxRefusesAFireworksKey(t *testing.T) {
	if fireworksKeySet([]string{"FIREWORKS_API_KEY="}) {
		t.Fatal("empty value should not count")
	}
	if !fireworksKeySet([]string{"PATH=/bin", "FIREWORKS_API_KEY=fw-real"}) {
		t.Fatal("expected a set key")
	}
	env := childEnv([]string{"PATH=/bin", "FIREWORKS_API_KEY=fw-real", "HOME=/tmp"})
	for _, entry := range env {
		if strings.HasPrefix(entry, "FIREWORKS_API_KEY=") {
			t.Fatalf("child env still has the key: %v", env)
		}
	}
	if len(env) != 2 {
		t.Fatalf("env = %v", env)
	}
}

func TestSandboxArgsStayOnTheMock(t *testing.T) {
	joined := strings.Join(routerArgs(), " ")
	if !strings.Contains(joined, "http://127.0.0.1:9000") {
		t.Fatalf("upstream = %s", joined)
	}
	if !strings.Contains(joined, "-status") || !strings.Contains(joined, "-ip-rpm 10") || !strings.Contains(joined, "-max-body 8192") {
		t.Fatalf("limits missing: %s", joined)
	}
	if strings.Contains(joined, "fireworks") || strings.Contains(joined, "api.fireworks") {
		t.Fatalf("router args point at Fireworks: %s", joined)
	}
	mock := strings.Join(mockArgs(), " ")
	if strings.Contains(mock, "admin-token") || strings.Contains(mock, "0.0.0.0") {
		t.Fatalf("mock args: %s", mock)
	}
}
