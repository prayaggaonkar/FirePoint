package main

import (
	"flag"
	"io"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	for _, arg := range []string{"--version", "-version"} {
		fs := flag.NewFlagSet("firerouter", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		opts := defineFlags(fs)
		if err := fs.Parse([]string{arg}); err != nil {
			t.Fatal(err)
		}
		if !opts.showVersion {
			t.Fatalf("%s did not request the version", arg)
		}
	}
}
