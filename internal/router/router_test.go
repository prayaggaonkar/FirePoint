package router

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCandidatesStayInConfigOrder(t *testing.T) {
	r := New(File{Models: map[string]Model{
		"big-broken": {Fallbacks: []string{"small"}},
		"big":        {Fallbacks: []string{"medium", "small"}},
	}})
	if got, want := r.Candidates("big-broken"), []string{"big-broken", "small"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if got, want := r.Candidates("big"), []string{"big", "medium", "small"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	// A backup that is requested on its own is not given someone else's list.
	if got, want := r.Candidates("small"), []string{"small"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestCandidatesDropEmptyAndRepeatedNames(t *testing.T) {
	r := New(File{Models: map[string]Model{
		"big": {Fallbacks: []string{"", "small", "big", "small"}},
	}})
	if got, want := r.Candidates("big"), []string{"big", "small"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestNilRouterTriesTheRequestedModel(t *testing.T) {
	var r *Router
	if got, want := r.Candidates("big"), []string{"big"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestLoadReadsFallbacks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "firerouter.json")
	body := []byte(`{"models":{"big-broken":{"fallbacks":["small"]}}}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.Candidates("big-broken"), []string{"big-broken", "small"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "firerouter.json")
	if err := os.WriteFile(path, []byte(`{"models"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted invalid JSON")
	}
}
