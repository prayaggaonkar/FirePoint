// Package router turns a requested model into the ordered list of models to try.
package router

import (
	"encoding/json"
	"fmt"
	"os"
)

// File is the JSON config. Keys are requested model names. fallbacks are tried
// after that model, in order, and only before any response bytes are sent.
//
//	{"models":{"big-broken":{"fallbacks":["small"]}}}
type File struct {
	Models map[string]Model `json:"models"`
}

// Model is one requested name and the backups to try after it.
type Model struct {
	Fallbacks []string `json:"fallbacks"`
}

// Router is the loaded config. It is not mutated after New or Load.
type Router struct {
	fallbacks map[string][]string
}

// Load reads a JSON config from path.
func Load(path string) (*Router, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return New(file), nil
}

// New copies fallbacks out of file so the caller can reuse the struct.
func New(file File) *Router {
	fallbacks := make(map[string][]string, len(file.Models))
	for name, model := range file.Models {
		fallbacks[name] = append([]string(nil), model.Fallbacks...)
	}
	return &Router{fallbacks: fallbacks}
}

// Candidates returns model first, then its configured fallbacks.
// A name with no entry is tried alone. Repeated names are listed once.
// Links are not followed: the list is exactly the order in the config for
// this request, not a walk through other models' backups.
func (r *Router) Candidates(model string) []string {
	out := []string{model}
	if r == nil {
		return out
	}
	seen := map[string]struct{}{model: {}}
	for _, name := range r.fallbacks[model] {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
