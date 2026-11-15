package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/prayaggaonkar/firerouter/internal/auth"
)

func TestKeygenPrintsKeyOnceAndHashOnly(t *testing.T) {
	out := captureStdout(t, func() error {
		return keygen([]string{"--tenant", "alice", "--rpm", "600", "--tpm", "200000"})
	})
	key, entry, ok := strings.Cut(out, "\n")
	if !ok {
		t.Fatalf("output %q", out)
	}
	if strings.Contains(entry, key) {
		t.Fatal("config entry contains the plaintext key")
	}
	var tenant auth.Tenant
	if err := json.Unmarshal([]byte(entry), &tenant); err != nil {
		t.Fatalf("entry %s: %v", entry, err)
	}
	if tenant.Name != "alice" || tenant.RPM != 600 || tenant.TPM != 200000 {
		t.Fatalf("entry %+v", tenant)
	}
	if tenant.KeySHA256 != auth.HashKey(key) {
		t.Fatalf("hash %s", tenant.KeySHA256)
	}
	store, err := auth.New(auth.File{Tenants: []auth.Tenant{tenant}})
	if err != nil {
		t.Fatal(err)
	}
	got, found := store.Lookup("Bearer " + key)
	if !found || got.Name != "alice" {
		t.Fatalf("lookup %+v ok=%v", got, found)
	}
}

func TestKeygenRequiresTenant(t *testing.T) {
	if err := keygen(nil); err == nil {
		t.Fatal("missing --tenant was accepted")
	}
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = prev })
	runErr := fn()
	w.Close()
	os.Stdout = prev
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(body)
}
