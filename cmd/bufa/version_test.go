package main

import (
	"regexp"
	"testing"

	"github.com/dimagog/bufa"
)

// Bufa.Version is embedded raw, so version.txt must be exactly one semver line with no trailing
// newline — this test is the build-time gate.
func TestVersionFormat(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Bufa.Version) {
		t.Fatalf("version.txt must be single-line semver with no trailing newline, got %q", Bufa.Version)
	}
}
