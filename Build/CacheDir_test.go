package Build

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

// Records the var, whether it is defined at all, and whether a marker from an earlier run survives.
const winCacheDirScript = "@echo off\r\n" +
	"if defined BUFA_CACHE_DIR (echo set>def.txt) else (echo unset>def.txt)\r\n" +
	"echo %BUFA_CACHE_DIR%>name.txt\r\n" +
	"echo %BUFA_CACHE_ROOT%\\%BUFA_CACHE_DIR%>cachedir.txt\r\n" +
	"if exist \"%BUFA_CACHE_ROOT%\\%BUFA_CACHE_DIR%\\marker.txt\" (echo history>hist.txt) else (echo fresh>hist.txt)\r\n" +
	"echo x>\"%BUFA_CACHE_ROOT%\\%BUFA_CACHE_DIR%\\marker.txt\"\r\n"
const unixCacheDirScript = "if [ -n \"${BUFA_CACHE_DIR+x}\" ]; then printf set > def.txt; else printf unset > def.txt; fi\n" +
	"printf '%s' \"$BUFA_CACHE_DIR\" > name.txt\n" +
	"printf '%s' \"$BUFA_CACHE_ROOT/$BUFA_CACHE_DIR\" > cachedir.txt\n" +
	"if [ -e \"$BUFA_CACHE_ROOT/$BUFA_CACHE_DIR/marker.txt\" ]; then printf history > hist.txt; else printf fresh > hist.txt; fi\n" +
	"echo x > \"$BUFA_CACHE_ROOT/$BUFA_CACHE_DIR/marker.txt\"\n"

// The no-cache-dir variant must not touch the (undefined) var's path.
const winNoCacheDirScript = "@echo off\r\n" +
	"if defined BUFA_CACHE_DIR (echo set>def.txt) else (echo unset>def.txt)\r\n"
const unixNoCacheDirScript = "if [ -n \"${BUFA_CACHE_DIR+x}\" ]; then printf set > def.txt; else printf unset > def.txt; fi\n"

func cacheDirToml(prefix string) string {
	return prefix + "deps.cacheDir = true\n" + bufaToml(winCacheDirScript, unixCacheDirScript)
}

func (f *fixture) userEntry(t *testing.T, srcDir string) string {
	t.Helper()
	linkName := Store.PathLinkPrefix + strings.ReplaceAll(srcDir, "/", Store.PathLinkPrefix)
	target := f.builder().store.GetLinkTarget(Store.UserRoot, linkName)
	if target == "" {
		t.Fatalf("user/%s link missing", linkName)
	}
	return filepath.Join(f.bld, Store.UserRoot, target)
}

// One requesting build dir P (own.txt "1"); rootConfig replaces newFixture's empty .BUFA marker.
func cacheDirFixture(t *testing.T, rootConfig string) (*fixture, *Builder) {
	t.Helper()
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, rootConfig)
	f.write(t, filepath.Join("P", "BUFA"), cacheDirToml(""))
	f.write(t, filepath.Join("P", "own.txt"), "1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	return f, b
}

func TestBuild_CacheDir_ExportedPersistentAndLinked(t *testing.T) {
	f, b := cacheDirFixture(t, "")

	key1 := b.Build("P")
	if got := strings.TrimSpace(f.readOut(t, key1, "def.txt")); got != "set" {
		t.Fatalf("BUFA_CACHE_DIR must be defined for a requesting dir, got %q", got)
	}
	name := strings.TrimSpace(f.readOut(t, key1, "name.txt"))
	if !Hashing.IsValidPathHash(name) {
		t.Errorf("BUFA_CACHE_DIR = %q, want a bare P… entry name", name)
	}
	cacheDir := strings.TrimSpace(f.readOut(t, key1, "cachedir.txt"))
	if want := filepath.Join(f.bld, Store.UserRoot, name); cacheDir != want {
		t.Errorf("BUFA_CACHE_ROOT\\BUFA_CACHE_DIR = %q, want %q", cacheDir, want)
	}
	if got := strings.TrimSpace(f.readOut(t, key1, "hist.txt")); got != "fresh" {
		t.Errorf("first run must find an empty cache dir, got %q", got)
	}
	if !osExists(filepath.Join(cacheDir, "marker.txt")) {
		t.Fatal("the script's marker must land in the cache dir")
	}
	if got := f.userEntry(t, "P"); got != cacheDir {
		t.Errorf("user/∕P must resolve to the exported dir: %q vs %q", got, cacheDir)
	}

	// A source change reruns the script: same dir, previous run's marker still there.
	f.write(t, filepath.Join("P", "own.txt"), "2")
	key2 := f.builder().Build("P")
	if key2 == key1 {
		t.Fatal("precondition: the source change must yield a new build")
	}
	if got := strings.TrimSpace(f.readOut(t, key2, "cachedir.txt")); got != cacheDir {
		t.Errorf("BUFA_CACHE_DIR must be stable across builds and builders: %q vs %q", got, cacheDir)
	}
	if got := strings.TrimSpace(f.readOut(t, key2, "hist.txt")); got != "history" {
		t.Errorf("cache dir content must survive a rebuild, got %q", got)
	}
}

func TestBuild_CacheDir_AbsentUnlessRequested(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("N", "BUFA"), bufaToml(winNoCacheDirScript, unixNoCacheDirScript))
	f.write(t, filepath.Join("U", "BUFA"), "unsafe = true\n"+bufaToml(winNoCacheDirScript, unixNoCacheDirScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	for _, dir := range []string{"N", "U"} {
		if got := strings.TrimSpace(f.readOut(t, b.Build(dir), "def.txt")); got != "unset" {
			t.Errorf("%s: BUFA_CACHE_DIR must be absent for a dir that doesn't request it, got %q", dir, got)
		}
	}
	if f.exists(Store.UserRoot) {
		t.Error("no requesting dir ⇒ user/ must not be created")
	}
}

func TestBuild_CacheDir_SameValueInEveryMode(t *testing.T) {
	f, b := cacheDirFixture(t, "[unsafe]\nttl = \"1h\"\n")

	safe := strings.TrimSpace(f.readOut(t, b.Build("P"), "cachedir.txt"))

	f.write(t, filepath.Join("P", "BUFA"), cacheDirToml("unsafe = true\n"))
	unsafe := strings.TrimSpace(f.readOut(t, f.builder().Build("P"), "cachedir.txt"))
	if unsafe != safe {
		t.Errorf("unsafe build BUFA_CACHE_DIR = %q, want the safe value %q", unsafe, safe)
	}

	f.dirtyBuilder().Build("P")
	if dirty := f.readSrc(t, "P/cachedir.txt"); dirty != safe {
		t.Errorf("dirty build BUFA_CACHE_DIR = %q, want the clean value %q", dirty, safe)
	}
	if got := f.readSrc(t, "P/hist.txt"); got != "history" {
		t.Errorf("dirty build must see the clean builds' cache content, got %q", got)
	}
	if f.srcExists("P/user") || osExists(filepath.Join(f.src, Store.UserRoot)) {
		t.Error("the cache dir must live in the build root, never in the source tree")
	}
}

func TestBuild_CacheDir_ForcedRebuildKeepsEntry(t *testing.T) {
	f, b := cacheDirFixture(t, "")

	cacheDir := strings.TrimSpace(f.readOut(t, b.Build("P"), "cachedir.txt"))

	forced := f.builder()
	forced.ForceRebuild = Runtime.ScopeTarget
	forced.TargetDirs = []string{"P"}
	key := forced.Build("P")
	if got := strings.TrimSpace(f.readOut(t, key, "cachedir.txt")); got != cacheDir {
		t.Errorf("forced rebuild BUFA_CACHE_DIR = %q, want %q", got, cacheDir)
	}
	if got := strings.TrimSpace(f.readOut(t, key, "hist.txt")); got != "history" {
		t.Errorf("a forced rebuild must not wipe the cache dir, got %q", got)
	}
}

// The zero-FS hot path: a cache hit never reaches user/.
func TestBuild_CacheDir_HitTouchesNothing(t *testing.T) {
	f, b := cacheDirFixture(t, "")

	key := b.Build("P")
	c.Check(os.RemoveAll(filepath.Join(f.bld, Store.UserRoot)))

	if again := f.builder().Build("P"); again != key {
		t.Fatalf("precondition: repeat build must hit, got %q vs %q", again, key)
	}
	if f.exists(Store.UserRoot) {
		t.Error("a cache hit must not recreate the entry")
	}
}

// Doc/Bugs/RenamedDirBakedPaths.md: a renamed requester must re-run against its own new entry —
// a hit would keep the old P… value baked in its output, which gc then sweeps from under consumers.
func TestBuild_CacheDir_RenamedDirGetsOwnEntry(t *testing.T) {
	f, b := cacheDirFixture(t, "")

	old := strings.TrimSpace(f.readOut(t, b.Build("P"), "cachedir.txt"))
	c.Check(f.srcFS.Rename("P", "Q"))
	key := f.builder().Build("Q")

	got := strings.TrimSpace(f.readOut(t, key, "cachedir.txt"))
	if got == old {
		t.Fatalf("renamed dir still exports the old entry %q", got)
	}
	if entry := f.userEntry(t, "Q"); entry != got {
		t.Errorf("user/∕Q -> %q, want the exported %q", entry, got)
	}
	if hist := strings.TrimSpace(f.readOut(t, key, "hist.txt")); hist != "fresh" {
		t.Errorf("a renamed dir starts a fresh entry, got %q", hist)
	}
}

func TestBuild_CacheDir_VirtualDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("Build", "go.BUFA"), cacheDirToml(""))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build(filepath.Join("Build", "go"))
	cacheDir := strings.TrimSpace(f.readOut(t, key, "cachedir.txt"))
	if got := f.userEntry(t, "Build/go"); got != cacheDir {
		t.Errorf("virtual dir's entry keyed by its virtual path: user/∕Build∕go -> %q, want %q", got, cacheDir)
	}
}

func TestBuild_CacheDir_PlatformSection(t *testing.T) {
	f := newFixture(t)
	section := "unix"
	if runtime.GOOS == "windows" {
		section = "windows"
	}
	f.write(t, filepath.Join("P", "BUFA"), "["+section+".deps]\ncacheDir = true\n"+bufaToml(winCacheDirScript, unixCacheDirScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	if got := strings.TrimSpace(f.readOut(t, b.Build("P"), "def.txt")); got != "set" {
		t.Errorf("a platform-section request must export the var, got %q", got)
	}
}

// The provider pattern: the owner publishes its entry name under an unexpanded BUFA_CACHE_ROOT; a
// consumer that never requested a cache dir writes through it — the directory exists because the
// owner was built first — and the published file holds no build-root path.
func TestBuild_CacheDir_ProviderConsumerSharing(t *testing.T) {
	winProvider := "@echo off\r\necho set CACHE=%%BUFA_CACHE_ROOT%%\\%BUFA_CACHE_DIR%>env.cmd\r\n"
	unixProvider := "printf 'CACHE=\"$BUFA_CACHE_ROOT/%s\"\\n' \"$BUFA_CACHE_DIR\" > env.sh\n"
	// Deps stage root-relative, so from the consumer's dir the provider is a sibling.
	winConsumer := "@echo off\r\ncall ..\\Provider\\env.cmd\r\nif not defined CACHE exit /b 1\r\n" +
		"echo shared>\"%CACHE%\\shared.txt\"\r\necho %CACHE%>seen.txt\r\n"
	unixConsumer := "set -e\n. ../Provider/env.sh\necho shared > \"$CACHE/shared.txt\"\nprintf '%s' \"$CACHE\" > seen.txt\n"

	f := newFixture(t)
	f.write(t, filepath.Join("Provider", "BUFA"), "deps.cacheDir = true\n"+bufaToml(winProvider, unixProvider))
	f.write(t, filepath.Join("Consumer", "BUFA"), "deps.bld = ['/Provider']\n"+bufaToml(winConsumer, unixConsumer))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("Consumer")
	seen := strings.TrimSpace(f.readOut(t, key, "seen.txt"))
	if want := f.userEntry(t, "Provider"); seen != want {
		t.Errorf("consumer used %q, want the provider's cache dir %q", seen, want)
	}
	if !osExists(filepath.Join(seen, "shared.txt")) {
		t.Error("consumer's write into the provider's cache dir must land")
	}
	if f.exists(filepath.Join(Store.UserRoot, "∕Consumer")) {
		t.Error("a consumer of a published path requests no entry of its own")
	}
	envFile := "env.sh"
	if runtime.GOOS == "windows" {
		envFile = "env.cmd"
	}
	if env := f.readOut(t, b.Build("Provider"), envFile); strings.Contains(env, f.bld) {
		t.Errorf("published %s must not bake the build root:\n%s", envFile, env)
	}
}
