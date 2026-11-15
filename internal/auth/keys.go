// Package auth checks the caller's API key and maps it to a tenant name.
// The key is the secret the app sends. The tenant name is what rate limits use.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// File is the JSON list of tenants. Each key belongs to one tenant.
//
//	{"tenants":[{"id":"acme","key":"replace-me"}]}
type File struct {
	Tenants []Tenant `json:"tenants"`
}

// Tenant is one customer. id is a name, not a secret. key is the secret the
// app sends in Authorization: Bearer <key>.
type Tenant struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// Store matches a presented key to a tenant. It keeps only hashes of the keys.
type Store struct {
	byHash map[string]string
}

// Load reads a tenant key file. The file holds secrets and should not be committed.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("keys %s: %w", path, err)
	}
	return New(file)
}

// New hashes each key. Two tenants cannot share a key. A tenant can have
// more than one key so an old key and a new key work during a changeover.
func New(file File) (*Store, error) {
	if len(file.Tenants) == 0 {
		return nil, errors.New("keys file has no tenants")
	}
	byHash := make(map[string]string, len(file.Tenants))
	for _, tenant := range file.Tenants {
		id := strings.TrimSpace(tenant.ID)
		if id == "" || strings.TrimSpace(tenant.Key) == "" {
			return nil, errors.New("tenant id and key must both be set")
		}
		sum := sha256.Sum256([]byte(tenant.Key))
		hash := hex.EncodeToString(sum[:])
		if other, ok := byHash[hash]; ok {
			return nil, fmt.Errorf("duplicate key for tenants %s and %s", other, id)
		}
		byHash[hash] = id
	}
	return &Store{byHash: byHash}, nil
}

// Tenant returns the tenant id for an Authorization header.
// The header must be "Bearer <key>". The key itself is not returned.
func (s *Store) Tenant(authorization string) (string, bool) {
	if s == nil {
		return "", false
	}
	key, ok := bearerKey(authorization)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(key))
	id, ok := s.byHash[hex.EncodeToString(sum[:])]
	return id, ok
}

func bearerKey(authorization string) (string, bool) {
	const prefix = "bearer "
	if len(authorization) < len(prefix) || !strings.EqualFold(authorization[:len(prefix)], prefix) {
		return "", false
	}
	key := strings.TrimSpace(authorization[len(prefix):])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", false
	}
	return key, true
}
