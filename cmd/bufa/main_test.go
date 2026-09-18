package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
