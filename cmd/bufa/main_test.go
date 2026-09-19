package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Hashing"
	c "github.com/dimagog/bufa/internal/contract"
)

func TestResolveVirtualDirAgainstOSPaths(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	c.Check(os.MkdirAll(sub, 0o755))
	t.Chdir(root)

	cases := []struct{ dir, want string }{
		{"", "."},
		{"sub", "sub"},
		{"./sub", "sub"},
		{"/sub", "sub"},
		{"/", "."},
	}
	for _, tt := range cases {
		if got := resolveVirtualDirAgainstOSPaths(root, tt.dir); got != tt.want {
			t.Errorf("resolve(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
	if err := c.Rescue(func() { resolveVirtualDirAgainstOSPaths(root, "..") }); err == nil {
		t.Error("resolve(\"..\") must fail: outside the source root")
	}
}

// Regression: `bufa /test` (and `bufa test`) walked from cwd/test, which on a case-folding fs was
// this repo's Test/ fixture root with its own .BUFA marker. An absolute target is never auto-detected
// (on unix "/x" is also root-relative); --start-dir is the only way to name one.
func TestBuild_AbsoluteTargetRejected(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only a volume-carrying path is detectably absolute; unix /x is root-relative by design")
	}
	err := run([]string{t.TempDir()}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--start-dir") {
		t.Errorf("an absolute <dir> must fail naming --start-dir, got: %v", err)
	}
}

// cwd has no marker: --global-only must never reach root resolution.
func TestCheck_GlobalOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	cacheBase := t.TempDir()
	t.Setenv("BUFA_GLOBAL_CACHE_DIR", cacheBase)
	cacheDir := filepath.Join(cacheBase, "bufa")
	prevSafeHashing := Hashing.SafeHashing
	defer func() { Hashing.SafeHashing = prevSafeHashing }()

	var out strings.Builder
	c.Check(run([]string{"check", "--global-only"}, strings.NewReader(""), &out, io.Discard))
	if want := "Check Global Artifact Cache '" + cacheDir + "': 0 artifacts hashed, 0 url links verified\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	// The cache lives in its own 'bufa' subdir, so --fix below can never reach this file.
	bystander := filepath.Join(cacheBase, "notes.txt")
	c.Check(os.WriteFile(bystander, []byte("mine"), 0o644))
	c.Check(os.Mkdir(cacheDir, 0o755))
	pin := Hashing.FilePrefix + Hashing.HashBytes([]byte("art"))
	c.Check(os.WriteFile(filepath.Join(cacheDir, pin), []byte("TAMPERED"), 0o444))
	err := run([]string{"check", "--global-only"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Global Artifact Cache check failed") {
		t.Errorf("a corrupt artifact must fail the check, got: %v", err)
	}

	out.Reset()
	c.Check(run([]string{"check", "--global-only", "--fix"}, strings.NewReader(""), &out, io.Discard))
	if !strings.Contains(out.String(), "1 artifacts hashed, 0 url links verified; 1 corrupt, 1 entries removed") {
		t.Errorf("fix run must report the removal and succeed, got:\n%s", out.String())
	}

	if _, statErr := os.Stat(bystander); statErr != nil {
		t.Errorf("--fix must stay inside the cache's own subdir: %v", statErr)
	}
}

// No marker under cwd: a guard that slipped past root resolution fails with a different message.
func TestBuild_ShellNeedsOneDir(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, flag := range []string{"--shell", "--post-shell"} {
		err := run([]string{"a", "b", flag}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "exactly one target dir") {
			t.Errorf("bufa a b %s must fail naming the one-target rule, got: %v", flag, err)
		}
	}
}
