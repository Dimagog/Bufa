package Build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Runtime"
	c "github.com/dimagog/bufa/internal/contract"
)

func taskToml(winCmd, unixCmd string) string {
	return "task = true\n" + bufaToml(winCmd, unixCmd)
}

func (f *fixture) task(t *testing.T, rel string) {
	t.Helper()
	f.write(t, filepath.Join(rel, "BUFA"), taskToml(winScript, unixScript))
	f.write(t, filepath.Join(rel, "own.txt"), "own")
}

func TestTask_RunsEveryInvocationPublishesNothing(t *testing.T) {
	f := newFixture(t)
	f.task(t, "T")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	var out bytes.Buffer
	b.Out = &out

	if h := b.Build("T"); h != "" {
		t.Errorf("a task has no result hash, got %q", h)
	}
	if s := out.String(); !strings.Contains(s, "Running task: T") || strings.Contains(s, "Building dir:") {
		t.Errorf("a task is announced as a task, not a build:\n%s", s)
	}
	if r := f.countRuns(t); r != 1 {
		t.Errorf("task ran %d times, want 1", r)
	}
	if d := f.outDirs(t); d != 0 {
		t.Errorf("a task publishes nothing, got %d out dirs", d)
	}
	if f.exists("out/∕T") {
		t.Error("a task must not record a build result")
	}
	if f.exists("bld/T") || f.exists("tmp") {
		t.Error("a successful task cleans its sandbox and tmp/ like a build")
	}

	// A duplicate target in one invocation runs once.
	b.Build("T")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("the local cache still applies within one invocation: %d runs, want 1", r)
	}

	b2 := f.builder()
	out.Reset()
	b2.Out = &out
	b2.Build("T")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("a task is never a cache hit: %d runs, want 2", r)
	}
	if strings.Contains(out.String(), "Already built:") {
		t.Errorf("a task never reports a hit:\n%s", out.String())
	}
}

func TestTask_DepsBuildAndCacheNormally(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "dep")
	f.write(t, filepath.Join("T", "BUFA"), "task = true\n[deps]\nbld = ['/Dep']\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("T", "own.txt"), "own")
	skipIfNoSymlinks(t, f.builder().store)

	f.builder().Build("T")
	f.builder().Build("T")
	if r := f.countRuns(t); r != 3 {
		t.Errorf("Dep once + the task twice: %d runs, want 3", r)
	}
	if d := f.outDirs(t); d != 1 {
		t.Errorf("only the dep's output is published, got %d out dirs", d)
	}
}

func TestTask_CannotBeBldDep(t *testing.T) {
	f := newFixture(t)
	f.task(t, "T")
	f.unit(t, "Dep", "dep")
	f.write(t, filepath.Join("A", "BUFA"), "[deps]\nbld = ['/Dep', '/T']\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("A", "own.txt"), "own")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	requireErrorContains(t, c.Rescue(func() { b.Build("A") }), "'T' is a task and cannot be a build dependency of 'A'")
	if r := f.countRuns(t); r != 0 {
		t.Errorf("the dep list is checked before any dep builds: %d runs, want 0", r)
	}

	// A task may still be a deps.src (its source tree only).
	f.write(t, filepath.Join("S", "BUFA"), "[deps]\nsrc = ['/T']\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("S", "own.txt"), "own")
	f.builder().Build("S")
}

func TestTask_CannotBeShellProvider(t *testing.T) {
	f := newFixture(t)
	f.wrapperProvider(t, "P")
	f.write(t, filepath.Join("P", "BUFA"), providerToml("task = true\n"))
	f.write(t, filepath.Join("U", "BUFA"), "shell = '/P'\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "own")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "'P' is a task and cannot be a build dependency of 'U'")
}

func TestTask_FailingScriptReportsAndKeepsSandbox(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("T", "BUFA"), taskToml(winFail, unixFail))
	f.write(t, filepath.Join("T", "own.txt"), "own")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	var out bytes.Buffer
	b.Out = &out

	requireErrorContains(t, c.Rescue(func() { b.Build("T") }), "exit status 1")
	f.requireKeptForInspection(t, "bld/T/own.txt")
	if s := out.String(); !strings.Contains(s, "----- Task Start: T -----") || !strings.Contains(s, "----- Task FAILED (exit code 1): T -----") {
		t.Errorf("a task's frame says Task, not Build:\n%s", s)
	}
}

func TestTask_ShellSession(t *testing.T) {
	f := newFixture(t)
	f.task(t, "T")
	skipIfNoSymlinks(t, f.builder().store)

	b := f.builder()
	out := shellSession(b.Config, Runtime.ModeShell, "T", byOS("exit\r\n", "exit\n"))
	if h := b.Build("T"); h != "" || f.countRuns(t) != 0 {
		t.Errorf("a session on a task runs no script: hash %q, runs %d", h, f.countRuns(t))
	}
	if !strings.Contains(out.String(), "Shell Start: T") {
		t.Errorf("a task opens its shell like any dir:\n%s", out.String())
	}
}

// A task publishes nothing and its sandbox is wiped, so the script reports through a file named by an argument.
func TestTask_ArgsLetCallerEnvThroughSafeEnv(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.txt")
	t.Setenv("TASK_ARG_REPORT", report)
	t.Setenv("TASK_ARG_VALUE", "probe-val")
	t.Setenv("TASK_NOT_DECLARED", "leaked")
	os.Unsetenv("TASK_ARG_MISSING")

	win := "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\n" +
		"echo value=%TASK_ARG_VALUE%>\"%TASK_ARG_REPORT%\"\r\n" +
		"echo leak=%TASK_NOT_DECLARED%>>\"%TASK_ARG_REPORT%\"\r\n" +
		"echo missing=[%TASK_ARG_MISSING%]>>\"%TASK_ARG_REPORT%\"\r\n"
	unix := "echo x >> \"$BUFA_TEST_COUNTER\"\n" +
		"printf 'value=%s\\n' \"$TASK_ARG_VALUE\" > \"$TASK_ARG_REPORT\"\n" +
		"printf 'leak=%s\\n' \"$TASK_NOT_DECLARED\" >> \"$TASK_ARG_REPORT\"\n" +
		"printf 'missing=[%s]\\n' \"$TASK_ARG_MISSING\" >> \"$TASK_ARG_REPORT\"\n"
	depWin := "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\necho %TASK_ARG_VALUE%>out.txt\r\n"
	depUnix := "echo x >> \"$BUFA_TEST_COUNTER\"\nprintf '%s' \"$TASK_ARG_VALUE\" > out.txt\n"

	f := newFixture(t)
	f.write(t, filepath.Join("Dep", "BUFA"), bufaToml(depWin, depUnix))
	f.write(t, filepath.Join("Dep", "own.txt"), "dep")
	f.write(t, filepath.Join("T", "BUFA"),
		"task = ['TASK_ARG_REPORT', 'TASK_ARG_VALUE', 'TASK_ARG_MISSING']\n[deps]\nbld = ['/Dep']\n"+bufaToml(win, unix))
	f.write(t, filepath.Join("T", "own.txt"), "own")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	b.Build("T")
	got := strings.ReplaceAll(string(c.Check2(os.ReadFile(report))), "\r\n", "\n")
	if want := "value=probe-val\nleak=\nmissing=[]\n"; got != want {
		t.Errorf("task env report:\n%s\nwant:\n%s", got, want)
	}
	if dep := f.readOut(t, b.Build("Dep"), "out.txt"); strings.Contains(dep, "probe-val") {
		t.Errorf("a task's arguments never reach its deps, got %q", dep)
	}

	// The session env is the script env.
	b2 := f.builder()
	out := shellSession(b2.Config, Runtime.ModeShell, "T",
		byOS("@echo SESSION_SEES=%TASK_ARG_VALUE%\r\nexit\r\n", "echo SESSION_SEES=$TASK_ARG_VALUE\nexit\n"))
	b2.Build("T")
	if !strings.Contains(out.String(), "SESSION_SEES=probe-val") {
		t.Errorf("a session on a task sees its arguments:\n%s", out.String())
	}
}

func TestDirtyTask_RunsEveryInvocationRecordsNothing(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "dep")
	f.write(t, filepath.Join("T", "BUFA"), "task = true\n[deps]\nbld = ['/Dep']\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("T", "own.txt"), "own")

	b := f.dirtyBuilder()
	var out bytes.Buffer
	b.Out = &out
	if h := b.Build("T"); h != "" {
		t.Errorf("a task has no result hash, got %q", h)
	}
	if s := out.String(); !strings.Contains(s, "Dirty-Running task: T") || strings.Contains(s, "Dirty-Building dir: T") {
		t.Errorf("a dirty task is announced as a task, not a build:\n%s", s)
	}
	if !f.srcExists("T/out.txt") {
		t.Error("a dirty task runs in place")
	}
	if f.exists("dirty/∕T") {
		t.Error("a task records no skip hash")
	}
	if !f.exists("dirty/∕Dep") {
		t.Error("the task's dep records its skip hash as usual")
	}

	f.dirtyBuilder().Build("T")
	if r := f.countRuns(t); r != 3 {
		t.Errorf("Dep once (skipped the second time) + the task twice: %d runs, want 3", r)
	}
}

func TestDirtyTask_CannotBeBldDep(t *testing.T) {
	f := newFixture(t)
	f.task(t, "T")
	f.write(t, filepath.Join("A", "BUFA"), "[deps]\nbld = ['/T']\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("A", "own.txt"), "own")

	requireErrorContains(t, c.Rescue(func() { f.dirtyBuilder().Build("A") }),
		"'T' is a task and cannot be a build dependency of 'A'")
	if r := f.countRuns(t); r != 0 {
		t.Errorf("nothing runs: %d runs", r)
	}
}

// Arguments through cmd/test-shell, whose `env NAME` prints NAME=<value> or "NAME unset"; -O streams it.
func (f *fixture) argTask(t *testing.T, dir, decl string) {
	t.Helper()
	f.write(t, filepath.Join(dir, "BUFA"), decl+"shell = '/P'\ncmd = '''env out\nenv rest\n'''\n")
	f.write(t, filepath.Join(dir, "own.txt"), "own")
}

func (f *fixture) runArgTask(t *testing.T, b *Builder, dir string, tokens ...string) string {
	t.Helper()
	var out bytes.Buffer
	b.Out = &out
	b.ShowOutput = Runtime.ScopeAll
	b.BindArgs(dir, tokens)
	b.Build(dir)
	return out.String()
}

func requireLines(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, line := range want {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
}

func TestTask_ArgsBoundFromTokens(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "P", testShellDef, "")
	f.write(t, filepath.Join("D", "BUFA"), "shell = '/P'\ncmd = 'write out.txt [${out}]'\n")
	f.write(t, filepath.Join("D", "own.txt"), "dep")
	f.argTask(t, "T", "task = ['out', '*rest']\ndeps.bld = ['/D']\n")
	f.argTask(t, "One", "task = ['out']\n")
	skipIfNoSymlinks(t, f.builder().store)
	t.Setenv("out", "from-env")
	t.Setenv("rest", "rest-from-env")

	requireLines(t, f.runArgTask(t, f.builder(), "T", `C:\Tools`, "-v", "--fast"), `out=C:\Tools`, "rest=-v --fast")
	requireLines(t, f.runArgTask(t, f.builder(), "T", "x"), "out=x", "rest=rest-from-env")
	os.Unsetenv("rest")
	requireLines(t, f.runArgTask(t, f.builder(), "T", "x"), "out=x", "rest unset")
	requireLines(t, f.runArgTask(t, f.builder(), "T"), "out=from-env", "rest unset")
	if dep := strings.TrimSpace(f.readOut(t, f.builder().Build("D"), "out.txt")); dep != "[]" {
		t.Errorf("a task's arguments never reach its deps, got %q", dep)
	}

	// The session env is the script env.
	b := f.builder()
	out := shellSession(b.Config, Runtime.ModeShell, "T", "env out\nexit\n")
	b.BindArgs("T", []string{"tok"})
	b.Build("T")
	requireLines(t, out.String(), "(bufa shell) > out=tok")

	// Every argument error is raised by BindArgs, before anything builds.
	os.Unsetenv("out")
	f2 := newFixture(t)
	f2.fakeProvider(t, "P", testShellDef, "")
	f2.argTask(t, "One", "task = ['out']\n")
	f2.argTask(t, "Two", "task = ['out', '*rest']\n")
	for _, tc := range []struct {
		dir    string
		tokens []string
		want   string
	}{
		{"One", nil, "argument 'out' is missing: pass it on the command line or set the env variable"},
		{"One", []string{"a", "b"}, "takes 1 argument(s), unexpected: 'b'"},
		{"One", []string{""}, "argument 'out' must not be empty"},
		{"One", []string{"a\r\nb"}, "argument 'out' must hold a single line"},
		{"Two", []string{"a", "b", "c\nd"}, "argument 'rest' must hold a single line"},
	} {
		b := f2.builder()
		requireErrorContains(t, c.Rescue(func() { b.BindArgs(tc.dir, tc.tokens) }), tc.want)
	}
	if d := f2.outDirs(t); d != 0 {
		t.Errorf("nothing builds on an argument error, got %d out dirs", d)
	}
	requireLines(t, f2.runArgTask(t, f2.builder(), "Two", "a"), "out=a", "rest unset")
}

func TestDirtyTask_ArgsBoundFromTokens(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "P", testShellDef, "")
	f.argTask(t, "T", "task = ['out', '*rest']\n")
	t.Setenv("out", "from-env")

	b := f.dirtyBuilder()
	var out bytes.Buffer
	b.Out = &out
	b.ShowOutput = Runtime.ScopeAll
	b.BindArgs("T", []string{"tok", "r1", "r2"})
	b.Build("T")
	// dirty inherits the whole env, so the token must still win
	requireLines(t, out.String(), "out=tok", "rest=r1 r2")
	if f.exists("dirty/∕T") {
		t.Error("a task records no skip hash")
	}
}

// Every token reaches the script byte for byte through the native shells.
func TestTask_ArgTransport(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.txt")
	f := newFixture(t)
	f.write(t, filepath.Join("T", "BUFA"), "task = ['TASK_ARG_REPORT', 'TASK_ARG_V']\n"+bufaToml(
		"@echo off\r\nchcp 65001>nul\r\nset TASK_ARG_V>\"%TASK_ARG_REPORT%\"\r\n",
		"printf 'TASK_ARG_V=%s' \"$TASK_ARG_V\" > \"$TASK_ARG_REPORT\"\n"))
	f.write(t, filepath.Join("T", "own.txt"), "own")
	skipIfNoSymlinks(t, f.builder().store)

	for _, v := range []string{"a b", `"quoted"`, `C:\Tools\`, "100%", "bang!", "x&y", "<h>", "$HOME", "héllo wörld ✓"} {
		b := f.builder()
		b.BindArgs("T", []string{report, v})
		b.Build("T")
		got := strings.TrimRight(string(c.Check2(os.ReadFile(report))), "\r\n")
		if want := "TASK_ARG_V=" + v; got != want {
			t.Errorf("the script saw %q, want %q", got, want)
		}
	}
}
