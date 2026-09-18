package Build

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

var includeAll = FilterFiles.Compile([]string{"+**"})

func writeFile(t *testing.T, fsys vfs.Fs, path string, data []byte) {
	t.Helper()
	c.Check(fsys.MkdirAll(filepath.Dir(path), 0o755))
	c.Check(vfs.WriteFile(fsys, path, data, 0o644))
}

func osSymlink(target, link string) {
	c.Check(os.MkdirAll(filepath.Dir(link), 0o755))
	c.Check(os.Symlink(target, link))
}

func osExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want it to contain %q", err, want)
	}
}

// Probes inside tmp/, which every user wipes before use, so it never shows up as a foreign top-level entry.
func skipIfNoSymlinks(t *testing.T, s *Store.Store) {
	t.Helper()
	if err := c.Rescue(func() { s.Link(Store.TmpRoot, "probe", "x") }); err != nil {
		t.Skipf("symlinks unsupported here (Windows Developer Mode off?): %v", err)
	}
}

type capHandler struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (h capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h capHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	*h.msgs = append(*h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h capHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h capHandler) WithGroup(string) slog.Handler      { return h }

func captureLogs(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	msgs := &[]string{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capHandler{&mu, msgs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return msgs
}

func count(msgs *[]string, msg string) int {
	n := 0
	for _, m := range *msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// Test-binary only: lets test scripts see the fixture's vars through the safe-env filter.
func init() {
	safeInheritedEnv.Add(foldEnvName("BUFA_TEST_COUNTER"))
	safeInheritedEnv.Add(foldEnvName("BUFA_TEST_LINK_TARGET"))
}

// The counter file records one line per script run.
const winScript = "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\ncopy own.txt out.txt >nul\r\n"
const unixScript = "echo x >> \"$BUFA_TEST_COUNTER\"\ncp own.txt out.txt\n"

const winFail = "@echo off\r\nexit /b 1\r\n"
const unixFail = "exit 1\n"

const winEcho = "@echo off\r\necho BUFA_MARKER\r\n"
const unixEcho = "echo BUFA_MARKER\n"
const winEchoFail = "@echo off\r\necho BUFA_MARKER\r\nexit /b 1\r\n"
const unixEchoFail = "echo BUFA_MARKER\nexit 1\n"

func bufaToml(winCmd, unixCmd string) string {
	return "[windows]\ncmd = '''" + winCmd + "'''\n[unix]\ncmd = '''" + unixCmd + "'''\n"
}

func byOS(win, unix string) string {
	if runtime.GOOS == "windows" {
		return win
	}
	return unix
}

func buildScriptFile() string {
	return byOS("BUFA.cmd", "BUFA.bash")
}

type fixture struct {
	src, bld string // absolute OS roots
	srcFS    vfs.Fs
	counter  string // the scripts' run counter, beside (not inside) the build root
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	bld := filepath.Join(base, "bld")
	c.Check(os.MkdirAll(src, 0o755))
	c.Check(os.MkdirAll(bld, 0o755))
	srcFS := vfs.NewBasePathFs(vfs.NewOsFs(), src)
	writeFile(t, srcFS, Store.SrcRootFileName, nil)
	counter := filepath.Join(base, "count.txt")
	t.Setenv("BUFA_TEST_COUNTER", counter)
	return &fixture{src: src, bld: bld, srcFS: srcFS, counter: counter}
}

func (f *fixture) write(t *testing.T, rel, data string) {
	t.Helper()
	writeFile(t, f.srcFS, rel, []byte(data))
}

func (f *fixture) unit(t *testing.T, rel, ownContent string) {
	t.Helper()
	f.write(t, filepath.Join(rel, "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join(rel, "own.txt"), ownContent)
}

func (f *fixture) builder() *Builder {
	bldFS := vfs.NewBasePathFs(vfs.NewOsFs(), f.bld)
	return NewBuilder(Runtime.NewTest(f.srcFS, bldFS, f.src, f.bld, io.Discard, true))
}

func (f *fixture) countRuns(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(f.counter)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "x")
}

func (f *fixture) exists(rel string) bool {
	return osExists(filepath.Join(f.bld, filepath.FromSlash(rel)))
}

// A session keeps its scratch for inspection: tmp/<script> always, plus the sandbox files named.
func (f *fixture) requireKeptForInspection(t *testing.T, sandboxFiles ...string) {
	t.Helper()
	for _, rel := range append([]string{"tmp/" + buildScriptFile()}, sandboxFiles...) {
		if !f.exists(rel) {
			t.Errorf("%s must stay for inspection after the session", rel)
		}
	}
}

func (f *fixture) requireStillCached(t *testing.T, dir string, wantRuns int) {
	t.Helper()
	var buf bytes.Buffer
	b := f.builder()
	b.Out = &buf
	b.Build(dir)
	if r := f.countRuns(t); r != wantRuns || !strings.Contains(buf.String(), "Already built: "+dir) {
		t.Errorf("the cached result must survive the session: %d runs (want %d), out:\n%s", r, wantRuns, buf.String())
	}
}

func (f *fixture) readOut(t *testing.T, key, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.bld, "out", key, name))
	if err != nil {
		t.Fatalf("read published %s: %v", name, err)
	}
	return string(data)
}

func (f *fixture) outDirs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(f.bld, "out"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "D") {
			n++
		}
	}
	return n
}

func TestSrcPrep_IdxHitSkipsStaging(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	msgs := captureLogs(t)

	k1 := b.store.SrcPrep(b.SrcFS, "U", includeAll, true)
	k2 := b.store.SrcPrep(b.SrcFS, "U", includeAll, true)
	if k1 != k2 {
		t.Fatalf("same source, different keys: %q vs %q", k1, k2)
	}
	if count(msgs, "SrcPrep cache hit") != 1 {
		t.Errorf("want exactly 1 cache hit, got %d (%v)", count(msgs, "SrcPrep cache hit"), *msgs)
	}
	if got := b.store.GetLinkTarget(Store.InRoot, "∕U"); got != k1 {
		t.Errorf("in P-link = %q, want %q", got, k1)
	}

	f.write(t, filepath.Join("U", "own.txt"), "v2-changed")
	k3 := b.store.SrcPrep(b.SrcFS, "U", includeAll, true)
	if k3 == k1 {
		t.Errorf("mutated source must yield a new key, still %q", k3)
	}
	if got := b.store.GetLinkTarget(Store.InRoot, "∕U"); got != k3 {
		t.Errorf("in P-link after mutation = %q, want %q", got, k3)
	}
}

func TestSrcPrep_ExcludesNestedUnit(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "own.txt"), "OWN")
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "nested", "BUFA"), "[unix]")
	f.write(t, filepath.Join("U", "nested", "gen.txt"), "NESTED")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	key := b.store.SrcPrep(b.SrcFS, "U", includeAll, true)

	root := filepath.Join(f.bld, "in", key)
	for _, p := range []string{"own.txt", "BUFA"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("prepared source missing own %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "nested")); err == nil {
		t.Error("prepared source must exclude the nested unit")
	}
}

func TestBuild_DeepSrcDirPath(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("a", "b", "U"), "deep")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build(filepath.ToSlash(filepath.Join("a", "b", "U")))
	if !strings.HasPrefix(key, "D") {
		t.Errorf("output key must be a content hash, got %q", key)
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("output not published to out store")
	}
	if f.exists("out/" + key + "/" + buildScriptFile()) {
		t.Error("generated build script must not be published as output")
	}
	if f.exists("tmp") {
		t.Error("tmp/ with the generated build script must be deleted after successful build")
	}
	if f.exists("bld/a/b/U") {
		t.Error("sandbox must be deleted on success")
	}
}

func TestBuild_OutCacheHitSkipsScript(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	msgs := captureLogs(t)

	k1 := b.Build("U")
	k2 := b.Build("U")

	if k1 != k2 {
		t.Fatalf("unchanged unit produced different output keys: %q vs %q", k1, k2)
	}
	if r := f.countRuns(t); r != 1 {
		t.Errorf("script ran %d times across two builds, want 1", r)
	}
	if c := count(msgs, "Build hash:"); c != 1 {
		t.Errorf("Build hash ×%d, want 1", c)
	}
	if c := count(msgs, "Cache 'Local BuildHash' hit:"); c != 1 {
		t.Errorf("local build cache hit ×%d, want 1", c)
	}
	if f.exists("bld/U") {
		t.Error("sandbox must be deleted on success")
	}
}

// The dir's own path is a combined term: a moved dir re-runs its script (its sandbox path and every
// path-derived env var changed) while the identical output still dedups to one D.
func TestBuild_RenamedDirRerunsScript(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src")
	skipIfNoSymlinks(t, f.builder().store)

	k1 := f.builder().Build("U")
	c.Check(f.srcFS.Rename("U", "V"))
	k2 := f.builder().Build("V")

	if r := f.countRuns(t); r != 2 {
		t.Errorf("script ran %d times across the rename, want 2 (own path must be a cache-key term)", r)
	}
	if k1 != k2 {
		t.Errorf("identical output at a new path must dedup to the same D: %q vs %q", k1, k2)
	}
	if n := f.outDirs(t); n != 1 {
		t.Errorf("out/ D dirs = %d, want 1", n)
	}
}

// Regression: after a content roundtrip A→B→A the third build is an out-cache hit served without
// storeSetBuildHash, so build() itself must refresh the out/∕ GC root or GC reclaims A's entries.
func TestBuild_RoundtripThenGCNoRebuild(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "u", "A")
	skipIfNoSymlinks(t, f.builder().store)

	f.builder().Build("u") // build A (realBuild)
	f.unit(t, "u", "B")
	f.builder().Build("u") // build B (realBuild)
	f.unit(t, "u", "A")
	f.builder().Build("u") // revert: out-cache hit, must refresh out/∕u -> B<combined(A)>
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("after roundtrip: script ran %d times, want 2", r)
	}

	store := Store.NewStore(vfs.NewBasePathFs(vfs.NewOsFs(), f.bld))
	store.GC(func(path string) bool { return Store.BuildDirPresent(f.srcFS, path) }, false)

	f.builder().Build("u") // post-GC: must stay a cache hit (A's entries survived GC)
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("post-GC build re-ran script: %d runs, want 2 (out/∕ GC-root lag regression)", r)
	}
}

func TestBuild_LocalCacheLivesOnBuilder(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	k1 := b.Build("U")
	runsAfter1 := f.countRuns(t)

	msgs := captureLogs(t)
	k2 := b.Build("U")

	if k2 != k1 {
		t.Fatalf("local cache changed build hash: %q -> %q", k1, k2)
	}
	if r := f.countRuns(t); r != runsAfter1 {
		t.Errorf("Builder-local cache should skip script on second Build: %d -> %d", runsAfter1, r)
	}
	if c := count(msgs, "Cache 'Local BuildHash' hit:"); c != 1 {
		t.Errorf("local build cache hit ×%d, want 1", c)
	}
}

func TestBuild_UnsafeTTLRebuildsWhenBucketChanges(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[unsafe]\nttl = \"1h\"\n")
	f.write(t, filepath.Join("U", "BUFA"), "unsafe = true\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "src")

	b1 := f.builder()
	b1.BuildStartTimeUTC = time.Unix(10*60, 0)
	skipIfNoSymlinks(t, b1.store)
	b1.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("first unsafe build ran script %d times, want 1", r)
	}

	b2 := f.builder()
	b2.BuildStartTimeUTC = time.Unix(30*60, 0)
	b2.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("same unsafe TTL bucket must hit the store cache: %d runs, want 1", r)
	}

	b2.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("same unsafe TTL bucket must hit the local cache: %d runs, want 1", r)
	}

	b3 := f.builder()
	b3.BuildStartTimeUTC = time.Unix(61*60, 0)
	b3.Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("new unsafe TTL bucket must force a rebuild: %d runs, want 2", r)
	}
}

func TestBuild_UnsafeZeroTTLUsesBuildStartTimeBucket(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[unsafe]\nttl = \"0s\"\n")
	f.write(t, filepath.Join("U", "BUFA"), "unsafe = true\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "src")

	b1 := f.builder()
	b1.BuildStartTimeUTC = time.Unix(1, 123_000_100)
	skipIfNoSymlinks(t, b1.store)
	b1.Build("U")
	b1.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("same builder must hit local cache with zero TTL: %d runs, want 1", r)
	}

	b2 := f.builder()
	b2.BuildStartTimeUTC = time.Unix(1, 123_000_999)
	b2.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("same millisecond BuildStartTime bucket must hit store cache with zero TTL: %d runs, want 1", r)
	}

	b3 := f.builder()
	b3.BuildStartTimeUTC = time.Unix(1, 124_000_000)
	b3.Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("new millisecond BuildStartTime bucket must rebuild with zero TTL: %d runs, want 2", r)
	}
}

func TestBuild_UnsafeUsesDefaultRootTTL(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "")
	f.write(t, filepath.Join("U", "BUFA"), "unsafe = true\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "src")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	b.Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("unsafe build with default TTL ran script %d times, want 1", r)
	}
}

func TestBuild_LocalCacheDoesNotCrossBuilders(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src")

	b1 := f.builder()
	skipIfNoSymlinks(t, b1.store)
	k1 := b1.Build("U")

	b2 := f.builder()
	msgs := captureLogs(t)
	k2 := b2.Build("U")

	if k2 != k1 {
		t.Fatalf("store cache changed build hash across Builders: %q -> %q", k1, k2)
	}
	if c := count(msgs, "Cache 'Local BuildHash' hit:"); c != 0 {
		t.Errorf("Builder-local cache crossed Builder instances: local hit ×%d, want 0", c)
	}
	if c := count(msgs, "Combined inputs hash:"); c != 1 {
		t.Errorf("second Builder should enter build path once, got %d", c)
	}
}

func TestBuild_LocalCacheSkipsSharedBldDep(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Shared", "shared")
	f.write(t, filepath.Join("Left", "BUFA"),
		"[deps]\nbld = [\"../Shared\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Left", "own.txt"), "left")
	f.write(t, filepath.Join("Right", "BUFA"),
		"[deps]\nbld = [\"../Shared\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Right", "own.txt"), "right")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Left\", \"../Right\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	msgs := captureLogs(t)

	b.Build("Parent")

	if r := f.countRuns(t); r != 4 {
		t.Errorf("Shared, Left, Right, Parent scripts should each run once; got %d runs", r)
	}
	if c := count(msgs, "Cache 'Local BuildHash' hit:"); c != 1 {
		t.Errorf("shared bld dep local-cache hit ×%d, want 1", c)
	}
	if c := count(msgs, "Combined inputs hash:"); c != 4 {
		t.Errorf("build path entries ×%d, want 4 (Shared should enter once)", c)
	}
}

func TestBuild_RecursiveBldDep(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "dep-v1")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	p1 := b.Build("Parent")
	if f.outDirs(t) < 2 {
		t.Errorf("want >=2 out content entries (Dep+Parent), got %d", f.outDirs(t))
	}
	runsAfter1 := f.countRuns(t) // Dep + Parent both ran

	if p2 := b.Build("Parent"); p2 != p1 {
		t.Errorf("unchanged rebuild changed parent key: %q -> %q", p1, p2)
	}
	if r := f.countRuns(t); r != runsAfter1 {
		t.Errorf("cached rebuild ran scripts again: %d -> %d", runsAfter1, r)
	}

	f.write(t, filepath.Join("Dep", "own.txt"), "dep-v2")
	msgs := captureLogs(t)
	if p3 := b.Build("Parent"); p3 != p1 {
		t.Errorf("Builder-local cached parent key changed: %q -> %q", p1, p3)
	}
	if r := f.countRuns(t); r != runsAfter1 {
		t.Errorf("trusted local cache should not re-run scripts: %d -> %d", runsAfter1, r)
	}
	if c := count(msgs, "Build hash:"); c != 0 {
		t.Errorf("trusted local cache should skip stores, got %d", c)
	}
}

func TestBuild_CircularBldDepPanics(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("A", "BUFA"),
		"[deps]\nbld = [\"../B\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("B", "BUFA"),
		"[deps]\nbld = [\"../C\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("C", "BUFA"),
		"[deps]\nbld = [\"../A\"]\n"+bufaToml(winScript, unixScript))
	b := f.builder()

	err := c.Rescue(func() { b.Build("A") })
	if err == nil {
		t.Fatal("circular bld deps must panic")
	}
	if msg := strings.ToLower(err.Error()); !strings.Contains(msg, "circular") || !strings.Contains(msg, "a") {
		t.Fatalf("panic %q should mention circular dependency involving A", err)
	}
	if len(b.buildingNow) != 0 {
		t.Fatalf("building set not cleared after panic: %#v", b.buildingNow)
	}
	if r := f.countRuns(t); r != 0 {
		t.Fatalf("cycle detection should happen before scripts run; got %d runs", r)
	}
}

func TestBuild_KeepsBldOnFailure(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winFail, unixFail))
	f.write(t, filepath.Join("U", "own.txt"), "x")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil {
		t.Fatal("failing script must panic out of Build")
	}
	if _, ok := b.localBuildCache.Get("U"); ok {
		t.Fatal("failing script must not populate Builder-local build cache")
	}
	script := buildScriptFile()
	if !f.exists("tmp/" + script) {
		t.Error("generated build script must be kept in tmp/ on failure for debugging")
	}
	if f.exists("bld/U/" + script) {
		t.Error("generated build script must not be written into the unit build dir")
	}
	if !f.exists("bld/U") {
		t.Error("sandbox must be kept on failure for debugging")
	}
	if d := f.outDirs(t); d != 0 {
		t.Errorf("failed build must not publish output, got %d out dirs", d)
	}
}

func TestBuild_ScriptOutputHiddenOnSuccess(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winEcho, unixEcho))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	var buf bytes.Buffer
	b.Out = &buf
	b.Build("U")
	if strings.Contains(buf.String(), "BUFA_MARKER") {
		t.Errorf("successful build must not surface script output, got:\n%s", buf.String())
	}
}

func TestBuild_ScriptOutputPrintedOnFailure(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winEchoFail, unixEchoFail))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	var buf bytes.Buffer
	b.Out = &buf
	if err := c.Rescue(func() { b.Build("U") }); err == nil {
		t.Fatal("failing script must panic out of Build")
	}
	if !strings.Contains(buf.String(), "BUFA_MARKER") {
		t.Errorf("failed build must surface script output, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Build FAILED (exit code 1): U") {
		t.Errorf("failure footer must carry the exit code, got:\n%s", buf.String())
	}
}

func echoToml(marker string) string {
	return bufaToml("@echo off\r\necho "+marker+"\r\n", "echo "+marker+"\n")
}

func TestBuild_ShowOutputAll(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winEcho, unixEcho))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	var buf bytes.Buffer
	b.Out = &buf
	b.ShowOutput = Runtime.ScopeAll
	b.Build("U")
	out := buf.String()
	if !strings.Contains(out, "BUFA_MARKER") {
		t.Errorf("--build-output-all must surface successful script output, got:\n%s", out)
	}
	if !strings.Contains(out, "Build Start") || !strings.Contains(out, "Build End") {
		t.Errorf("shown output must be framed with delimiter lines, got:\n%s", out)
	}
}

func TestBuild_ShowOutputTargetOnlyTargetDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("D", "BUFA"), echoToml("MARKER_DEP"))
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"../D\"]\n"+echoToml("MARKER_TARGET"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	var buf bytes.Buffer
	b.Out = &buf
	b.ShowOutput = Runtime.ScopeTarget
	b.TargetDirs = []string{"U"} // CLI normally sets this; no CLI here
	b.Build("U")
	out := buf.String()
	if !strings.Contains(out, "MARKER_TARGET") {
		t.Errorf("--build-output must surface the target dir's output, got:\n%s", out)
	}
	if strings.Contains(out, "MARKER_DEP") {
		t.Errorf("--build-output must not surface a dep's output, got:\n%s", out)
	}
}

// Guards double-printing: streamed output must not be dumped again by the failure path.
func TestBuild_ShowOutputAllFailureStreamsOnce(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winEchoFail, unixEchoFail))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	var buf bytes.Buffer
	b.Out = &buf
	b.ShowOutput = Runtime.ScopeAll
	if err := c.Rescue(func() { b.Build("U") }); err == nil {
		t.Fatal("failing script must panic out of Build")
	}
	out := buf.String()
	if got := strings.Count(out, "BUFA_MARKER"); got != 1 {
		t.Errorf("streamed output printed %d times, want 1:\n%s", got, out)
	}
	if got := strings.Count(out, "Build Start"); got != 1 {
		t.Errorf("frame opened %d times, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, "Build FAILED (exit code 1): U") {
		t.Errorf("failure footer must carry the exit code, got:\n%s", out)
	}
}

func TestBuild_ShowOutputStreamsToFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winEcho, unixEcho))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	outFile, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	b.Out = outFile
	b.ShowOutput = Runtime.ScopeAll
	b.Build("U")
	data, err := os.ReadFile(outFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	iStart := strings.Index(out, "Build Start: U")
	iMark := strings.Index(out, "BUFA_MARKER")
	iEnd := strings.Index(out, "Build End: U")
	if iStart < 0 || iMark < 0 || iEnd < 0 || iMark < iStart || iEnd < iMark {
		t.Errorf("fd passthrough must write header, script output, footer in order, got:\n%s", out)
	}
	if !strings.Contains(out, "\n-----   Build End: U") {
		t.Errorf("footer must start on its own line even though bufa cannot see the child's bytes, got:\n%s", out)
	}
}

// Several command-line targets on one builder: --force re-runs each of them, and the local caches
// carry across targets so their shared dep builds once.
func TestBuild_ForceTargetAppliesToEveryTarget(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Shared", "shared")
	f.write(t, filepath.Join("Left", "BUFA"),
		"[deps]\nbld = [\"../Shared\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Left", "own.txt"), "left")
	f.write(t, filepath.Join("Right", "BUFA"),
		"[deps]\nbld = [\"../Shared\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Right", "own.txt"), "right")
	skipIfNoSymlinks(t, f.builder().store)

	b := f.builder()
	b.Build("Left")
	b.Build("Right")
	if r := f.countRuns(t); r != 3 {
		t.Fatalf("Shared, Left, Right should each run once, got %d", r)
	}

	b = f.builder()
	b.ForceRebuild = Runtime.ScopeTarget
	b.TargetDirs = []string{"Left", "Right"} // CLI normally sets this; no CLI here
	msgs := captureLogs(t)
	b.Build("Left")
	b.Build("Right")
	if r := f.countRuns(t); r != 5 {
		t.Errorf("--force must re-run both targets and neither Shared build: %d runs, want 5", r)
	}
	if c := count(msgs, "Cache 'Local BuildHash' hit:"); c != 1 {
		t.Errorf("Shared local-cache hit ×%d, want 1 (the second target must reuse the first's local caches)", c)
	}
}

func TestBuild_ForceRebuildTargetOnly(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "dep")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")
	skipIfNoSymlinks(t, f.builder().store)

	k1 := f.builder().Build("Parent")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("Dep + Parent should each run once, got %d", r)
	}

	b := f.builder()
	b.ForceRebuild = Runtime.ScopeTarget
	b.TargetDirs = []string{"Parent"} // CLI normally sets this; no CLI here
	var buf bytes.Buffer
	b.Out = &buf
	if k2 := b.Build("Parent"); k2 != k1 {
		t.Errorf("deterministic forced rebuild changed the key: %q -> %q", k1, k2)
	}
	if r := f.countRuns(t); r != 3 {
		t.Errorf("--force must re-run the target only: %d runs, want 3", r)
	}
	if out := buf.String(); !strings.Contains(out, "Already built: Dep") || strings.Contains(out, "Already built: Parent") {
		t.Errorf("--force must report the dep as cached and the target as built, got:\n%s", out)
	}
	if out := buf.String(); strings.Contains(out, "not reproducible") {
		t.Errorf("a reproducible forced rebuild must not warn, got:\n%s", out)
	}

	f.builder().Build("Parent")
	if r := f.countRuns(t); r != 3 {
		t.Errorf("plain build after a forced one must hit the store: %d runs, want 3", r)
	}
}

func TestBuild_ForceRebuildAllRebuildsEveryDirOnce(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Shared", "shared")
	f.write(t, filepath.Join("Left", "BUFA"),
		"[deps]\nbld = [\"../Shared\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Left", "own.txt"), "left")
	f.write(t, filepath.Join("Right", "BUFA"),
		"[deps]\nbld = [\"../Shared\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Right", "own.txt"), "right")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Left\", \"../Right\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")
	skipIfNoSymlinks(t, f.builder().store)

	f.builder().Build("Parent")
	if r := f.countRuns(t); r != 4 {
		t.Fatalf("Shared, Left, Right, Parent should each run once, got %d", r)
	}

	b := f.builder()
	b.ForceRebuild = Runtime.ScopeAll
	var buf bytes.Buffer
	b.Out = &buf
	b.Build("Parent")
	if r := f.countRuns(t); r != 8 {
		t.Errorf("--force-all must re-run every dir exactly once (Shared via the local cache): %d runs, want 8", r)
	}
	if out := buf.String(); strings.Contains(out, "Already built:") {
		t.Errorf("--force-all must not report any dir as cached, got:\n%s", out)
	}
}

// A forced rebuild whose output differs must re-point B<combined>, or the next plain build would
// serve the superseded tree while the forced one reported the new — and must say so loudly.
func TestBuild_ForceRebuildRepointsChangedOutput(t *testing.T) {
	f := newFixture(t)
	// The output embeds the run counter, so every run publishes a different tree.
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(
		"@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\ncopy \"%BUFA_TEST_COUNTER%\" out.txt >nul\r\n",
		"echo x >> \"$BUFA_TEST_COUNTER\"\ncp \"$BUFA_TEST_COUNTER\" out.txt\n"))
	skipIfNoSymlinks(t, f.builder().store)

	k1 := f.builder().Build("U")

	b := f.builder()
	b.ForceRebuild = Runtime.ScopeAll
	var buf bytes.Buffer
	b.Out = &buf
	k2 := b.Build("U")
	if k2 == k1 {
		t.Fatalf("counter-embedding script must publish a different tree per run, still %q", k2)
	}
	if got := f.readOut(t, k2, "out.txt"); strings.Count(got, "x") != 2 {
		t.Errorf("forced output = %q, want the two-run counter", got)
	}
	if out := buf.String(); !strings.Contains(out, "WARNING: Dir 'U' build is not reproducible") ||
		!strings.Contains(out, k1) || !strings.Contains(out, k2) {
		t.Errorf("changed output under unchanged inputs must be reported with both hashes, got:\n%s", out)
	}

	if k3 := f.builder().Build("U"); k3 != k2 {
		t.Errorf("plain build after a forced one = %q, want the forced result %q (B<combined> not re-pointed)", k3, k2)
	}
	if r := f.countRuns(t); r != 2 {
		t.Errorf("plain build after a forced one must be a store hit: %d runs, want 2", r)
	}
}

func TestBuild_RootDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, "BUFA", bufaToml(winScript, unixScript))
	f.write(t, "own.txt", "root")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build(".")
	if !strings.HasPrefix(key, "D") {
		t.Errorf("output key must be content hash, got %q", key)
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("root build output not published")
	}
	if f.exists("out/" + key + "/" + buildScriptFile()) {
		t.Error("generated build script must not be published for root builds")
	}
	if f.exists("tmp") {
		t.Error("tmp/ with the generated build script must be deleted after successful root build")
	}
	if f.exists("bld/bld") || f.exists("bld") && f.outDirs(t) == 0 {
		t.Error("sandbox must be deleted on success")
	}
}

func TestBuild_SrcDep(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Dep", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Dep", "data.txt"), "dep-v1")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nsrc = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "parent")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	msgs := captureLogs(t)

	p1 := b.Build("Parent")
	if c := count(msgs, "Dep hash:"); c != 1 {
		t.Errorf("Dep hash logged %d times, want 1", c)
	}
	runs1 := f.countRuns(t)

	if p2 := b.Build("Parent"); p2 != p1 {
		t.Errorf("unchanged rebuild: %q -> %q", p1, p2)
	}
	if r := f.countRuns(t); r != runs1 {
		t.Errorf("cached rebuild ran script: %d -> %d", runs1, r)
	}

	f.write(t, filepath.Join("Dep", "data.txt"), "dep-v2")
	if p3 := b.Build("Parent"); p3 != p1 {
		t.Errorf("Builder-local cached parent key changed after src dep mutation: %q -> %q", p1, p3)
	}
	if r := f.countRuns(t); r != runs1 {
		t.Errorf("trusted local cache should not re-run script: %d -> %d", runs1, r)
	}
}

// Regression: restoring the deeper own source before its ancestor dep hits a non-empty target.
func TestBuild_SrcDepOnParentDir(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "a", "ancestor") // ancestor is itself a build dir (src deps need BUFA)
	f.write(t, filepath.Join("a", "b", "U", "BUFA"),
		"[deps]\nsrc = [\"/a\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("a", "b", "U", "own.txt"), "leaf")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build(filepath.ToSlash(filepath.Join("a", "b", "U")))
	if !strings.HasPrefix(key, "D") {
		t.Errorf("output key must be a content hash, got %q", key)
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("output not published")
	}
}

func TestBuild_NestedSrcDepsOrderIndependent(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "a", "ancestor")
	f.unit(t, filepath.Join("a", "b", "U"), "leaf")
	f.write(t, filepath.Join("Top", "BUFA"),
		"[deps]\nsrc = [\"/a/b/U\", \"/a\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Top", "own.txt"), "top")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("Top")
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("output not published")
	}
}

// Regression: "." must rank below every top-level dir, not merely tie-break by insertion order.
func TestBuild_SrcDepOnRoot(t *testing.T) {
	f := newFixture(t)
	f.write(t, "BUFA", bufaToml(winScript, unixScript)) // root is itself a build dir
	f.write(t, "own.txt", "root")
	f.write(t, filepath.Join("test", "BUFA"),
		"[deps]\nsrc = [\"/\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("test", "own.txt"), "leaf")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("test")
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("output not published")
	}
}

func TestBuild_NestedBldDepNotPublished(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "tool"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if f.exists("out/" + key + "/tool") {
		t.Error("staged nested bld dep must not ride into the consumer's output")
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("consumer output missing")
	}
}

func TestBuild_NestedSrcDepNotPublished(t *testing.T) {
	f := newFixture(t)
	// src deps must carry a BUFA too
	f.write(t, filepath.Join("U", "data", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "data", "data.txt"), "payload")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nsrc = [\"data\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if f.exists("out/" + key + "/data") {
		t.Error("staged nested src dep must not ride into the consumer's output")
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("consumer output missing")
	}
}

func TestBuild_RootBuildDepsNotPublished(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "tool", "tool")
	f.write(t, "BUFA", "[deps]\nbld = [\"tool\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, "own.txt", "root")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build(".")
	if f.exists("out/" + key + "/tool") {
		t.Error("root build must not publish its staged deps")
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("root build output missing")
	}
}

// Regression: pruning a/b must drop the staging-created a/ too, not publish a hollow shell.
func TestBuild_NestedDepEmptyAncestorNotPublished(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "a", "b"), "dep")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"a/b\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if f.exists("out/" + key + "/a") {
		t.Error("ancestor dir created only for dep staging must not publish")
	}
}

func TestBuild_NestedDepAncestorWithContentSurvives(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "a", "b"), "dep")
	f.write(t, filepath.Join("U", "a", "keep.txt"), "keep")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"a/b\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if !f.exists("out/" + key + "/a/keep.txt") {
		t.Error("own source under the dep's ancestor must survive")
	}
	if f.exists("out/" + key + "/a/b") {
		t.Error("staged nested dep must not publish")
	}
}

func TestBuild_FailureKeepsStagedNestedDep(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "tool"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\n"+bufaToml(winFail, unixFail))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	if err := c.Rescue(func() { b.Build("U") }); err == nil {
		t.Fatal("failing script must panic out of Build")
	}
	if !f.exists("bld/U/tool") {
		t.Error("failed build must keep the staged nested dep for debugging")
	}
}

func TestBuild_ScriptConsumesNestedDep(t *testing.T) {
	f := newFixture(t)
	winConsume := "@echo off\r\n%BUFA_COPY_OR_MOVE% tool\\out.txt got.txt >nul\r\nif errorlevel 1 exit /b 1\r\n"
	unixConsume := "$BUFA_COPY_OR_MOVE tool/out.txt got.txt\n"
	f.unit(t, filepath.Join("U", "tool"), "tool-src")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\n"+bufaToml(winConsume, unixConsume))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if !f.exists("out/" + key + "/got.txt") {
		t.Error("file moved out of the staged dep is script output and must publish")
	}
	if f.exists("out/" + key + "/tool") {
		t.Error("the consumed dep dir must still be pruned")
	}
}

func TestBuild_ExportedNestedBldDepPublished(t *testing.T) {
	f := newFixture(t)
	winAdd := "@echo off\r\necho added>tool\\added.txt\r\ncopy own.txt out.txt >nul\r\n"
	unixAdd := "echo added > tool/added.txt\ncp own.txt out.txt\n"
	f.unit(t, filepath.Join("U", "tool"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\nexport = [\"tool\"]\n"+bufaToml(winAdd, unixAdd))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if !f.exists("out/" + key + "/tool/out.txt") {
		t.Error("exported dep's staged output must publish")
	}
	if !f.exists("out/" + key + "/tool/added.txt") {
		t.Error("script output written into the exported dep must publish")
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("consumer output missing")
	}
}

func TestBuild_ExportedNestedSrcDepPublished(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "data", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "data", "data.txt"), "payload")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nsrc = [\"data\"]\nexport = [\"/U/data\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if !f.exists("out/" + key + "/data/data.txt") {
		t.Error("exported src dep must publish")
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("consumer output missing")
	}
}

func TestBuild_ExportExemptsOnlyNamedDep(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "a"), "a")
	f.unit(t, filepath.Join("U", "b"), "b")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"a\", \"b\"]\nexport = [\"a\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "consumer")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if !f.exists("out/" + key + "/a/out.txt") {
		t.Error("exported dep must publish")
	}
	if f.exists("out/" + key + "/b") {
		t.Error("a dep not named in export must still be pruned")
	}
}

func TestBuild_ExportRejectsNonDep(t *testing.T) {
	f := newFixture(t)
	f.unit(t, filepath.Join("U", "tool"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\nexport = [\"other\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "deps.export 'other' in 'U' is not a deps.bld, deps.src, or deps.ext dependency")
}

func TestBuild_ExportRejectsNonNestedDep(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "tool", "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"/tool\"]\nexport = [\"/tool\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "deps.export '/tool' in 'U' must be a dependency nested under the build dir")
}

func TestBuild_ExportRejectsLinkStagedDep(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "tool", "BUFA"), largeToml())
	f.write(t, filepath.Join("U", "tool", "own.txt"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\nexport = [\"tool\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	requireErrorContains(t, c.Rescue(func() { b.Build("U") }), "cannot be exported with the output of 'U'")
	if f.exists("bld/U") {
		t.Error("the export-link check must fire before the sandbox is staged")
	}
}

// Pins the pruneDirs own-"." exclusion: filepath.IsLocal(".") is true.
func TestBuild_VerbatimPublishKeepsEmptyDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml("@echo off\r\nmkdir empty\r\n", "mkdir empty\n"))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	key := b.Build("U")
	if !f.exists("out/" + key + "/empty") {
		t.Error("verbatim publish (no filter, no nested deps) must keep an empty output dir")
	}
}

func TestBuild_LocalCacheTrustedAcrossOwnSourceChange(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	k1 := b.Build("U")
	f.write(t, filepath.Join("U", "own.txt"), "v2")
	k2 := b.Build("U")
	if k2 != k1 {
		t.Errorf("trusted local cache should return same output key: %q -> %q", k1, k2)
	}
	if r := f.countRuns(t); r != 1 {
		t.Errorf("trusted local cache should not re-run script, got %d runs", r)
	}
}

func TestBuild_SrcFilterExcludesFromSource(t *testing.T) {
	f := newFixture(t)
	cfg := "[filters]\nsrc = [\"-*.md\"]\n" + bufaToml(winScript, unixScript)
	f.write(t, filepath.Join("U", "BUFA"), cfg)
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	f.write(t, filepath.Join("U", "README.md"), "doc-v1")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	k1 := b.Build("U")
	runs1 := f.countRuns(t)

	if f.exists("out/" + k1 + "/README.md") {
		t.Error("README.md must be excluded from the filtered source")
	}
	if !f.exists("out/" + k1 + "/own.txt") {
		t.Error("own.txt must survive the filter")
	}

	f.write(t, filepath.Join("U", "README.md"), "doc-v2-much-longer-content")
	if k2 := f.builder().Build("U"); k2 != k1 {
		t.Errorf("filtered README change altered build key: %q -> %q", k1, k2)
	}
	if r := f.countRuns(t); r != runs1 {
		t.Errorf("filtered README change re-ran the build: %d -> %d", runs1, r)
	}
}

func TestBuild_BldFilterRestrictsOutput(t *testing.T) {
	f := newFixture(t)
	cfg := "[filters]\nbld = [\"+/out.txt\"]\n" + bufaToml(winScript, unixScript)
	f.write(t, filepath.Join("U", "BUFA"), cfg)
	f.write(t, filepath.Join("U", "own.txt"), "v1")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	k := b.Build("U")

	if !f.exists("out/" + k + "/out.txt") {
		t.Error("bld filter dropped the included out.txt")
	}
	if f.exists("out/" + k + "/own.txt") {
		t.Error("bld filter must drop own.txt")
	}
	if f.exists("out/" + k + "/" + buildScriptFile()) {
		t.Error("generated build script must not be published")
	}

	runs := f.countRuns(t)
	if k2 := f.builder().Build("U"); k2 != k {
		t.Errorf("bld-filtered rebuild key changed: %q -> %q", k, k2)
	}
	if r := f.countRuns(t); r != runs {
		t.Errorf("bld-filtered rebuild re-ran the script: %d -> %d", runs, r)
	}
}

func TestBuild_BufaConfigNeverStaged(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1") // no [filters] at all
	cfg := "[filters]\nsrc = [\"+/BUFA\", \"+own.txt\"]\n" + bufaToml(winScript, unixScript)
	f.write(t, filepath.Join("V", "BUFA"), cfg)
	f.write(t, filepath.Join("V", "own.txt"), "v1")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	for _, dir := range []string{"U", "V"} {
		k := b.Build(dir)
		if f.exists("out/" + k + "/BUFA") {
			t.Errorf("%s: BUFA must never be staged into the source", dir)
		}
		if !f.exists("out/" + k + "/own.txt") {
			t.Errorf("%s: own.txt must survive", dir)
		}
	}
}

func TestBuild_RootMarkerNeverStaged(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1") // no [filters] at all
	f.write(t, filepath.Join("U", ".BUFA"), "")
	cfg := "[filters]\nsrc = [\"+/.BUFA\", \"+own.txt\"]\n" + bufaToml(winScript, unixScript)
	f.write(t, filepath.Join("V", "BUFA"), cfg)
	f.write(t, filepath.Join("V", "own.txt"), "v1")
	f.write(t, filepath.Join("V", ".BUFA"), "")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	for _, dir := range []string{"U", "V"} {
		k := b.Build(dir)
		if f.exists("out/" + k + "/.BUFA") {
			t.Errorf("%s: the .BUFA marker must never be staged into the source", dir)
		}
		if !f.exists("out/" + k + "/own.txt") {
			t.Errorf("%s: own.txt must survive", dir)
		}
	}
}

func TestBuild_HiddenFilesExcludedByDefault(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1") // no [filters]
	f.write(t, filepath.Join("U", ".secret"), "s")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	k := b.Build("U")
	if f.exists("out/" + k + "/.secret") {
		t.Error(".secret must be excluded by the default hidden-files rule")
	}
	if !f.exists("out/" + k + "/own.txt") {
		t.Error("own.txt must survive")
	}

	cfg := "[filters]\nsrc = [\"+**\"]\nbld = [\"+**\"]\n" + bufaToml(winScript, unixScript)
	f.write(t, filepath.Join("V", "BUFA"), cfg)
	f.write(t, filepath.Join("V", "own.txt"), "v1")
	f.write(t, filepath.Join("V", ".secret"), "s")
	kv := f.builder().Build("V")
	if !f.exists("out/" + kv + "/.secret") {
		t.Error("explicit +** on src and bld must re-include the hidden file")
	}
}

func TestBuild_ConfigChangeRebuildsWithoutRestaging(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	msgs := captureLogs(t)

	b.Build("U")
	runs1 := f.countRuns(t)

	f.write(t, filepath.Join("U", "BUFA"),
		bufaToml("rem v2\r\n"+winScript, "# v2\n"+unixScript))
	f.builder().Build("U") // fresh Builder — local caches empty

	if r := f.countRuns(t); r != runs1+1 {
		t.Errorf("config-only change must re-run the script: %d -> %d runs", runs1, r)
	}
	if c := count(msgs, "SrcPrep prepared"); c != 1 {
		t.Errorf("config-only change must not re-stage the source: %d stagings, want 1", c)
	}
	if c := count(msgs, "SrcPrep cache hit"); c != 1 {
		t.Errorf("second build should reuse the staged source: %d hits, want 1", c)
	}
}

const winPathScript = "@echo off\r\necho %PATH%>path.txt\r\n"
const unixPathScript = "printf '%s' \"$PATH\" > path.txt\n"

func pathScriptToml(unsafe bool) string {
	cfg := bufaToml(winPathScript, unixPathScript)
	if unsafe {
		cfg = "unsafe = true\n" + cfg
	}
	return cfg
}

func TestBuild_HermeticPath(t *testing.T) {
	sentinelDir := "/bufa-path-sentinel"
	sep := ":"
	if runtime.GOOS == "windows" {
		sentinelDir, sep = `C:\bufa-path-sentinel`, ";"
	}
	// The interpreter is absolute on Windows and found via the parent PATH on Unix, so cmd.Env never breaks the run.
	t.Setenv("PATH", sentinelDir+sep+os.Getenv("PATH"))

	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[unsafe]\nttl = \"1h\"\n")
	f.write(t, filepath.Join("Safe", "BUFA"), pathScriptToml(false))
	f.write(t, filepath.Join("Unsafe", "BUFA"), pathScriptToml(true))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	safe := f.readOut(t, b.Build("Safe"), "path.txt")
	if strings.Contains(safe, sentinelDir) {
		t.Errorf("safe build leaked inherited PATH: %q", safe)
	}
	if want := cleanSystemPath(); !strings.HasPrefix(strings.TrimSpace(safe), want) {
		t.Errorf("safe build PATH = %q, want clean default %q", safe, want)
	}

	unsafe := f.readOut(t, b.Build("Unsafe"), "path.txt")
	if !strings.Contains(unsafe, sentinelDir) {
		t.Errorf("unsafe build must keep inherited PATH, got %q", unsafe)
	}
}

func TestBuild_HermeticEnv(t *testing.T) {
	sentinel := "bufa-env-probe-value"
	t.Setenv("HERMETIC_ENV_PROBE", sentinel)

	win := "@echo off\r\necho %HERMETIC_ENV_PROBE%>probe.txt\r\necho %windir%>windir.txt\r\n" +
		"echo %PATHEXT%>pathext.txt\r\necho %ComSpec%>comspec.txt\r\n"
	unix := "printf '%s' \"$HERMETIC_ENV_PROBE\" > probe.txt\nprintf '%s' \"$HOME\" > home.txt\n"

	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[unsafe]\nttl = \"1h\"\n")
	f.write(t, filepath.Join("Safe", "BUFA"), bufaToml(win, unix))
	f.write(t, filepath.Join("Unsafe", "BUFA"), "unsafe = true\n"+bufaToml(win, unix))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	safeKey := b.Build("Safe")
	if probe := f.readOut(t, safeKey, "probe.txt"); strings.Contains(probe, sentinel) {
		t.Errorf("safe build leaked inherited env var: %q", probe)
	}
	if runtime.GOOS == "windows" {
		if windir := strings.TrimSpace(f.readOut(t, safeKey, "windir.txt")); windir != os.Getenv("windir") {
			t.Errorf("safe build windir = %q, want %q", windir, os.Getenv("windir"))
		}
		if pathExt := strings.TrimSpace(f.readOut(t, safeKey, "pathext.txt")); pathExt != cleanPathExt {
			t.Errorf("safe build PATHEXT = %q, want stock default %q", pathExt, cleanPathExt)
		}
		wantComSpec := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		if comSpec := strings.TrimSpace(f.readOut(t, safeKey, "comspec.txt")); comSpec != wantComSpec {
			t.Errorf("safe build ComSpec = %q, want stock default %q", comSpec, wantComSpec)
		}
	} else {
		if home := strings.TrimSpace(f.readOut(t, safeKey, "home.txt")); home != filepath.Join(f.bld, "tmp") {
			t.Errorf("safe build HOME = %q, want synthetic %q", home, filepath.Join(f.bld, "tmp"))
		}
	}

	unsafeKey := b.Build("Unsafe")
	if probe := f.readOut(t, unsafeKey, "probe.txt"); !strings.Contains(probe, sentinel) {
		t.Errorf("unsafe build must keep inherited env, got %q", probe)
	}
	if runtime.GOOS == "windows" {
		if pathExt := strings.TrimSpace(f.readOut(t, unsafeKey, "pathext.txt")); pathExt != os.Getenv("PATHEXT") {
			t.Errorf("unsafe build PATHEXT = %q, want inherited %q", pathExt, os.Getenv("PATHEXT"))
		}
		if comSpec := strings.TrimSpace(f.readOut(t, unsafeKey, "comspec.txt")); comSpec != os.Getenv("ComSpec") {
			t.Errorf("unsafe build ComSpec = %q, want inherited %q", comSpec, os.Getenv("ComSpec"))
		}
	} else {
		if home := strings.TrimSpace(f.readOut(t, unsafeKey, "home.txt")); home != os.Getenv("HOME") {
			t.Errorf("unsafe build HOME = %q, want inherited %q", home, os.Getenv("HOME"))
		}
	}
}

const winBufaEnvScript = "@echo off\r\n" +
	"echo %BUFA_BUILD_ROOT%>root.txt\r\n" +
	"echo %BUFA_BUILD_DIR%>dir.txt\r\n" +
	"echo %BUFA_CACHE_ROOT%>cacheroot.txt\r\n" +
	"echo %BUFA_COPY_OR_MOVE%>verb.txt\r\n"
const unixBufaEnvScript = "printf '%s' \"$BUFA_BUILD_ROOT\" > root.txt\n" +
	"printf '%s' \"$BUFA_BUILD_DIR\" > dir.txt\n" +
	"printf '%s' \"$BUFA_CACHE_ROOT\" > cacheroot.txt\n" +
	"printf '%s' \"$BUFA_COPY_OR_MOVE\" > verb.txt\n"

func TestBuild_ScriptEnv(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("A", "U", "BUFA"), bufaToml(winBufaEnvScript, unixBufaEnvScript))
	f.write(t, filepath.Join("A", "U", "own.txt"), "x")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build(filepath.Join("A", "U"))
	read := func(name string) string { return strings.TrimSpace(f.readOut(t, key, name)) }
	if got, want := read("root.txt"), filepath.Join(f.bld, Store.BldSandboxRoot); got != want {
		t.Errorf("BUFA_BUILD_ROOT = %q, want the sandbox root %q", got, want)
	}
	if got, want := read("dir.txt"), filepath.Join("A", "U"); got != want {
		t.Errorf("BUFA_BUILD_DIR = %q, want root-relative %q", got, want)
	}
	if got, want := read("cacheroot.txt"), filepath.Join(f.bld, Store.UserRoot); got != want {
		t.Errorf("BUFA_CACHE_ROOT = %q, want %q", got, want)
	}
	wantVerb := "mv"
	if runtime.GOOS == "windows" {
		wantVerb = "move"
	}
	if got := read("verb.txt"); got != wantVerb {
		t.Errorf("BUFA_COPY_OR_MOVE = %q, want %q", got, wantVerb)
	}
}

const winTempScript = "@echo off\r\n" +
	"echo %TEMP%>temp.txt\r\n" +
	"echo %TMP%>tmp.txt\r\n" +
	"if exist \"%TEMP%\\marker.txt\" (echo stale>stale.txt) else (echo fresh>stale.txt)\r\n" +
	"echo x>\"%TEMP%\\marker.txt\"\r\n" +
	"copy \"%TEMP%\\marker.txt\" copied.txt >nul\r\n"
const unixTempScript = "printf '%s' \"$TMPDIR\" > temp.txt\n" +
	"if [ -e \"$TMPDIR/marker.txt\" ]; then printf stale > stale.txt; else printf fresh > stale.txt; fi\n" +
	"echo x > \"$TMPDIR/marker.txt\"\n" +
	"cp \"$TMPDIR/marker.txt\" copied.txt\n"

const winTempFail = "@echo off\r\n" +
	"if exist \"%TEMP%\\marker.txt\" (echo stale>stale.txt) else (echo fresh>stale.txt)\r\n" +
	"echo x>\"%TEMP%\\marker.txt\"\r\n" +
	"exit /b 1\r\n"
const unixTempFail = "if [ -e \"$TMPDIR/marker.txt\" ]; then printf stale > stale.txt; else printf fresh > stale.txt; fi\n" +
	"echo x > \"$TMPDIR/marker.txt\"\n" +
	"exit 1\n"

func TestBuild_PrivateTempDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Fail", "BUFA"), bufaToml(winTempFail, unixTempFail))
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winTempScript, unixTempScript))
	f.write(t, filepath.Join("U", "own.txt"), "x")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	if err := c.Rescue(func() { b.Build("Fail") }); err == nil {
		t.Fatal("Fail build must panic")
	}
	if !f.exists("tmp/marker.txt") {
		t.Error("failed build must keep tmp/ (and its marker) for debugging")
	}

	// Re-running Fail on the same Builder hits localSrcCache, so only the per-script recreation
	// could wipe the first run's marker.
	if err := c.Rescue(func() { b.Build("Fail") }); err == nil {
		t.Fatal("Fail build must panic")
	}
	stale, err := os.ReadFile(filepath.Join(f.bld, "bld", "Fail", "stale.txt"))
	if err != nil {
		t.Fatalf("read sandbox stale.txt: %v", err)
	}
	if got := strings.TrimSpace(string(stale)); got != "fresh" {
		t.Error("temp dir not recreated: previous build's marker survived")
	}

	key := b.Build("U")
	read := func(name string) string {
		return strings.TrimSpace(f.readOut(t, key, name))
	}

	wantTmp := filepath.Join(f.bld, "tmp")
	if got := read("temp.txt"); got != wantTmp {
		t.Errorf("TEMP/TMPDIR = %q, want %q", got, wantTmp)
	}
	if runtime.GOOS == "windows" {
		if got := read("tmp.txt"); got != wantTmp {
			t.Errorf("TMP = %q, want %q", got, wantTmp)
		}
	}
	if got := read("stale.txt"); got != "fresh" {
		t.Error("temp dir not recreated: previous build's marker survived")
	}
	read("copied.txt") // Fatals if the round-trip through the temp dir failed
	if f.exists("tmp") {
		t.Error("tmp/ must be removed after a successful build")
	}
}

func TestBuild_MissingPlatformScript(t *testing.T) {
	f := newFixture(t)
	var cfg string
	if runtime.GOOS == "windows" {
		cfg = "[unix]\ncmd = '''" + unixScript + "'''\n"
	} else {
		cfg = "[windows]\ncmd = '''" + winScript + "'''\n"
	}
	f.write(t, filepath.Join("U", "BUFA"), cfg)
	f.write(t, filepath.Join("U", "own.txt"), "x")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil {
		t.Fatal("missing platform cmd must panic")
	}
	if !strings.Contains(err.Error(), BuildConfig.PlatformSectionNames+".cmd") {
		t.Errorf("panic message %q should name this platform's sections %s", err, BuildConfig.PlatformSectionNames)
	}
}

func TestBuild_BadToml(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), "this = is not [valid toml")
	f.write(t, filepath.Join("U", "own.txt"), "x")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	if err := c.Rescue(func() { b.Build("U") }); err == nil {
		t.Fatal("malformed TOML must panic")
	}
}

func TestBuild_NotADirectory(t *testing.T) {
	f := newFixture(t)
	f.write(t, "not-a-dir", "x")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	if err := c.Rescue(func() { b.Build("not-a-dir") }); err == nil {
		t.Fatal("Build on a regular file must panic")
	}
}

func TestResolveRelDir(t *testing.T) {
	cases := []struct {
		name, baseDir, dir, want string
		wantPanic                bool
	}{
		{"plain-relative", "a/b", "c", filepath.Join("a", "b", "c"), false},
		{"leading-slash-is-root-relative", "a/b", "/x/y", filepath.FromSlash("x/y"), false},
		{"parent-relative-allowed", "a/b", "../c", filepath.Join("a", "c"), false},
		{"escape-via-relative-panics", "a/b", "../../../x", "", true},
		{"escape-to-parent-panics", "a", "../..", "", true},
		{"resolves-to-current-allowed", "a", "..", ".", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantPanic {
				if err := c.Rescue(func() { resolveRelDir(tc.baseDir, tc.dir) }); err == nil {
					t.Fatalf("resolveRelDir(%q,%q) must panic", tc.baseDir, tc.dir)
				}
				return
			}
			if got := resolveRelDir(tc.baseDir, tc.dir); got != tc.want {
				t.Errorf("resolveRelDir(%q,%q)=%q, want %q", tc.baseDir, tc.dir, got, tc.want)
			}
		})
	}
}

func TestResolveRelDir_AbsolutePanics(t *testing.T) {
	// filepath.IsAbs is only reachable on Windows: on Unix the leading-slash case catches first.
	if runtime.GOOS != "windows" {
		t.Skip("non-leading-slash absolute path only exists on Windows")
	}
	abs := c.Check2(filepath.Abs("anywhere"))
	if err := c.Rescue(func() { resolveRelDir("a", abs) }); err == nil {
		t.Fatalf("absolute dep %q must panic", abs)
	}
}

const virtualWin = "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\necho hi>out.txt\r\n"
const virtualUnix = "echo x >> \"$BUFA_TEST_COUNTER\"\necho hi > out.txt\n"

func virtualToml() string { return bufaToml(virtualWin, virtualUnix) }

func TestBuild_VirtualDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript)) // parent build dir
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())               // declares Build/clj

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("Build/clj")
	if !strings.HasPrefix(key, "D") {
		t.Fatalf("output key must be a content hash, got %q", key)
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("virtual unit output not published")
	}
	if runs := f.countRuns(t); runs != 1 {
		t.Fatalf("script ran %d times, want 1", runs)
	}

	if !f.exists("in/" + Hashing.EmptyDirHash) {
		t.Error("empty source tree not published under in/")
	}
	if got := b.store.GetLinkTarget(Store.InRoot, "∕Build∕clj"); got != Hashing.EmptyDirHash {
		t.Errorf("in ∕build∕clj -> %q, want empty-dir hash %q", got, Hashing.EmptyDirHash)
	}

	if k2 := b.Build("Build/clj"); k2 != key {
		t.Errorf("unchanged rebuild: %q -> %q", key, k2)
	}
	if runs := f.countRuns(t); runs != 1 {
		t.Errorf("cached rebuild ran the script: now %d runs", runs)
	}

	f.write(t, filepath.Join("Build", "clj.BUFA"),
		bufaToml("rem v2\r\n"+virtualWin, "# v2\n"+virtualUnix))
	f.builder().Build("Build/clj") // fresh Builder — local caches empty
	if runs := f.countRuns(t); runs != 2 {
		t.Errorf("config-only change must re-run the script: now %d runs, want 2", runs)
	}
}

func TestBuild_VirtualConfigNotStagedInParent(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Build", "parent-src")                              // Build/BUFA + Build/own.txt
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml()) // virtual config in the same dir

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	k := b.Build("Build")
	if f.exists("out/" + k + "/clj.BUFA") {
		t.Error("clj.BUFA config must not be staged into the parent's source")
	}
	if !f.exists("out/" + k + "/out.txt") {
		t.Error("parent own source (own.txt -> out.txt) must survive")
	}
}

func TestBuild_VirtualDirWithoutParentBufa(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml()) // no Build/BUFA

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("Build/clj")
	if !strings.HasPrefix(key, "D") {
		t.Fatalf("output key must be a content hash, got %q", key)
	}
	if !f.exists("out/" + key + "/out.txt") {
		t.Error("virtual unit output not published")
	}
	if runs := f.countRuns(t); runs != 1 {
		t.Fatalf("script ran %d times, want 1", runs)
	}
	if got := b.store.GetLinkTarget(Store.InRoot, "∕Build∕clj"); got != Hashing.EmptyDirHash {
		t.Errorf("in ∕build∕clj -> %q, want empty-dir hash %q", got, Hashing.EmptyDirHash)
	}
}

func TestBuild_NestedVirtualConfigNotStaged(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Build", "parent-src")                                     // Build/BUFA + Build/own.txt
	f.write(t, filepath.Join("Build", "sub", "clj.BUFA"), virtualToml()) // declares Build/sub/clj

	skipIfNoSymlinks(t, f.builder().store)
	k := f.builder().Build("Build")
	if f.exists("out/" + k + "/sub/clj.BUFA") {
		t.Error("nested clj.BUFA must not be staged into the owning dir's source")
	}
	if f.exists("out/" + k + "/sub") {
		t.Error("sub/ held only the config, so the empty dir must be pruned")
	}
	if !f.exists("out/" + k + "/out.txt") {
		t.Error("owning dir's own source (own.txt -> out.txt) must survive")
	}

	if key := f.builder().Build("Build/sub/clj"); !f.exists("out/" + key + "/out.txt") {
		t.Error("nested virtual unit output not published")
	}
	if runs := f.countRuns(t); runs != 2 {
		t.Fatalf("owning dir + virtual unit: %d runs, want 2", runs)
	}

	f.write(t, filepath.Join("Build", "sub", "clj.BUFA"),
		bufaToml("rem v2\r\n"+virtualWin, "# v2\n"+virtualUnix))
	if k2 := f.builder().Build("Build"); k2 != k {
		t.Errorf("nested config edit must not re-key the owning dir: %q -> %q", k, k2)
	}
	f.builder().Build("Build/sub/clj")
	if runs := f.countRuns(t); runs != 3 {
		t.Errorf("nested config edit must re-key the virtual unit: %d runs, want 3", runs)
	}
}

func TestBuild_VirtualDirMustNotExistInSource(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())
	f.write(t, filepath.Join("Build", "clj", "stray.txt"), "x") // real Build/clj dir

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	err := c.Rescue(func() { b.Build("Build/clj") })
	if err == nil {
		t.Fatal("a real Build/clj dir alongside clj.BUFA must panic")
	}
	if !strings.Contains(err.Error(), "must not exist in source") {
		t.Errorf("panic %q should mention the must-not-exist collision", err)
	}
}

// Reaching that check needs the Open failure to read as not-exist everywhere — Unix reports
// ENOTDIR, not ErrNotExist.
func TestBuild_FileAtVirtualPathCollides(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())
	f.write(t, filepath.Join("Build", "clj"), "a file, not a dir")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	err := c.Rescue(func() { b.Build("Build/clj") })
	if err == nil {
		t.Fatal("a file at the virtual path must panic")
	}
	if !strings.Contains(err.Error(), "Unexpected file") {
		t.Errorf("panic %q should be the file-collision message, not a raw Open error", err)
	}
}

func TestBuild_VirtualDirCollidesWithRealUnit(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())
	f.unit(t, filepath.Join("Build", "clj"), "real") // real Build/clj/BUFA + own.txt

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	err := c.Rescue(func() { b.Build("Build/clj") })
	if err == nil {
		t.Fatal("real Build/clj/BUFA colliding with Build/clj.BUFA must panic")
	}
	if !strings.Contains(err.Error(), "collide") {
		t.Errorf("panic %q should mention the collision", err)
	}
}

func TestBuild_BldDepOnVirtualDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml()) // declares Build/clj
	depWin := "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\ncopy ..\\Build\\clj\\out.txt dep.txt >nul\r\n"
	depUnix := "echo x >> \"$BUFA_TEST_COUNTER\"\ncp ../Build/clj/out.txt dep.txt\n"
	f.write(t, filepath.Join("Top", "BUFA"),
		"[deps]\nbld = [\"/Build/clj\"]\n"+bufaToml(depWin, depUnix))

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	key := b.Build("Top")
	if !f.exists("out/" + key + "/dep.txt") {
		t.Error("virtual dep's output must be restored into the consumer's sandbox")
	}
	if runs := f.countRuns(t); runs != 2 {
		t.Errorf("virtual dep + consumer should each run once, got %d runs", runs)
	}
}

func TestBuild_VirtualConfigEditDoesNotRekeyParent(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Build", "parent-src") // Build/BUFA + Build/own.txt
	f.write(t, filepath.Join("Build", "clj.BUFA"), virtualToml())
	skipIfNoSymlinks(t, f.builder().store)

	f.builder().Build("Build")
	f.builder().Build("Build/clj")
	if runs := f.countRuns(t); runs != 2 {
		t.Fatalf("initial builds: %d runs, want 2", runs)
	}

	f.write(t, filepath.Join("Build", "clj.BUFA"),
		bufaToml("rem v2\r\n"+virtualWin, "# v2\n"+virtualUnix))
	f.builder().Build("Build")
	if runs := f.countRuns(t); runs != 2 {
		t.Errorf("config edit must not re-key the parent: %d runs, want 2", runs)
	}
	f.builder().Build("Build/clj")
	if runs := f.countRuns(t); runs != 3 {
		t.Errorf("config edit must re-key the virtual unit: %d runs, want 3", runs)
	}
}

// GC liveness for virtual units: a directory at the config path silently does not count, a
// directory named BUFA panics, and the lowercase dir keeps ∕-links matching case-sensitive FSes.
func TestBuild_GCKeepsLiveVirtualDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("build", "own.txt"), "parent-src")
	f.write(t, filepath.Join("build", "clj.BUFA"), virtualToml())
	skipIfNoSymlinks(t, f.builder().store)

	f.builder().Build("build/clj")
	if runs := f.countRuns(t); runs != 1 {
		t.Fatalf("initial build: %d runs, want 1", runs)
	}

	store := Store.NewStore(vfs.NewBasePathFs(vfs.NewOsFs(), f.bld))
	gc := func() Store.GCStats {
		return store.GC(func(path string) bool { return Store.BuildDirPresent(f.srcFS, path) }, false)
	}

	if st := gc(); st != (Store.GCStats{}) {
		t.Fatalf("GC with a live virtual config must collect nothing, got %+v", st)
	}
	f.builder().Build("build/clj")
	if runs := f.countRuns(t); runs != 1 {
		t.Fatalf("post-GC rebuild re-ran the script: %d runs, want 1", runs)
	}

	c.Check(f.srcFS.Remove(filepath.Join("build", "clj.BUFA")))
	c.Check(f.srcFS.MkdirAll(filepath.Join("build", "clj.BUFA"), 0o755))
	want := Store.GCStats{StalePathLinks: 2, OrphanContent: 2, OrphanBuildLinks: 1}
	if st := gc(); st != want {
		t.Fatalf("GC after config delete: got %+v, want %+v", st, want)
	}
	if f.exists("in/" + Hashing.EmptyDirHash) {
		t.Error("orphaned shared empty source tree must be collected")
	}

	c.Check(f.srcFS.MkdirAll(filepath.Join("build", "clj", "BUFA"), 0o755))
	if err := c.Rescue(func() { Store.BuildDirPresent(f.srcFS, "build/clj") }); err == nil {
		t.Error("BuildDirPresent with a directory named BUFA must panic (reserved name)")
	}
}

// allowBufaDir: a BUFA-named source subdirectory stages as ordinary content.
func TestBuild_AllowBufaDir(t *testing.T) {
	f := newFixture(t)
	skipIfNoSymlinks(t, f.builder().store)
	f.unit(t, "u", "OWN")
	f.write(t, filepath.Join("u", "sub", "BUFA", "inner.txt"), "INNER")

	err := c.Rescue(func() { f.builder().Build("u") })
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("without allowBufaDir the build must fail on the reserved name, got: %v", err)
	}

	f.write(t, Store.SrcRootFileName, "allowBufaDir = true\n")
	b := f.builder()
	b.Build("u")
	srcHash := b.store.GetLinkTarget(Store.InRoot, "∕u")
	if !f.exists("in/" + srcHash + "/sub/BUFA/inner.txt") {
		t.Error("the BUFA-named dir must stage as content")
	}
}

// The marker rename root.BUFA -> .BUFA freed the "root" suffix: it declares an ordinary virtual dir.
func TestBuild_RootNamedDirCanBeVirtual(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "root", "root-src") // real root/BUFA next to the fixture's .BUFA marker

	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	if key := b.Build("root"); !strings.HasPrefix(key, "D") {
		t.Fatalf("real build dir 'root' must build, got %q", key)
	}

	f2 := newFixture(t) // no root/ dir at all — root.BUFA declares the virtual dir "root"
	f2.write(t, "root.BUFA", virtualToml())
	b2 := f2.builder()
	key := b2.Build("root")
	if !strings.HasPrefix(key, "D") {
		t.Fatalf("virtual build dir 'root' must build, got %q", key)
	}
	if !f2.exists("out/" + key + "/out.txt") {
		t.Error("virtual unit output not published")
	}
}

func largeToml() string { return "largeOutput = true\n" + bufaToml(winScript, unixScript) }

func TestBuild_LinkStagedDep(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Tool", "BUFA"), largeToml())
	f.write(t, filepath.Join("Tool", "own.txt"), "tool-payload")
	winConsume := "@echo off\r\ncopy ..\\Tool\\out.txt got.txt >nul\r\nif errorlevel 1 exit /b 1\r\n"
	unixConsume := "cp ../Tool/out.txt got.txt\n"
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"../Tool\"]\n"+bufaToml(winConsume, unixConsume))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	toolKey := b.Build("Tool")
	key := b.Build("U")
	data, err := os.ReadFile(filepath.Join(f.bld, "out", key, "got.txt"))
	if err != nil {
		t.Fatalf("consumer must read the dep through the link: %v", err)
	}
	if got := string(data); got != "tool-payload" {
		t.Errorf("got.txt = %q, want the dep payload", got)
	}
	if !f.exists("out/" + toolKey + "/out.txt") {
		t.Error("linked store tree must survive the consumer build and sandbox wipe")
	}
	if f.exists("bld/Tool") {
		t.Error("sandbox must be deleted on success")
	}
}

func TestBuild_LinkStagedDepIsSymlink(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Tool", "BUFA"), largeToml())
	f.write(t, filepath.Join("Tool", "own.txt"), "tool")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"../Tool\"]\n"+bufaToml(winFail, unixFail))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	if err := c.Rescue(func() { b.Build("U") }); err == nil {
		t.Fatal("failing script must panic out of Build")
	}
	toolKey := b.Build("Tool") // local-cache hit: built during the failed U build
	if got := b.store.GetLinkTarget("bld", "Tool"); got != toolKey {
		t.Fatalf("kept sandbox must hold a symlink to the dep's out key %q, GetLinkTarget = %q", toolKey, got)
	}
	if !osExists(filepath.Join(f.bld, "bld", "Tool", "out.txt")) {
		t.Error("dep content not browsable through the kept link")
	}
}

func TestBuild_NestedLinkStagedDepNotPublished(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "tool", "BUFA"), largeToml())
	f.write(t, filepath.Join("U", "tool", "own.txt"), "tool")
	winConsume := "@echo off\r\ncopy tool\\out.txt got.txt >nul\r\nif errorlevel 1 exit /b 1\r\n"
	unixConsume := "cp tool/out.txt got.txt\n"
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"tool\"]\n"+bufaToml(winConsume, unixConsume))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	toolKey := b.Build(filepath.Join("U", "tool"))
	key := b.Build("U")
	if f.exists("out/" + key + "/tool") {
		t.Error("nested link-staged dep must not ride into the consumer's output")
	}
	if !f.exists("out/" + key + "/got.txt") {
		t.Error("consumer output missing")
	}
	if !f.exists("out/" + toolKey + "/out.txt") {
		t.Error("prune must remove the link object, not the store tree behind it")
	}
}

func TestBuild_LeafContractViolationPanics(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Tool", "BUFA"), largeToml())
	f.write(t, filepath.Join("Tool", "own.txt"), "tool")
	f.unit(t, filepath.Join("Tool", "sub"), "sub") // build dir nested inside the linked dep
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"../Tool\"]\nsrc = [\"../Tool/sub\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil {
		t.Fatal("staged target below a link-staged dep must panic")
	}
	if msg := err.Error(); !strings.Contains(msg, "'Tool'") || !strings.Contains(msg, "'"+filepath.Join("Tool", "sub")+"'") {
		t.Fatalf("error %q must name both paths", err)
	}
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("only the dep's own build may run before the violation; got %d runs, want 1", r)
	}
}

func TestBuild_LinkStagedVirtualDep(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("Build", "BUFA"), bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Build", "own.txt"), "parent")
	f.write(t, filepath.Join("Build", "go.BUFA"), "largeOutput = true\n"+virtualToml())
	winConsume := "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\ncopy ..\\Build\\go\\out.txt got.txt >nul\r\nif errorlevel 1 exit /b 1\r\n"
	unixConsume := "echo x >> \"$BUFA_TEST_COUNTER\"\ncp ../Build/go/out.txt got.txt\n"
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = [\"/Build/go\"]\n"+bufaToml(winConsume, unixConsume))
	f.write(t, filepath.Join("U", "own.txt"), "x")

	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	key := b.Build("U")
	if !f.exists("out/" + key + "/got.txt") {
		t.Error("consumer must read the virtual dep through the link")
	}
	if runs := f.countRuns(t); runs != 2 {
		t.Errorf("virtual dep + consumer should each run once, got %d runs", runs)
	}
}

func TestBuild_SafeSourceSymlinkFails(t *testing.T) {
	cases := []struct {
		name   string
		target func(f *fixture) string
	}{
		{"file link", func(f *fixture) string { return filepath.Join(f.src, "U", "own.txt") }},
		{"dir link", func(f *fixture) string { return filepath.Join(f.src, "U") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.unit(t, "U", "OWN")
			b := f.builder()
			skipIfNoSymlinks(t, b.store)
			osSymlink(tc.target(f), filepath.Join(f.src, "U", "lnk"))

			err := c.Rescue(func() { b.Build("U") })
			if err == nil {
				t.Fatal("a safe build with a source symlink must fail")
			}
			if msg := err.Error(); !strings.Contains(msg, "lnk") ||
				!strings.Contains(msg, "[[deps.ext]]") || !strings.Contains(msg, "unsafe") {
				t.Errorf("the error must name the link with the deps.ext/unsafe hint, got %q", msg)
			}
		})
	}
}

func TestBuild_UnsafeSourceSymlinkPublishesLinkObject(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), "unsafe = true\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("U", "own.txt"), "OWN")
	f.write(t, "shared.txt", "SHARED")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	target := filepath.Join(f.src, "shared.txt")
	osSymlink(target, filepath.Join(f.src, "U", "lnk"))

	h := b.Build("U")

	published := filepath.Join(f.bld, "out", h, "lnk")
	info, err := os.Lstat(published)
	if err != nil {
		t.Fatalf("published link missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("published lnk mode %v, want a link object (never materialized)", info.Mode())
	}
	if got := c.Check2(os.Readlink(published)); got != target {
		t.Errorf("published link targets %q, want the absolute %q", got, target)
	}
	if got := c.Check2(os.ReadFile(published)); string(got) != "SHARED" {
		t.Errorf("bytes through published link = %q, want SHARED", got)
	}
}

// Shared preamble of the safe/unsafe script-created-link tests, so the pair cannot diverge.
func linkScripts(t *testing.T, f *fixture) (winLink, unixLink, target string) {
	t.Helper()
	target = filepath.Join(f.src, "link-target.txt")
	f.write(t, "link-target.txt", "target bytes")
	t.Setenv("BUFA_TEST_LINK_TARGET", target)
	winLink = "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\n" +
		"mklink output-link.txt \"%BUFA_TEST_LINK_TARGET%\" >nul\r\n"
	unixLink = "echo x >> \"$BUFA_TEST_COUNTER\"\n" +
		"ln -s \"$BUFA_TEST_LINK_TARGET\" output-link.txt\n"
	return winLink, unixLink, target
}

func TestBuild_SafeScriptCreatedLinkRejectedAtPublish(t *testing.T) {
	f := newFixture(t)
	winLink, unixLink, _ := linkScripts(t, f)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winLink, unixLink))
	f.write(t, filepath.Join("U", "own.txt"), "source")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	err := c.Rescue(func() { b.Build("U") })
	if err == nil {
		t.Fatal("a safe build must reject an undeclared script-created link")
	}
	if msg := err.Error(); !strings.Contains(msg, "output-link.txt") ||
		!strings.Contains(msg, "[[deps.ext]]") || !strings.Contains(msg, "unsafe") {
		t.Errorf("the error must name the link with the deps.ext/unsafe hint, got %q", msg)
	}
	if runs := f.countRuns(t); runs != 1 {
		t.Errorf("the policy must fail at publish, after exactly one script run, got %d", runs)
	}
	if _, statErr := os.Lstat(filepath.Join(f.bld, Store.BldSandboxRoot, "U", "output-link.txt")); statErr != nil {
		t.Errorf("a failed publish must preserve the script's sandbox link: %v", statErr)
	}
	if !f.exists("bld/U/own.txt") {
		t.Error("a failed publish must preserve the sandbox contents")
	}
}

func TestBuild_UnsafeScriptCreatedLinkPublishes(t *testing.T) {
	f := newFixture(t)
	winLink, unixLink, target := linkScripts(t, f)
	f.write(t, filepath.Join("U", "BUFA"), "unsafe = true\n"+bufaToml(winLink, unixLink))
	f.write(t, filepath.Join("U", "own.txt"), "source")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)
	osSymlink(target, filepath.Join(f.src, "U", "source-link.txt"))

	hash := b.Build("U")
	srcHash := b.getSrcHash("U")
	for _, path := range []string{
		filepath.Join(f.bld, Store.InRoot, srcHash, "source-link.txt"),
		filepath.Join(f.bld, Store.OutRoot, hash, "source-link.txt"),
		filepath.Join(f.bld, Store.OutRoot, hash, "output-link.txt"),
	} {
		if got := c.Check2(os.Readlink(path)); got != target {
			t.Errorf("link %q targets %q, want preserved absolute target %q", path, got, target)
		}
		if data := c.Check2(os.ReadFile(path)); string(data) != "target bytes" {
			t.Errorf("link %q reads %q, want the referent's bytes", path, data)
		}
	}
}

// A shell session under test: commands fed through stdin, the session's output captured.
func shellSession(rc *Runtime.Config, mode Runtime.BuildMode, target, input string) *bytes.Buffer {
	var out bytes.Buffer
	rc.Out = &out
	rc.In = strings.NewReader(input)
	rc.BuildMode = mode
	rc.TargetDirs = []string{target} // CLI normally sets this; no CLI here
	return &out
}

func TestBuild_ShellInsteadStagesAndKeepsSandbox(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "Dep", "own-of-dep")
	f.write(t, filepath.Join("Parent", "BUFA"),
		"[deps]\nbld = [\"../Dep\"]\n"+bufaToml(winScript, unixScript))
	f.write(t, filepath.Join("Parent", "own.txt"), "own-of-parent")
	skipIfNoSymlinks(t, f.builder().store)

	b := f.builder()
	out := shellSession(&b.Config, Runtime.ModeShell, "Parent",
		byOS("@echo SHELL_DIR=%BUFA_BUILD_DIR%\r\n@type own.txt\r\n@type ..\\Dep\\out.txt\r\n@echo x>shell.txt\r\nexit\r\n",
			"echo SHELL_DIR=$BUFA_BUILD_DIR\ncat own.txt\ncat ../Dep/out.txt\necho x > shell.txt\nexit\n"))
	if key := b.Build("Parent"); key != "" {
		t.Errorf("a shell-only session builds nothing, got result %q", key)
	}
	if r := f.countRuns(t); r != 1 {
		t.Errorf("the dep builds, the target's script must not run: %d runs, want 1", r)
	}
	for _, want := range []string{
		"----- Shell Start: Parent ('exit' to continue) -----",
		"Dir: " + filepath.Join(f.bld, Store.BldSandboxRoot, "Parent"),
		"Script: " + filepath.Join(f.bld, Store.TmpRoot, buildScriptFile()),
		"SHELL_DIR=Parent", // the script's env
		"own-of-parent",    // own source staged in the cwd
		"own-of-dep",       // the dep's output staged beside it
		"-----   Shell End: Parent -----",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("session output lacks %q:\n%s", want, out.String())
		}
	}
	f.requireKeptForInspection(t, "bld/Parent/own.txt")
	if d := f.outDirs(t); d != 1 {
		t.Errorf("only the dep's output may be published, got %d out dirs", d)
	}
	if f.exists("out/∕Parent") {
		t.Error("a shell-only session must not record a build result for the target")
	}

	f.builder().Build("Parent")
	if r := f.countRuns(t); r != 2 {
		t.Errorf("the next plain build must run the target's script (nothing was cached): %d runs, want 2", r)
	}
}

func TestBuild_ShellInsteadBypassesCachedResult(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "v1")
	skipIfNoSymlinks(t, f.builder().store)
	f.builder().Build("U")

	b := f.builder()
	out := shellSession(&b.Config, Runtime.ModeShell, "U", byOS("exit\r\n", "exit\n"))
	b.Build("U")
	if !strings.Contains(out.String(), "Shell Start: U") {
		t.Errorf("a cached target must still open its shell, got:\n%s", out.String())
	}
	if r := f.countRuns(t); r != 1 {
		t.Errorf("the shell replaces the script: %d runs, want 1", r)
	}

	f.requireStillCached(t, "U", 1)
}

func TestBuild_ShellAfterRunsScriptAndKeepsSandbox(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winScript+"set SHELL_STATE=kept\r\n", unixScript+"SHELL_STATE=kept\n"))
	f.write(t, filepath.Join("U", "own.txt"), "v1")
	skipIfNoSymlinks(t, f.builder().store)
	f.builder().Build("U")

	b := f.builder()
	out := shellSession(&b.Config, Runtime.ModePostShell, "U",
		byOS("@type out.txt\r\n@echo STATE=%SHELL_STATE%\r\n@echo tweak>shell.txt\r\nexit\r\n",
			"cat out.txt\necho STATE=$SHELL_STATE\necho tweak > shell.txt\nexit\n"))
	if key := b.Build("U"); key != "" {
		t.Errorf("a session builds nothing, got result %q", key)
	}
	if r := f.countRuns(t); r != 2 {
		t.Errorf("--post-shell must run the target's script past its cached result: %d runs, want 2", r)
	}
	if s := out.String(); strings.Index(s, "Building dir: U") > strings.Index(s, "Shell Start: U") || !strings.Contains(s, "v1") {
		t.Errorf("the shell must open after the script, seeing its output:\n%s", s)
	}
	if s := out.String(); !strings.Contains(s, "STATE=kept") {
		t.Errorf("the shell is the script's own process, so its state must survive:\n%s", s)
	}
	if d := f.outDirs(t); d != 1 {
		t.Errorf("a session must not publish: %d out dirs, want the first build's 1", d)
	}
	f.requireKeptForInspection(t, "bld/U/shell.txt")
	f.requireStillCached(t, "U", 2)
}

func TestBuild_ShellAfterScriptFailureKeepsTheShell(t *testing.T) {
	f := newFixture(t)
	// bash's top-level `exit 1` ends the session by design (cmd's `exit /b 1` does not), so fail with a plain false.
	f.write(t, filepath.Join("U", "BUFA"), bufaToml(winEchoFail, "echo BUFA_MARKER\nfalse\n"))
	f.write(t, filepath.Join("U", "own.txt"), "x")
	skipIfNoSymlinks(t, f.builder().store)

	b := f.builder()
	out := shellSession(&b.Config, Runtime.ModePostShell, "U", byOS("@echo IN_SHELL\r\nexit\r\n", "echo IN_SHELL\nexit\n"))
	if err := c.Rescue(func() { b.Build("U") }); err != nil {
		t.Fatalf("a session is not a build, so the script's exit status must not fail it: %v", err)
	}
	s := out.String()
	scriptAt, shellAt := strings.Index(s, "BUFA_MARKER"), strings.Index(s, "IN_SHELL")
	if scriptAt < 0 || shellAt < 0 || scriptAt > shellAt {
		t.Errorf("the script's output must precede the shell's:\n%s", s)
	}
	f.requireKeptForInspection(t, "bld/U/own.txt")
	if d := f.outDirs(t); d != 0 {
		t.Errorf("a session must not publish, got %d out dirs", d)
	}
}
