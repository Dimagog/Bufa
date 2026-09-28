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

// A marked source root as cwd, its build root under a scratch $BUFA_BUILD_ROOT.
func newProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("BUFA_BUILD_ROOT", t.TempDir())
	c.Check(os.WriteFile(filepath.Join(root, Store.SrcRootFileName), nil, 0o644))
	t.Chdir(root)
	return root
}

func writeConfig(t *testing.T, root, dir, winCmd, unixCmd string) {
	t.Helper()
	writeConfigWithHead(t, root, dir, "", winCmd, unixCmd)
}

func writeConfigWithHead(t *testing.T, root, dir, head, winCmd, unixCmd string) {
	t.Helper()
	c.Check(os.MkdirAll(filepath.Join(root, dir), 0o755))
	c.Check(os.WriteFile(filepath.Join(root, dir, Store.BuildConfigName),
		[]byte(head+"[windows]\ncmd = '"+winCmd+"'\n[unix]\ncmd = '"+unixCmd+"'\n"), 0o644))
}

func byOS(win, unix string) string {
	if runtime.GOOS == "windows" {
		return win
	}
	return unix
}

// The result line per target kind: a build names its output, a task reports success, a failure reports nothing.
func TestBuild_ResultLines(t *testing.T) {
	skipIfNoSymlinks(t)
	root := newProject(t)
	writeConfig(t, root, "U", "@rem noop", ":")
	writeConfigWithHead(t, root, "T", "task = true\n", "@rem noop", ":")
	writeConfigWithHead(t, root, "F", "task = true\n", "@exit /b 3", "exit 3")

	var out strings.Builder
	c.Check(run([]string{"U", "-d"}, strings.NewReader(""), &out, io.Discard))
	if s := out.String(); !strings.Contains(s, "\nBuild result: ") || strings.Contains(s, "succeeded") {
		t.Errorf("a build prints its Build result line, got:\n%s", s)
	}
	out.Reset()
	c.Check(run([]string{"T", "-d"}, strings.NewReader(""), &out, io.Discard))
	if s := out.String(); !strings.Contains(s, "\nTask succeeded\n") || strings.Contains(s, "result") {
		t.Errorf("a task prints Task succeeded and no result line, got:\n%s", s)
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

// The tokens after a task are its arguments; -o streams the script's echo of them.
func TestBuild_TaskArgs(t *testing.T) {
	skipIfNoSymlinks(t)
	root := newProject(t)
	writeConfig(t, root, "U", "@rem noop", ":")
	writeConfigWithHead(t, root, "T", "task = ['out', '*rest']\n", "@echo out=%out% rest=[%rest%]", `echo "out=$out rest=[$rest]"`)
	writeConfigWithHead(t, root, "One", "task = ['out']\ndeps.bld = ['/U']\n", "@echo out=%out%", `echo "out=$out"`)
	t.Setenv("out", "from-env")
	os.Unsetenv("out")

	runOK := func(args ...string) string {
		t.Helper()
		var out strings.Builder
		if err := run(args, strings.NewReader(byOS("exit\r\n", "exit\n")), &out, io.Discard); err != nil {
			t.Fatalf("bufa %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}
	runErr := func(want string, args ...string) string {
		t.Helper()
		var out strings.Builder
		err := run(args, strings.NewReader(""), &out, io.Discard)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("bufa %s: want error containing %q, got %v", strings.Join(args, " "), want, err)
		}
		return out.String()
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"T", `C:\Tools`, "-d", "-o"}, `out=C:\Tools rest=[]`},            // a volume-carrying token is not a dir
		{[]string{"-d", "-o", "--", "T", "x", "-x", "--y"}, "out=x rest=[-x --y]"}, // dash tokens: the dir list after --, space-joined
		{[]string{"T", "gc", "-d", "-o"}, "out=gc rest=[]"},                        // command names win as the first token only
		{[]string{"One", "tok", "-d", "-o"}, "out=tok"},
	} {
		if s := runOK(tc.args...); !strings.Contains(s, tc.want) || !strings.Contains(s, "Task succeeded") {
			t.Errorf("bufa %s: want %q, got:\n%s", strings.Join(tc.args, " "), tc.want, s)
		}
	}
	if s := runOK("T", "a", "b", "--shell", "-d"); !strings.Contains(s, "Shell Start: T") {
		t.Errorf("a task with arguments is one target for --shell:\n%s", s)
	}

	if s := runErr("task 'T' must be the only target", "U", "T", "-d"); strings.Contains(s, "Building dir") {
		t.Errorf("a task after a build dir fails before anything builds:\n%s", s)
	}
	if s := runErr("argument 'out' is missing", "One", "-d"); strings.Contains(s, "Building dir") {
		t.Errorf("a missing argument fails before the deps build:\n%s", s)
	}
	runErr("takes 1 argument(s), unexpected: 'b'", "One", "a", "b", "-d")
	runErr("argument 'out' must not be empty", "One", "", "-d")
	runErr("argument 'out' must hold a single line", "One", "a\nb", "-d")

	// A fixed argument without a token takes the caller's variable; a token overrides it.
	t.Setenv("out", "from-env")
	if s := runOK("One", "-d", "-o"); !strings.Contains(s, "out=from-env") {
		t.Errorf("the caller's variable fills a missing argument:\n%s", s)
	}
	if s := runOK("One", "tok", "-d", "-o"); !strings.Contains(s, "out=tok") {
		t.Errorf("a token overrides the caller's variable:\n%s", s)
	}
}
