// Package auth checks a virtual API key and maps it to a tenant.
// The file stores a sha256 hash, never the key. The tenant name is what
// rate limits and metrics use. The real upstream key is not this package's job.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	keyPrefix = "fp-"
	keyRandom = 32
	alphabet  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// File is the JSON list of tenants. The process reloads this file on SIGHUP.
//
//	{"tenants":[{"name":"alice","key_sha256":"<hex>","rpm":600,"tpm":200000}]}
type File struct {
	Tenants []Tenant `json:"tenants"`
}

// Tenant is one customer. KeySHA256 is the hex sha256 of the virtual key
// the app sends. AllowedModels empty-or-omitted means every model; an
// explicit empty list means none.
type Tenant struct {
	Name          string   `json:"name"`
	KeySHA256     string   `json:"key_sha256"`
	RPM           int      `json:"rpm"`
	TPM           int      `json:"tpm"`
	AllowedModels []string `json:"allowed_models,omitempty"`
}

// Caller is a tenant that presented a matching key.
type Caller struct {
	Name   string
	RPM    int
	TPM    int
	models map[string]struct{}
}

// Allows reports whether this tenant may call model. A nil allow-list
// means the config did not restrict models.
func (c Caller) Allows(model string) bool {
	if c.models == nil {
		return true
	}
	_, ok := c.models[model]
	return ok
}

// Store matches a presented key to a tenant. Keys are kept only as hashes.
type Store struct {
	byHash map[[sha256.Size]byte]Caller
}

// Load reads a tenant file. A bad file returns an error and does not
// produce a store, so a failed reload can keep the previous one.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("keys %s: %w", path, err)
	}
	store, err := New(file)
	if err != nil {
		return nil, fmt.Errorf("keys %s: %w", path, err)
	}
	return store, nil
}

// New indexes tenants by the raw sha256 of their virtual key.
// Two entries may share a name (a key rotation) only when their limits match,
// because the name is the single rate-limit bucket.
func New(file File) (*Store, error) {
	if len(file.Tenants) == 0 {
		return nil, errors.New("keys file has no tenants")
	}
	byHash := make(map[[sha256.Size]byte]Caller, len(file.Tenants))
	byName := make(map[string]Caller, len(file.Tenants))
	for _, tenant := range file.Tenants {
		name := strings.TrimSpace(tenant.Name)
		if name == "" || strings.ContainsAny(name, "\r\n") {
			return nil, errors.New("tenant name is required")
		}
		if tenant.RPM <= 0 || tenant.TPM <= 0 {
			return nil, fmt.Errorf("tenant %s: rpm and tpm must be positive", name)
		}
		raw, err := hex.DecodeString(tenant.KeySHA256)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("tenant %s: key_sha256 must be a sha256 hex digest", name)
		}
		var sum [sha256.Size]byte
		copy(sum[:], raw)
		caller := Caller{
			Name:   name,
			RPM:    tenant.RPM,
			TPM:    tenant.TPM,
			models: allowSet(tenant.AllowedModels),
		}
		if other, ok := byName[name]; ok && !sameLimits(other, caller) {
			return nil, fmt.Errorf("tenant %s: keys for one name must share rpm, tpm, and allowed_models", name)
		}
		if _, ok := byHash[sum]; ok {
			return nil, fmt.Errorf("duplicate key hash for tenant %s", name)
		}
		byHash[sum] = caller
		byName[name] = caller
	}
	return &Store{byHash: byHash}, nil
}

// Lookup hashes the Bearer token and compares it to every stored hash.
// The comparison does not stop at the first differing byte, and a miss
// still walks the whole map, so the result does not depend on which byte
// differed or on which entry would have matched.
func (s *Store) Lookup(authorization string) (Caller, bool) {
	if s == nil {
		return Caller{}, false
	}
	key, ok := bearerKey(authorization)
	if !ok {
		return Caller{}, false
	}
	sum := sha256.Sum256([]byte(key))
	var chosen Caller
	matched := 0
	for hash, caller := range s.byHash {
		eq := subtle.ConstantTimeCompare(hash[:], sum[:])
		if eq == 1 {
			chosen = caller
			matched = 1
		}
	}
	if matched == 1 {
		return chosen, true
	}
	return Caller{}, false
}

// HashKey is the hex digest stored in the config for key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// NewKey returns a virtual key, fp- plus 32 random letters and digits.
// The caller prints it once. It is not written to the config.
func NewKey() (string, error) {
	buf := make([]byte, keyRandom)
	// 62 symbols do not divide 256. Reject the spare values so the last
	// symbols are not more likely than the first.
	const limit = byte(len(alphabet) * 4) // 248
	var b [1]byte
	for i := 0; i < keyRandom; i++ {
		for {
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			if b[0] < limit {
				buf[i] = alphabet[int(b[0])%len(alphabet)]
				break
			}
		}
	}
	return keyPrefix + string(buf), nil
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

// allowSet returns nil when the config omitted the list, which means every
// model is allowed. An explicit empty list is an empty map: nothing is allowed.
func allowSet(models []string) map[string]struct{} {
	if models == nil {
		return nil
	}
	set := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		set[model] = struct{}{}
	}
	return set
}

func sameLimits(a, b Caller) bool {
	if a.RPM != b.RPM || a.TPM != b.TPM || len(a.models) != len(b.models) {
		return false
	}
	if a.models == nil || b.models == nil {
		return a.models == nil && b.models == nil
	}
	for model := range a.models {
		if _, ok := b.models[model]; !ok {
			return false
		}
	}
	return true
}
