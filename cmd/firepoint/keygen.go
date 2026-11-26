package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/prayaggaonkar/firepoint/internal/auth"
)

// keygen prints a new virtual key once, then the config entry that stores
// only its hash. The plaintext is not written anywhere else.
func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tenant := fs.String("tenant", "", "tenant name")
	rpm := fs.Int("rpm", 600, "requests per minute for this tenant")
	tpm := fs.Int("tpm", 200000, "tokens per minute for this tenant")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(*tenant)
	if name == "" {
		return errors.New("keygen: --tenant is required")
	}
	if *rpm <= 0 || *tpm <= 0 {
		return errors.New("keygen: --rpm and --tpm must be positive")
	}
	key, err := auth.NewKey()
	if err != nil {
		return err
	}
	entry := auth.Tenant{
		Name:      name,
		KeySHA256: auth.HashKey(key),
		RPM:       *rpm,
		TPM:       *tpm,
	}
	fmt.Fprintln(os.Stdout, key)
	fmt.Fprintln(os.Stdout)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(entry)
}
