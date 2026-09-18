package Util

import (
	"path/filepath"
	"strings"

	c "github.com/dimagog/bufa/internal/contract"
)

// Does not clean or absolutize.
func NormalizePath(path string) string {
	return strings.ToLower(filepath.ToSlash(path))
}

// The "." exclusion is load-bearing: filepath.IsLocal(".") is true.
func RelStrictlyBelow(rel string) bool {
	return rel != "." && filepath.IsLocal(rel)
}

// Both paths must be relative to the same root (filepath.Rel panics otherwise, via contract).
func AtOrBelow(base, path string) bool {
	return filepath.IsLocal(c.Check2(filepath.Rel(base, path)))
}

func StrictlyBelow(base, path string) bool {
	return RelStrictlyBelow(c.Check2(filepath.Rel(base, path)))
}

// A Rel error (different volumes) reports false instead of panicking.
func AtOrBelowIgnoreUnrelated(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && filepath.IsLocal(rel)
}

func PathsOverlap(a, b string) bool {
	return AtOrBelow(a, b) || AtOrBelow(b, a)
}
