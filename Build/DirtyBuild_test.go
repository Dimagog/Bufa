package Build

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

func (f *fixture) dirtyBuilder() *DirtyBuilder {
	bldFS := vfs.NewBasePathFs(vfs.NewOsFs(), f.bld)
	return NewDirtyBuilder(Runtime.NewTest(f.srcFS, bldFS, f.src, f.bld, io.Discard, true))
}

func (f *fixture) srcExists(rel string) bool {
	return osExists(filepath.Join(f.src, filepath.FromSlash(rel)))
}

func (f *fixture) readSrc(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.src, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return strings.TrimSpace(string(data))
}

func TestDirtyBuild_BuildsInPlaceAndSkips(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src")

	h1 := f.dirtyBuilder().Build("U")
	if !strings.HasPrefix(h1, "D") {
		t.Fatalf("dirty hash must be a tree content hash, got %q", h1)
	}
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("first dirty build ran script %d times, want 1", r)
	}
	if !f.srcExists("U/out.txt") {
		t.Error("dirty output must land in the source tree")
	}
	for _, root := range []string{Store.InRoot, Store.OutRoot, Store.BldSandboxRoot} {
		if f.exists(root) {
			t.Errorf("dirty build must not create the %s/ tree", root)
		}
	}
	if !f.exists("dirty/∕U") {
		t.Error("successful dirty build must write the dirty/∕U skip hash")
	}
	if f.exists("tmp") {
		t.Error("tmp/ with the generated build script must be deleted after a successful dirty build")
	}

	b2 := f.dirtyBuilder()
	msgs := captureLogs(t)
	if h2 := b2.Build("U"); h2 != h1 {
		t.Errorf("unchanged tree changed dirty hash: %q -> %q", h1, h2)
	}
	if r := f.countRuns(t); r != 1 {
		t.Errorf("skip-hash match must skip the script: %d runs, want 1", r)
	}
	if c := count(msgs, "Cache 'Store DirtySkipHash' hit:"); c != 1 {
		t.Errorf("dirty skip-hash hit ×%d, want 1", c)
	}

	if h3 := b2.Build("U"); h3 != h1 {
		t.Errorf("local cache changed dirty hash: %q -> %q", h1, h3)
	}
	if c := count(msgs, "Cache 'Local DirtyBuild' hit:"); c != 1 {
		t.Errorf("local dirty build cache hit ×%d, want 1", c)
	}
}

// Outputs are hashed by default, so deleting one changes the tree hash and the build restores it.
func TestDirtyBuild_OutputTamperRebuilds(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src")

	f.dirtyBuilder().Build("U")
	c.Check(os.Remove(filepath.Join(f.src, "U", "out.txt")))

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("deleted output must force a rebuild: %d runs, want 2", r)
	}
	if !f.srcExists("U/out.txt") {
		t.Error("rebuild must restore the deleted output")
	}

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("restored tree must skip again: %d runs, want 2", r)
	}
}

func TestDirtyBuild_BldDepChangePropagates(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "dep-v1")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("Dep + Parent should each run once, got %d", r)
	}

	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("unchanged rebuild must skip both: %d runs, want 2", r)
	}

	f.write(t, filepath.Join("Dep", "own.txt"), "dep-v2")
	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 4 {
		t.Errorf("dep source change must rebuild dep AND consumer: %d runs, want 4", r)
	}
}

func TestDirtyBuild_ForceRebuild(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "dep")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("Dep + Parent should each run once, got %d", r)
	}

	b := f.dirtyBuilder()
	b.ForceRebuild = Runtime.ScopeTarget
	b.TargetDirs = []string{"Parent"} // CLI normally sets this; no CLI here
	msgs := captureLogs(t)
	b.Build("Parent")
	if r := f.countRuns(t); r != 3 {
		t.Errorf("--force must re-run the target only: %d runs, want 3", r)
	}
	if c := count(msgs, "Own dirty hash:"); c != 1 {
		t.Errorf("pre-build tree hash ×%d, want 1 (the forced target must not be hashed before its build)", c)
	}

	b = f.dirtyBuilder()
	b.ForceRebuild = Runtime.ScopeAll
	b.Build("Parent")
	if r := f.countRuns(t); r != 5 {
		t.Errorf("--force-all must re-run both: %d runs, want 5", r)
	}

	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 5 {
		t.Errorf("plain build after forced ones must skip (skip hashes re-recorded): %d runs, want 5", r)
	}
}

func TestDirtyBuild_NestedBldDepSkips(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "tool"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("tool + U should each run once, got %d", r)
	}
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("unchanged repeat must skip both: %d runs, want 2", r)
	}
}

func TestDirtyBuild_NestedVirtualDepSkips(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "tool.BUFA"),
		bufaToml("@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\necho out>out.txt\r\n",
			"echo x >> \"$BUFA_TEST_COUNTER\"\necho out > out.txt\n"))
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("tool + U should each run once, got %d", r)
	}
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("unchanged repeat must skip both: %d runs, want 2", r)
	}
}

func TestDirtyBuild_DeepNestedDepSkips(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "a", "b"), "dep")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"a/b\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("a/b + U should each run once, got %d", r)
	}
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("unchanged repeat must skip both: %d runs, want 2", r)
	}
}

func TestDirtyBuild_SrcDep(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Dep", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Dep", "data.txt"), "dep-v1")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nsrc = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("src dep must not be built: %d runs, want 1", r)
	}
	if f.srcExists("Dep/out.txt") {
		t.Error("src dep must not be built (no output expected)")
	}

	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("unchanged rebuild must skip: %d runs, want 1", r)
	}

	f.write(t, filepath.Join("Dep", "data.txt"), "dep-v2")
	f.dirtyBuilder().Build("Parent")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("src dep change must rebuild the consumer: %d runs, want 2", r)
	}
}

func TestDirtyBuild_DirtyFilterOptsOutOutput(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"),
		"[filters]\ndirty = [\"-/out.txt\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "src")

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("first build: %d runs, want 1", r)
	}

	c.Check(os.Remove(filepath.Join(f.src, "U", "out.txt")))
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("filtered-out output deletion must go undetected: %d runs, want 1", r)
	}

	f.write(t, filepath.Join("U", "own.txt"), "src-v2")
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("source change must still rebuild: %d runs, want 2", r)
	}
}

func TestDirtyBuild_VirtualDirMaterialized(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())

	f.dirtyBuilder().Build("Build/clj")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("first virtual dirty build: %d runs, want 1", r)
	}
	if !f.srcExists("Build/clj/out.txt") {
		t.Fatal("virtual dir must be materialized in the source tree with its output")
	}

	f.dirtyBuilder().Build("Build/clj")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("unchanged materialized virtual dir must skip: %d runs, want 1", r)
	}

	writeFile(t, f.srcFS, filepath.Join("Build", "clj", "keep.txt"), []byte("k"))
	f.dirtyBuilder().Build("Build/clj")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("changed virtual dir must rebuild: %d runs, want 2", r)
	}
	if !f.srcExists("Build/clj/keep.txt") {
		t.Error("virtual dir must never be wiped between builds")
	}
}

func TestDirtyBuild_MaterializedVirtualDirVsCleanMode(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Build", "parent-src")
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())
	skipIfNoSymlinks(t, f.builder().store)

	k1 := f.builder().Build("Build") // clean, pre-materialization
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("clean parent build: %d runs, want 1", r)
	}

	f.dirtyBuilder().Build("Build/clj") // materializes src/Build/clj
	if !f.srcExists("Build/clj/out.txt") {
		t.Fatal("virtual dir not materialized")
	}

	k2 := f.builder().Build("Build") // clean again, fresh builder
	if k2 != k1 {
		t.Errorf("materialized virtual dir re-keyed the parent: %q -> %q", k1, k2)
	}
	if r := f.countRuns(t); r != 2 { // 1 clean parent + 1 dirty clj
		t.Errorf("clean parent rebuild must stay a cache hit: %d runs, want 2", r)
	}

	err := c.Rescue(func() { f.builder().Build("Build/clj") })
	if err == nil {
		t.Fatal("clean build of a materialized virtual dir must panic")
	}
	if !strings.Contains(err.Error(), "perhaps dirty-build output needs cleaning") {
		t.Errorf("panic %q should carry the dirty-cleanup hint", err)
	}
}

func TestDirtyBuild_FileAtVirtualPathCollides(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())
	f.write(t, filepath.Join("Build", "clj"), "a file, not a dir")

	err := c.Rescue(func() { f.dirtyBuilder().Build("Build/clj") })
	if err == nil {
		t.Fatal("a file at the virtual path must panic in dirty mode too")
	}
	if !strings.Contains(err.Error(), "Unexpected file") {
		t.Errorf("panic %q should be the file-collision message, not a raw MkdirAll error", err)
	}
	if r := f.countRuns(t); r != 0 {
		t.Errorf("collision must be caught before any script runs; got %d runs", r)
	}
}

// Regression: the flag travels with the config, so a clean build still panics when a warm daemon
// serves what a dirty builder primed after materializing.
func TestDirtyBuild_MaterializedFlagEnforcedPerMode(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())

	dirty := f.dirtyBuilder()
	if cfg := dirty.getBuildConfig("Build/clj"); cfg.VirtualDirMaterialized {
		t.Fatal("virtual dir not materialized yet, VirtualDirMaterialized must be false")
	}

	dirty.Build("Build/clj") // materializes src/Build/clj

	cfg := f.dirtyBuilder().getBuildConfig("Build/clj")
	if !cfg.VirtualDirMaterialized {
		t.Fatal("materialized virtual dir must set VirtualDirMaterialized")
	}

	err := c.Rescue(func() { f.builder().requireVirtualDirAbsent("Build/clj", cfg) })
	if err == nil {
		t.Fatal("clean mode must reject a materialized-virtual-dir config")
	}
	if !strings.Contains(err.Error(), "perhaps dirty-build output needs cleaning") {
		t.Errorf("panic %q should carry the dirty-cleanup hint", err)
	}
}

func TestDirtyBuild_FailureKeepsScriptAndWritesNoSkipHash(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winFail, unixFail))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.dirtyBuilder()
	if err := c.Rescue(func() { b.Build("U") }); err == nil {
		t.Fatal("failing script must panic out of Build")
	}
	if _, ok := b.localBuildCache.Get("U"); ok {
		t.Error("failing script must not populate the local build cache")
	}
	if !f.exists("tmp/" + buildScriptFile()) {
		t.Error("generated build script must be kept in tmp/ on failure for debugging")
	}
	if f.exists("dirty/∕U") {
		t.Error("failed build must not write a skip hash")
	}
}

func TestDirtyBuild_ScriptEnv(t *testing.T) {
	sentinelDir := "/bufa-path-sentinel"
	sep := ":"
	if runtime.GOOS == "windows" {
		sentinelDir, sep = `C:\bufa-path-sentinel`, ";"
	}
	t.Setenv("PATH", sentinelDir+sep+os.Getenv("PATH"))

	f := newFixture(t)
	win := winBufaEnvScript + "echo %PATH%>path.txt\r\n"
	unix := unixBufaEnvScript + "printf '%s' \"$PATH\" > path.txt\n"
	f.write(t, filepath.Join("A", "U", "BUFA"), bufaToml(win, unix))

	f.dirtyBuilder().Build(filepath.Join("A", "U"))

	if got := f.readSrc(t, "A/U/root.txt"); got != f.src {
		t.Errorf("BUFA_BUILD_ROOT = %q, want source root %q", got, f.src)
	}
	if got, want := f.readSrc(t, "A/U/dir.txt"), filepath.Join("A", "U"); got != want {
		t.Errorf("BUFA_BUILD_DIR = %q, want root-relative %q", got, want)
	}
	if got, want := f.readSrc(t, "A/U/cacheroot.txt"), filepath.Join(f.bld, Store.UserRoot); got != want {
		t.Errorf("BUFA_CACHE_ROOT = %q, want the clean build root's %q", got, want)
	}
	if got := f.readSrc(t, "A/U/path.txt"); !strings.Contains(got, sentinelDir) {
		t.Errorf("dirty build must inherit the caller's PATH, got %q", got)
	}
	wantVerb := "cp"
	if runtime.GOOS == "windows" {
		wantVerb = "copy"
	}
	if got := f.readSrc(t, "A/U/verb.txt"); got != wantVerb {
		t.Errorf("BUFA_COPY_OR_MOVE = %q, want %q", got, wantVerb)
	}
}

func TestDirtyBuild_CircularBldDepPanics(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("A", "BUFA"),
		"[deps]\nbld = [\"../B\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("B", "BUFA"),
		"[deps]\nbld = [\"../A\"]\n"+bufaToml(winScript, unixScript))
	b := f.dirtyBuilder()

	err := c.Rescue(func() { b.Build("A") })
	if err == nil {
		t.Fatal("circular bld deps must panic")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "circular") {
		t.Fatalf("panic %q should mention circular dependency", err)
	}
	if len(b.buildingNow) != 0 {
		t.Fatalf("building set not cleared after panic: %#v", b.buildingNow)
	}
	if r := f.countRuns(t); r != 0 {
		t.Fatalf("cycle detection should happen before scripts run; got %d runs", r)
	}
}

func TestDirtyBuild_SrcDepWithoutConfigPanics(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Dep", "data.txt"), "no config here")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nsrc = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	err := c.Rescue(func() { f.dirtyBuilder().Build("Parent") })
	if err == nil {
		t.Fatal("src dep without a config must panic")
	}
	if !strings.Contains(err.Error(), "No '") {
		t.Errorf("panic %q should be the missing-config message", err)
	}
}

func TestDirtyBuild_DirSymlinkBuildsAndSkips(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "OWN")
	f.write(t, filepath.Join("U", "real", "data.txt"), "DATA")
	db := f.dirtyBuilder()
	skipIfNoSymlinks(t, db.store)
	osSymlink(filepath.Join(f.src, "U", "real"), filepath.Join(f.src, "U", "lnk"))

	db.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("first dirty build: %d runs, want 1", r)
	}

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("repeat dirty build: %d runs, want 1 (skip must be stable with a dir link)", r)
	}
}

func TestDirtyBuild_ShellInstead(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "own-of-u")

	wantVerb := byOS("copy", "cp")
	b := f.dirtyBuilder()
	out := shellSession(&b.Config, Runtime.ModeShell, "U",
		byOS("@echo SHELL_ROOT=%BUFA_BUILD_ROOT%\r\n@echo SHELL_VERB=%BUFA_COPY_OR_MOVE%\r\n@type own.txt\r\nexit\r\n",
			"echo SHELL_ROOT=$BUFA_BUILD_ROOT\necho SHELL_VERB=$BUFA_COPY_OR_MOVE\ncat own.txt\nexit\n"))
	if h := b.Build("U"); h != "" {
		t.Errorf("a shell-only session builds nothing, got %q", h)
	}
	if r := f.countRuns(t); r != 0 {
		t.Errorf("the shell replaces the script: %d runs, want 0", r)
	}
	for _, want := range []string{
		"Shell Start: U",
		"Dir: " + filepath.Join(f.src, "U"),
		"SHELL_ROOT=" + f.src, // dirty mode's env
		"SHELL_VERB=" + wantVerb,
		"own-of-u", // cwd is the real source dir
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("session output lacks %q:\n%s", want, out.String())
		}
	}
	if f.srcExists("U/out.txt") || f.exists("dirty/∕U") {
		t.Error("a shell-only session must leave no script output or skip hash")
	}
	f.requireKeptForInspection(t)

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Errorf("the next plain dirty build must run the script: %d runs, want 1", r)
	}
}

func TestDirtyBuild_ShellAfterRunsScriptRecordsNothing(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "own-of-u")
	f.dirtyBuilder().Build("U")

	b := f.dirtyBuilder()
	out := shellSession(&b.Config, Runtime.ModePostShell, "U",
		byOS("@type out.txt\r\n@echo tweak>shell.txt\r\nexit\r\n", "cat out.txt\necho tweak > shell.txt\nexit\n"))
	if h := b.Build("U"); h != "" {
		t.Errorf("a session builds nothing, got %q", h)
	}
	if r := f.countRuns(t); r != 2 {
		t.Errorf("--post-shell must run the script past the skip hash: %d runs, want 2", r)
	}
	if s := out.String(); strings.Index(s, "Dirty-Building dir: U") > strings.Index(s, "Shell Start: U") || !strings.Contains(s, "own-of-u") {
		t.Errorf("the shell must open after the script, seeing its output:\n%s", s)
	}
	f.requireKeptForInspection(t)

	// The shell changed the tree, so a recorded post-session skip hash would wrongly skip here.
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 3 {
		t.Errorf("a session records no skip hash, so the next plain build re-runs: %d runs, want 3", r)
	}
}
