package Build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

// The test-shell binary is built once per test binary, on first use, and removed at exit.
var testShellDir string

var testShellExe = sync.OnceValue(func() string {
	exe := filepath.Join(testShellDir, "test-shell"+byOS(".exe", ""))
	cmd := exec.Command("go", "build", "-o", exe, "../cmd/test-shell")
	cmd.Stderr = os.Stderr
	c.Checkf(cmd.Run(), "build test-shell")
	return exe
})

func TestMain(m *testing.M) {
	testShellDir = c.Check2(os.MkdirTemp("", "bufa-test-shell-"))
	code := m.Run()
	os.RemoveAll(testShellDir)
	os.Exit(code)
}

const testShellDef = `exe = "./test-shell"
ext = ".fake"
run = ["${script}"]
shell = ["-i"]
postShell = ["-i", "${script}"]
prompt = { env = "FAKE_PROMPT", default = "> " }
`

const testShellDefWithVerbs = testShellDef + "move = \"mv\"\ncopy = \"cp\"\n"

// The test-shell language: one cross-platform body appends a run marker and writes out.txt.
const testScript = "append ${BUFA_TEST_COUNTER} x\nwrite out.txt hello\n"

// A provider's own script: the native no-op, under the native shell named explicitly.
func providerToml(extra string) string {
	return extra + "[windows]\nshell = \"cmd\"\ncmd = \"@rem noop\"\n[unix]\nshell = \"bash\"\ncmd = \":\"\n"
}

// The test-shell binary as own source of dir (mode kept, so it publishes executable).
func (f *fixture) copyTestShell(dir string) {
	exe := testShellExe()
	data := c.Check2(os.ReadFile(exe))
	path := filepath.Join(f.src, dir, filepath.Base(exe))
	c.Check(os.MkdirAll(filepath.Dir(path), 0o755))
	c.Check(os.WriteFile(path, data, 0o755))
}

// A provider dir whose output is the test-shell binary beside its BUFA.shell.
func (f *fixture) fakeProvider(t *testing.T, dir, def, extraToml string) {
	t.Helper()
	f.copyTestShell(dir)
	f.write(t, filepath.Join(dir, Store.ShellDefFileName), def)
	f.write(t, filepath.Join(dir, "BUFA"), providerToml(extraToml))
}

// A provider that wraps the native shell: no new binary, its BUFA.shell is the preset itself.
func (f *fixture) wrapperProvider(t *testing.T, dir string) {
	t.Helper()
	f.write(t, filepath.Join(dir, Store.ShellDefFileName), shellDefToml(nativePreset()))
	f.write(t, filepath.Join(dir, "BUFA"), providerToml(""))
}

func nativeShell() string { return byOS("cmd", "bash") }

func nativePreset() BuildConfig.ShellDef { return shellPresets()[nativeShell()] }

func shellDefToml(def BuildConfig.ShellDef) string {
	var out strings.Builder
	c.Check(toml.NewEncoder(&out).Encode(def))
	return out.String()
}

func TestShell_ExplicitPresetAndPlatformOverride(t *testing.T) {
	f := newFixture(t)
	// The dir's platform preset overrides the root provider before the provider is resolved.
	f.write(t, Store.SrcRootFileName, "shell = \"/unused\"\n")
	f.write(t, filepath.Join("U", "BUFA"),
		"shell = \"nope\"\n[windows]\nshell = \"cmd\"\ncmd = '''"+winScript+"'''\n[unix]\nshell = \"bash\"\ncmd = '''"+unixScript+"'''\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if got := f.readOut(t, key, "out.txt"); got != "v1" {
		t.Errorf("out.txt = %q, want v1", got)
	}
	if f.exists("bld") {
		t.Error("a preset-shell build must clean its sandbox like before")
	}
}

func TestShell_UnknownNameFails(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"nope\"\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "unknown shell 'nope'")

	// The root marker rejects an unknown alias while loading the project config.
	f.write(t, Store.SrcRootFileName, "shell = \"nope\"\n")
	f.unit(t, "V", "v1")
	requireErrorContains(t, c.Rescue(func() { f.builder().Build("V") }), "must be a [shells] alias or an absolute")
}

func TestShell_ProviderDirCannotUsePresetPrefix(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/:bash\"\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	requireErrorContains(t, c.Rescue(func() { f.builder().Build("U") }), "must not start with reserved ':'")
}

func TestShell_WrapperProvider_BuildsAndKeysByDefinition(t *testing.T) {
	f := newFixture(t)
	f.wrapperProvider(t, "P")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/P\"\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if got := f.readOut(t, key, "out.txt"); got != "v1" || f.countRuns(t) != 1 {
		t.Fatalf("out.txt = %q, runs = %d; want v1, 1", got, f.countRuns(t))
	}
	if f.exists("out/"+key+"/P") || f.exists("out/"+key+"/"+Store.ShellDefFileName) {
		t.Error("the provider's tree is a staged dep, never part of the consumer's output")
	}

	f.builder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("repeat build must hit the cache: %d runs", r)
	}

	// Editing the definition changes the provider's output ⇒ re-keys the consumer through the dep.
	noVerbs := nativePreset()
	noVerbs.Move = ""
	noVerbs.Copy = ""
	f.write(t, filepath.Join("P", Store.ShellDefFileName), shellDefToml(noVerbs))
	f.builder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("a BUFA.shell edit must re-key the consumer: %d runs, want 2", r)
	}
	// Switching to the preset changes the dep list ⇒ another key.
	f.write(t, filepath.Join("U", "BUFA"), "shell = \""+nativeShell()+"\"\n"+bufaToml(winScript, unixScript))
	f.builder().Build("U")
	if r := f.countRuns(t); r != 3 {
		t.Errorf("switching provider ⇄ preset must re-key: %d runs, want 3", r)
	}
}

func TestShell_ExplicitProviderDepFails(t *testing.T) {
	f := newFixture(t)
	f.wrapperProvider(t, "P")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/P\"\n[deps]\nbld = [\"/P\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "lists its shell provider 'P' in deps.bld")
}

func TestShell_ProviderWithoutDefinitionFails(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "P", "not-a-shell")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/P\"\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "provider 'P' output has no "+Store.ShellDefFileName)

	f.write(t, filepath.Join("P", Store.ShellDefFileName), "exe = \"x\"\n") // no ext/run
	requireErrorContains(t, c.Rescue(func() { f.builder().Build("U") }), "ext is required")
}

func TestShell_FakeProvider_RunsScriptViaProviderExe(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDef, "")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/tools/fake\"\ncmd = '''"+testScript+"env BUFA_COPY_OR_MOVE\n'''\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	out := shellSession(&b.Config, Runtime.ModeBuild, "U", "")
	b.ShowOutput = Runtime.ScopeAll

	key := b.Build("U")
	if got := f.readOut(t, key, "out.txt"); got != "hello\n" || f.countRuns(t) != 1 {
		t.Fatalf("out.txt = %q, runs = %d; want hello, 1\n%s", got, f.countRuns(t), out.String())
	}
	if !strings.Contains(out.String(), "BUFA_COPY_OR_MOVE unset") {
		t.Errorf("a definition without move/copy exports no BUFA_COPY_OR_MOVE:\n%s", out.String())
	}
	if f.exists("tmp/BUFA.fake") {
		t.Error("the script is a tmp/ tenant, gone after success")
	}
	f.builder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("repeat build must hit the cache: %d runs", r)
	}

	// A definition with verbs exports the mode's spelling (clean ⇒ move).
	f.fakeProvider(t, "tools/fake", testShellDefWithVerbs, "")
	b = f.builder()
	out = shellSession(&b.Config, Runtime.ModeBuild, "U", "")
	b.ShowOutput = Runtime.ScopeAll
	b.Build("U")
	if !strings.Contains(out.String(), "BUFA_COPY_OR_MOVE=mv") {
		t.Errorf("clean mode exports the definition's move verb:\n%s", out.String())
	}
}

func TestShell_FakeProvider_LinkStagedDefinitionReadThroughLink(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDef, "largeOutput = true\n")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/tools/fake\"\ncmd = '''"+testScript+"'''\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	key := b.Build("U")
	if got := f.readOut(t, key, "out.txt"); got != "hello\n" {
		t.Errorf("out.txt = %q, want hello", got)
	}
}

func TestShell_FakeProvider_ScriptFailurePropagates(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDef, "")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/tools/fake\"\ncmd = \"exit 7\"\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	out := shellSession(&b.Config, Runtime.ModeBuild, "U", "")
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "exit status 7")
	if !strings.Contains(out.String(), "Build FAILED (exit code 7): U") {
		t.Errorf("frame must report the shell's exit code:\n%s", out.String())
	}
	if !f.exists("tmp/BUFA.fake") {
		t.Error("a failure keeps the script for debugging")
	}
}

func TestShell_FakeProvider_Sessions(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDef, "")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/tools/fake\"\ncmd = '''"+testScript+"'''\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	skipIfNoSymlinks(t, f.builder().store)

	b := f.builder()
	out := shellSession(&b.Config, Runtime.ModeShell, "U", "echo IN_SESSION\nexit\n")
	if h := b.Build("U"); h != "" || f.countRuns(t) != 0 {
		t.Errorf("--shell builds nothing and replaces the script: hash %q, runs %d", h, f.countRuns(t))
	}
	for _, want := range []string{"Shell Start: U", "(bufa shell) > IN_SESSION", "Script: " + filepath.Join(f.bld, "tmp", "BUFA.fake")} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("session output lacks %q:\n%s", want, out.String())
		}
	}

	b = f.builder()
	out = shellSession(&b.Config, Runtime.ModePostShell, "U", "echo AFTER\nexit\n")
	b.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("--post-shell runs the script: %d runs, want 1", r)
	}
	if s := out.String(); !strings.Contains(s, "(bufa shell) > AFTER") || !f.exists("bld/U/out.txt") {
		t.Errorf("the session follows the script in its sandbox:\n%s", s)
	}
}

func TestShell_FakeProvider_SessionModeAbsentFails(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", "exe = \"./test-shell\"\next = \".fake\"\nrun = [\"${script}\"]\n", "")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/tools/fake\"\ncmd = '''"+testScript+"'''\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	skipIfNoSymlinks(t, f.builder().store)

	// A plain build never needs the session modes.
	f.builder().Build("U")

	b := f.builder()
	shellSession(&b.Config, Runtime.ModeShell, "U", "exit\n")
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "defines no 'shell' session mode")
	b = f.builder()
	shellSession(&b.Config, Runtime.ModePostShell, "U", "exit\n")
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "defines no 'postShell' session mode")
}

func TestShell_CatalogueAliasAndRootDefault(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDef, "")
	f.write(t, Store.SrcRootFileName, "shell = \"fake\"\n[shells]\nfake = \"/tools/fake\"\n")
	// U inherits the root default; V names the alias; W shadows a preset name with its own provider.
	f.write(t, filepath.Join("U", "BUFA"), "cmd = '''"+testScript+"'''\n")
	f.write(t, filepath.Join("V", "BUFA"), "shell = \"fake\"\ncmd = '''"+testScript+"'''\n")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	for _, dir := range []string{"U", "V"} {
		if got := f.readOut(t, b.Build(dir), "out.txt"); got != "hello\n" {
			t.Errorf("%s: out.txt = %q, want hello", dir, got)
		}
	}
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("runs = %d, want 2", r)
	}

	// Re-pointing the alias re-keys the consumers through the dep manifest.
	f.fakeProvider(t, "tools/fake2", testShellDef, "")
	f.write(t, Store.SrcRootFileName, "shell = \"fake\"\n[shells]\nfake = \"/tools/fake2\"\n")
	b = f.builder()
	b.Build("U")
	b.Build("V")
	if r := f.countRuns(t); r != 4 {
		t.Errorf("re-pointed alias must re-key: %d runs, want 4", r)
	}

	// An alias may shadow a preset: the project pins its own native shell.
	f.write(t, Store.SrcRootFileName, "[shells]\n"+nativeShell()+" = \"/tools/fake\"\n")
	b = f.builder()
	f.write(t, filepath.Join("W", "BUFA"), "shell = \""+nativeShell()+"\"\ncmd = '''"+testScript+"'''\n")
	if got := f.readOut(t, b.Build("W"), "out.txt"); got != "hello\n" {
		t.Errorf("W: out.txt = %q, want hello", got)
	}
}

func TestShell_DefinitionCachedAcrossDirs(t *testing.T) {
	f := newFixture(t)
	f.wrapperProvider(t, "P")
	f.write(t, Store.SrcRootFileName, "[shells]\np = \"/P\"\n")
	for _, unit := range []struct{ dir, shell string }{{"U", "p"}, {"V", "/P"}} {
		f.write(t, filepath.Join(unit.dir, "BUFA"), "shell = \""+unit.shell+"\"\n"+bufaToml(winScript, unixScript))
		f.write(t, filepath.Join(unit.dir, "own.txt"), "v1")
	}
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	msgs := captureLogs(t)

	for _, dir := range []string{"U", "V"} {
		if got := f.readOut(t, b.Build(dir), "out.txt"); got != "v1" {
			t.Errorf("%s: out.txt = %q, want v1", dir, got)
		}
	}

	if got := count(msgs, "Loading shell definition:"); got != 1 {
		t.Errorf("shell definition loaded %d times, want 1", got)
	}
	if got := count(msgs, "Cache 'ShellDef' hit:"); got != 1 {
		t.Errorf("shell definition cache hit %d times, want 1", got)
	}
}

func TestShell_RootShellValueRekeysEveryDir(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	b.Build("U")
	// A provider wrapping the native shell preserves behavior while changing the marker default.
	f.wrapperProvider(t, "P")
	f.write(t, Store.SrcRootFileName, "shell = \"/P\"\n")
	f.builder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("the marker's shell value keys every dir: %d runs, want 2", r)
	}
	f.write(t, Store.SrcRootFileName, "")
	f.builder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("reverting the marker hits the earlier key: %d runs, want 2", r)
	}
}

func TestShell_ProviderSelfDependencyFails(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "shell = \"/tools/fake\"\n")
	// A provider that forgets to name its native shell inherits itself.
	f.fakeProvider(t, "tools/fake", testShellDef, "")
	f.write(t, filepath.Join("tools", "fake", "BUFA"), "cmd = \"@rem noop\"\n")
	f.write(t, filepath.Join("U", "BUFA"), "cmd = '''"+testScript+"'''\n")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "'"+filepath.Join("tools", "fake")+"' depends on itself via shell '/tools/fake'")

	// An indirect cycle through a dep that inherits the provider as its shell.
	f.write(t, filepath.Join("tools", "fake", "BUFA"), providerToml("[deps]\nbld = [\"/Lib\"]\n"))
	f.write(t, filepath.Join("Lib", "BUFA"), "cmd = \"echo lib\"\n")
	requireErrorContains(t, c.Rescue(func() { f.builder().Build("U") }), "circular build dependency involving "+filepath.Join("tools", "fake"))
}

func TestShell_RawScriptUnderProviderShell(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDef, "")
	f.write(t, Store.SrcRootFileName, "shell = \"/tools/fake\"\n")
	// Not TOML ⇒ the whole file is the cmd, in the provider's language; the implied dep still applies.
	f.write(t, filepath.Join("U", "BUFA"), "append ${BUFA_TEST_COUNTER} x\nwrite out.txt raw-hello\n")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	key := b.Build("U")
	if got := f.readOut(t, key, "out.txt"); got != "raw-hello\n" || f.countRuns(t) != 1 {
		t.Errorf("out.txt = %q, runs = %d; want raw-hello, 1", got, f.countRuns(t))
	}
}

func TestShell_ScriptlessDirResolvesNoShell(t *testing.T) {
	data := []byte("ARTIFACT")
	f, _, url, _ := extFixture(t, &data)
	f.write(t, filepath.Join("P", "BUFA"), "shell = \"nope\"\ncmd = false\n"+extToml(url, pinOf(data), "export = true"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	if err := c.Rescue(func() { b.Build("P") }); err != nil {
		t.Errorf("a cmd = false dir has no script, so its shell value is never resolved: %v", err)
	}
	// A session on it does need the shell.
	b = f.builder()
	shellSession(&b.Config, Runtime.ModeShell, "P", "exit\n")
	requireErrorContains(t, c.Rescue(func() { b.Build("P") }), "unknown shell 'nope'")
}

func TestShell_RelativeProviderPathAndExeGrammar(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, filepath.Join("U", "tools", "fake"), testShellDef, "")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"./tools/fake\"\ncmd = '''"+testScript+"'''\n")
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	key := b.Build("U")
	if got := f.readOut(t, key, "out.txt"); got != "hello\n" {
		t.Errorf("out.txt = %q, want hello", got)
	}
	if f.exists("out/" + key + "/tools/fake/" + Store.ShellDefFileName) {
		t.Error("a nested provider is a staged dep, pruned from the consumer's output")
	}

	// An absolute exe: the provider ships only the definition.
	f.write(t, filepath.Join("Abs", Store.ShellDefFileName),
		fmt.Sprintf("exe = '%s'\next = \".fake\"\nrun = [\"${script}\"]\n", testShellExe()))
	f.write(t, filepath.Join("Abs", "BUFA"), providerToml(""))
	f.write(t, filepath.Join("V", "BUFA"), "shell = \"/Abs\"\ncmd = '''"+testScript+"'''\n")
	if got := f.readOut(t, f.builder().Build("V"), "out.txt"); got != "hello\n" {
		t.Errorf("V: out.txt = %q, want hello", got)
	}

	// A relative exe without "./" is provider-relative too: the binary sits in a subdir of the output.
	f.copyTestShell(filepath.Join("Sub", "bin"))
	f.write(t, filepath.Join("Sub", Store.ShellDefFileName),
		"exe = \"bin/test-shell\"\next = \".fake\"\nrun = [\"${script}\"]\n")
	f.write(t, filepath.Join("Sub", "BUFA"), providerToml(""))
	f.write(t, filepath.Join("W", "BUFA"), "shell = \"/Sub\"\ncmd = '''"+testScript+"'''\n")
	if got := f.readOut(t, f.builder().Build("W"), "out.txt"); got != "hello\n" {
		t.Errorf("W: out.txt = %q, want hello", got)
	}
}

func TestDirtyBuild_ShellProviderBuiltInPlace(t *testing.T) {
	f := newFixture(t)
	f.fakeProvider(t, "tools/fake", testShellDefWithVerbs, "")
	f.write(t, filepath.Join("U", "BUFA"),
		"shell = \"/tools/fake\"\ncmd = '''"+testScript+"write verb.txt ${BUFA_COPY_OR_MOVE}\n'''\n")

	b := f.dirtyBuilder()
	b.Build("U")
	if got := f.readSrc(t, "U/out.txt"); got != "hello" || f.readSrc(t, "U/verb.txt") != "cp" {
		t.Errorf("dirty build via the in-place provider: out.txt = %q, verb = %q", got, f.readSrc(t, "U/verb.txt"))
	}
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("repeat dirty build must skip: %d runs", r)
	}
	// The provider's own BUFA.shell is part of its dirty tree hash: an edit re-runs the consumer.
	f.write(t, filepath.Join("tools", "fake", Store.ShellDefFileName), testShellDef)
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("a provider edit must re-run the consumer: %d runs, want 2", r)
	}
	if runtime.GOOS == "windows" && !f.srcExists("tools/fake/test-shell.exe") {
		t.Error("the provider's binary stays in place")
	}
}
