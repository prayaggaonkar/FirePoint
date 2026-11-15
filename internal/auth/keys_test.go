package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidKey(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "fr-") || len(key) != len("fr-")+32 {
		t.Fatalf("key %q is not fr- plus 32 chars", key)
	}
	for _, c := range key[len("fr-"):] {
		if !strings.ContainsRune(alphabet, c) {
			t.Fatalf("key %q has %q outside the alphabet", key, c)
		}
	}
	store, err := New(File{Tenants: []Tenant{{
		Name:          "alice",
		KeySHA256:     HashKey(key),
		RPM:           600,
		TPM:           200000,
		AllowedModels: []string{"small"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.Lookup("Bearer " + key)
	if !ok || got.Name != "alice" || got.RPM != 600 || got.TPM != 200000 {
		t.Fatalf("lookup = %+v ok=%v", got, ok)
	}
	if !got.Allows("small") {
		t.Fatal("listed model was rejected")
	}
	if got.Allows("big") {
		t.Fatal("unlisted model was allowed")
	}
	got, ok = store.Lookup("bearer " + key)
	if !ok || got.Name != "alice" {
		t.Fatal("bearer scheme is case-sensitive in the scheme word only")
	}
}

func TestBadKey(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(File{Tenants: []Tenant{{
		Name: "alice", KeySHA256: HashKey(key), RPM: 1, TPM: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{
		"Bearer " + other,
		"Bearer fr-" + strings.Repeat("a", 32),
		"Bearer " + key + " extra",
		"Basic " + key,
		"",
	} {
		if _, ok := store.Lookup(header); ok {
			t.Fatalf("accepted %q", header)
		}
	}
	// A digest that differs by one trailing nibble must not match.
	flipped := []byte(HashKey(key))
	if flipped[len(flipped)-1] == '0' {
		flipped[len(flipped)-1] = '1'
	} else {
		flipped[len(flipped)-1] = '0'
	}
	bad, err := New(File{Tenants: []Tenant{{
		Name: "alice", KeySHA256: string(flipped), RPM: 1, TPM: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bad.Lookup("Bearer " + key); ok {
		t.Fatal("a hash that differs by one nibble matched")
	}
}

func TestModelNotAllowedWhenListIsEmpty(t *testing.T) {
	key := "fr-" + strings.Repeat("a", 32)
	store, err := New(File{Tenants: []Tenant{{
		Name: "alice", KeySHA256: HashKey(key), RPM: 1, TPM: 1,
		AllowedModels: []string{},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := store.Lookup("Bearer " + key)
	if !ok {
		t.Fatal("key was rejected")
	}
	if got.Allows("small") {
		t.Fatal("explicit empty allow-list allowed a model")
	}
	open, err := New(File{Tenants: []Tenant{{
		Name: "alice", KeySHA256: HashKey(key), RPM: 1, TPM: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok = open.Lookup("Bearer " + key)
	if !ok || !got.Allows("anything") {
		t.Fatal("omitted allow-list should allow every model")
	}
}

func TestReloadReplacesTheStore(t *testing.T) {
	first, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	write := func(key, name string) {
		t.Helper()
		body, err := json.Marshal(File{Tenants: []Tenant{{
			Name: name, KeySHA256: HashKey(key), RPM: 10, TPM: 20,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(first, "alice")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Lookup("Bearer " + first); !ok {
		t.Fatal("first key was not loaded")
	}
	write(second, "bob")
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// The previous store stays usable. A reload installs the new value
	// beside it; it does not mutate the store an in-flight call already read.
	if _, ok := store.Lookup("Bearer " + first); !ok {
		t.Fatal("reload mutated the store already in use")
	}
	if _, ok := reloaded.Lookup("Bearer " + first); ok {
		t.Fatal("old key still matched after reload")
	}
	got, ok := reloaded.Lookup("Bearer " + second)
	if !ok || got.Name != "bob" || got.RPM != 10 || got.TPM != 20 {
		t.Fatalf("reloaded key = %+v ok=%v", got, ok)
	}
}

func TestRejectsBadConfig(t *testing.T) {
	good := HashKey("fr-" + strings.Repeat("b", 32))
	cases := []File{
		{},
		{Tenants: []Tenant{{Name: "a", KeySHA256: good, RPM: 0, TPM: 1}}},
		{Tenants: []Tenant{{Name: "a", KeySHA256: "abcd", RPM: 1, TPM: 1}}},
		{Tenants: []Tenant{{Name: "", KeySHA256: good, RPM: 1, TPM: 1}}},
		{Tenants: []Tenant{
			{Name: "a", KeySHA256: good, RPM: 1, TPM: 1},
			{Name: "b", KeySHA256: good, RPM: 1, TPM: 1},
		}},
		{Tenants: []Tenant{
			{Name: "a", KeySHA256: good, RPM: 1, TPM: 1},
			{Name: "a", KeySHA256: HashKey("fr-" + strings.Repeat("c", 32)), RPM: 2, TPM: 1},
		}},
	}
	for _, file := range cases {
		if _, err := New(file); err == nil {
			t.Fatalf("accepted %#v", file)
		}
	}
}
