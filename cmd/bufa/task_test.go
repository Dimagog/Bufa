package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

func skipIfNoSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
}

func writeConfig(t *testing.T, root, dir, winCmd, unixCmd string) {
	t.Helper()
	c.Check(os.MkdirAll(filepath.Join(root, dir), 0o755))
	c.Check(os.WriteFile(filepath.Join(root, dir, Store.BuildConfigName),
		[]byte("[windows]\ncmd = '"+winCmd+"'\n[unix]\ncmd = '"+unixCmd+"'\n"), 0o644))
}

// The result line per target kind: a build names its output, a task reports success, a failure reports nothing.
func TestBuild_ResultLines(t *testing.T) {
	skipIfNoSymlinks(t)
	root := t.TempDir()
	t.Setenv("BUFA_BUILD_ROOT", t.TempDir())
	c.Check(os.WriteFile(filepath.Join(root, Store.SrcRootFileName), nil, 0o644))
	writeConfig(t, root, "U", "@rem noop", ":")
	writeConfig(t, root, "T", "@rem noop", ":")
	writeConfig(t, root, "F", "@exit /b 3", "exit 3")
	for _, task := range []string{"T", "F"} {
		path := filepath.Join(root, task, Store.BuildConfigName)
		c.Check(os.WriteFile(path, append([]byte("task = true\n"), c.Check2(os.ReadFile(path))...), 0o644))
	}
	t.Chdir(root)

	var out strings.Builder
	c.Check(run([]string{"U", "T", "-d"}, strings.NewReader(""), &out, io.Discard))
	s := out.String()
	build, task := strings.Index(s, "\nBuild result: "), strings.Index(s, "\nTask succeeded\n")
	if build < 0 || task < build || strings.Count(s, "result") != 1 {
		t.Errorf("want one Build result line for U, then Task succeeded for T, got:\n%s", s)
	}

	out.Reset()
	err := run([]string{"F", "-d"}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("a failing task fails the run, got: %v", err)
	}
	if s := out.String(); strings.Contains(s, "result") || strings.Contains(s, "succeeded") || !strings.Contains(s, "Task FAILED (exit code 3): F") {
		t.Errorf("a failed task prints the frame footer and no result line:\n%s", s)
	}
	if runtime.GOOS == "windows" && !strings.Contains(out.String(), "----- Task Start: F -----") {
		t.Errorf("the buffered output is dumped on failure:\n%s", out.String())
	}
}
