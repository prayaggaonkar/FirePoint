package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyMapsToTenant(t *testing.T) {
	store, err := New(File{Tenants: []Tenant{
		{ID: "acme", Key: "acme-secret"},
		{ID: "acme", Key: "acme-next"},
		{ID: "other", Key: "other-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := store.Tenant("Bearer acme-secret")
	if !ok || id != "acme" {
		t.Fatalf("acme-secret -> %q ok=%v", id, ok)
	}
	id, ok = store.Tenant("bearer acme-next")
	if !ok || id != "acme" {
		t.Fatalf("rotated key -> %q ok=%v", id, ok)
	}
	if _, ok := store.Tenant("Bearer other-secret"); !ok {
		t.Fatal("other tenant key was rejected")
	}
	if _, ok := store.Tenant("Bearer no-such-key"); ok {
		t.Fatal("unknown key was accepted")
	}
	if _, ok := store.Tenant("Basic acme-secret"); ok {
		t.Fatal("non-bearer header was accepted")
	}
	if _, ok := store.Tenant(""); ok {
		t.Fatal("empty header was accepted")
	}
}

func TestRejectsDuplicateKey(t *testing.T) {
	_, err := New(File{Tenants: []Tenant{
		{ID: "a", Key: "same"},
		{ID: "b", Key: "same"},
	}})
	if err == nil {
		t.Fatal("duplicate key was accepted")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	body := []byte(`{"tenants":[{"id":"local","key":"replace-me"}]}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := store.Tenant("Bearer replace-me")
	if !ok || id != "local" {
		t.Fatalf("loaded key -> %q ok=%v", id, ok)
	}
}
