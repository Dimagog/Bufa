package Watcher

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rjeczalik/notify"

	"github.com/dimagog/bufa"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

// Regression: a short-circuit on non-nil nsCleanup made the takeover RPC a silent no-op on the
// daemon whose NS was externally stopped — its held cleanup is spent, not live.
func TestTryStartNS_ReprobesWhenCleanupHeld(t *testing.T) {
	saved := nsTryStart
	defer func() { nsTryStart = saved }()

	ran := ""
	w := &watcher{}
	w.nsCleanup = func() { ran = "spent" }
	nsTryStart = func() func() { return func() { ran = "fresh" } }

	w.tryStartNS()
	w.closeNS()
	if ran != "fresh" {
		t.Fatalf("closeNS ran %q cleanup; tryStartNS must re-probe and replace a held one", ran)
	}
}

func TestTryStartNS_ClaimLossKeepsHeldCleanup(t *testing.T) {
	saved := nsTryStart
	defer func() { nsTryStart = saved }()

	ran := ""
	w := &watcher{}
	w.nsCleanup = func() { ran = "held" }
	nsTryStart = func() func() { return nil }

	w.tryStartNS()
	w.closeNS()
	if ran != "held" {
		t.Fatalf("closeNS ran %q cleanup; a lost claim must not drop the held one", ran)
	}
}

// FSEvents assigns ids to a fixture's own setup events asynchronously, so a stream started right after
// them can replay them as live Creates. Ids are volume-global, so a probe on a throwaway temp dir waits
// for everything written so far to be committed without touching the fixture.
func settledWatcher(dir string) *watcher {
	probeDir := c.Check2(os.MkdirTemp("", "fsevents-probe"))
	probe := make(chan notify.EventInfo, 16)
	c.Check(notify.Watch(probeDir, probe, notify.Create))
	c.Check(os.WriteFile(filepath.Join(probeDir, "marker"), nil, 0o644))
	deadline := time.After(5 * time.Second)
	for settled := false; !settled; {
		select {
		case ev := <-probe:
			settled = filepath.Base(ev.Path()) == "marker"
		case <-deadline:
			settled = true
		}
	}
	notify.Stop(probe)
	c.Check(os.RemoveAll(probeDir))
	return newWatcher(dir)
}

func waitFor(t *testing.T, predicate func() bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func getHash(t *testing.T, w *watcher, path string) string {
	t.Helper()
	var got string
	if err := w.GetSrcHash(path, &got); err != nil {
		t.Fatalf("GetSrcHash(%q): %v", path, err)
	}
	return got
}

func setHash(t *testing.T, w *watcher, path, hash string) {
	t.Helper()
	var reply Util.Nothing
	if err := w.SetSrcHash(SetSrcHashArgs{Path: path, Hash: hash}, &reply); err != nil {
		t.Fatalf("SetSrcHash(%q,%q): %v", path, hash, err)
	}
}

func getDirty(t *testing.T, w *watcher, path string) string {
	t.Helper()
	var got string
	if err := w.GetDirtyHash(path, &got); err != nil {
		t.Fatalf("GetDirtyHash(%q): %v", path, err)
	}
	return got
}

func setDirty(t *testing.T, w *watcher, path, hash string) {
	t.Helper()
	var reply Util.Nothing
	if err := w.SetDirtyHash(SetDirtyHashArgs{Path: path, Hash: hash}, &reply); err != nil {
		t.Fatalf("SetDirtyHash(%q,%q): %v", path, hash, err)
	}
}

func getSkipHash(t *testing.T, w *watcher, path string) string {
	t.Helper()
	var got string
	if err := w.GetDirtySkipHash(path, &got); err != nil {
		t.Fatalf("GetDirtySkipHash(%q): %v", path, err)
	}
	return got
}

func setSkipHash(t *testing.T, w *watcher, path, skipHash string) {
	t.Helper()
	var reply Util.Nothing
	if err := w.SetDirtySkipHash(SetDirtySkipHashArgs{Path: path, SkipHash: skipHash}, &reply); err != nil {
		t.Fatalf("SetDirtySkipHash(%q,%q): %v", path, skipHash, err)
	}
}

func getInfo(t *testing.T, w *watcher, path string) GetBuildConfigReply {
	t.Helper()
	var got GetBuildConfigReply
	if err := w.GetBuildConfig(path, &got); err != nil {
		t.Fatalf("GetBuildConfig(%q): %v", path, err)
	}
	return got
}

func envNames[V BuildConfig.EnvValue](env BuildConfig.EnvTable[V]) []string {
	return slices.Collect(env.Keys())
}

func envValue[V BuildConfig.EnvValue](env BuildConfig.EnvTable[V], name string) V {
	value, _ := env.Get(name)
	return value
}

func setInfo(t *testing.T, w *watcher, path string, info BuildConfig.BufaConfig) {
	t.Helper()
	var reply Util.Nothing
	if err := w.SetBuildConfig(SetBuildConfigArgs{Path: path, Config: info}, &reply); err != nil {
		t.Fatalf("SetBuildConfig(%q): %v", path, err)
	}
}

func getBuild(t *testing.T, w *watcher, combined string) string {
	t.Helper()
	var reply GetBuildHashReply
	if err := w.GetBuildHash(GetBuildHashArgs{Combined: combined}, &reply); err != nil {
		t.Fatalf("GetBuildHash(%q): %v", combined, err)
	}
	return reply.BuildHash
}

func setBuild(t *testing.T, w *watcher, combined, buildHash string) {
	t.Helper()
	var reply Util.Nothing
	if err := w.SetBuildHash(SetBuildHashArgs{Combined: combined, BuildHash: buildHash}, &reply); err != nil {
		t.Fatalf("SetBuildHash(%q,%q): %v", combined, buildHash, err)
	}
}

func getRoot(t *testing.T, w *watcher) GetRootConfigReply {
	t.Helper()
	var reply GetRootConfigReply
	if err := w.GetRootConfig(Util.Nothing{}, &reply); err != nil {
		t.Fatalf("GetRootConfig: %v", err)
	}
	if reply.BufaVersion != Bufa.Version {
		t.Fatalf("GetRootConfig BufaVersion = %q, want %q", reply.BufaVersion, Bufa.Version)
	}
	return reply
}

func setRoot(t *testing.T, w *watcher, cfg BuildConfig.RootConfig) {
	t.Helper()
	var reply Util.Nothing
	if err := w.SetRootConfig(cfg, &reply); err != nil {
		t.Fatalf("SetRootConfig: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSetGet_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()

	if got := getHash(t, w, "U"); got != "" {
		t.Fatalf("empty cache Get = %q, want \"\"", got)
	}
	setHash(t, w, "U", "H1")
	if got := getHash(t, w, "U"); got != "H1" {
		t.Fatalf("after Set: Get = %q, want H1", got)
	}
}

// Backslash is a separator on Windows only; elsewhere it is an ordinary name character.
func spellings(slashPath string) []string {
	s := []string{slashPath, strings.ToLower(slashPath), strings.ToUpper(slashPath)}
	if runtime.GOOS == "windows" {
		native := filepath.FromSlash(slashPath)
		s = append(s, native, strings.ToUpper(native))
	}
	return s
}

func TestSetGet_CaseAndSeparatorInsensitive(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()

	setHash(t, w, filepath.FromSlash("Sub/Dir"), "H1")
	for _, q := range spellings("Sub/Dir") {
		if got := getHash(t, w, q); got != "H1" {
			t.Errorf("Get(%q) = %q, want H1", q, got)
		}
	}
}

func TestFileChange_WalksUpToBufa(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(filepath.Join(buildDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "U", "H1")
	writeFile(t, filepath.Join(buildDir, "sub", "deep.txt"), "a")

	if !waitFor(t, func() bool { return getHash(t, w, "U") == "" }, 2*time.Second) {
		t.Fatal("cache entry \"U\" not invalidated after deep file change")
	}
}

func TestFileChange_NoBufaAncestor_KeepsCache(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "stale", "H")
	writeFile(t, filepath.Join(dir, "x.txt"), "a")

	time.Sleep(300 * time.Millisecond)
	if got := getHash(t, w, "stale"); got != "H" {
		t.Fatalf("cache \"stale\" got %q, want H (no BUFA in ancestry)", got)
	}
}

func TestOwningDirCache_PopulatedOnFirstEvent(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	writeFile(t, filepath.Join(buildDir, "x.txt"), "a")

	if !waitFor(t, func() bool {
		w.mu.RLock()
		_, ok := w.owningDirCache[Util.NormalizePath(buildDir)]
		w.mu.RUnlock()
		return ok
	}, 2*time.Second) {
		t.Fatal("owningDirCache not populated after first event")
	}
	w.mu.RLock()
	got := w.owningDirCache[Util.NormalizePath(buildDir)]
	w.mu.RUnlock()
	if got != "u" {
		t.Errorf("owningDirCache[buildDir] = %q, want %q", got, "u")
	}
}

func TestBufaTomlEvent_ClearsCaches(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bufaToml := filepath.Join(buildDir, Store.BuildConfigName)
	writeFile(t, bufaToml, "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	writeFile(t, filepath.Join(buildDir, "x.txt"), "a")
	if !waitFor(t, func() bool {
		w.mu.RLock()
		_, ok := w.owningDirCache[Util.NormalizePath(buildDir)]
		w.mu.RUnlock()
		return ok
	}, 2*time.Second) {
		t.Fatal("setup: owningDirCache not populated")
	}

	setHash(t, w, "U", "H")
	setDirty(t, w, "U", "Dtree")

	if err := os.Remove(bufaToml); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, func() bool { return getHash(t, w, "U") == "" }, 2*time.Second) {
		t.Fatal("srcHashCache not cleared after BUFA remove")
	}
	// waitFor, not a one-shot assert: the earlier Write event may still be in flight.
	if !waitFor(t, func() bool { return getDirty(t, w, "U") == "" }, 2*time.Second) {
		t.Fatal("dirtyHashCache not cleared after BUFA remove")
	}
}

func TestConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()

	if got := getInfo(t, w, "U"); got.Found {
		t.Fatalf("empty cache Get returned %+v, want zero", got)
	}
	var env BuildConfig.EnvTable[BuildConfig.EnvVar]
	env.Set("ANTLR_VER", BuildConfig.EnvVar{File: "antlr.ver"})
	env.Set("JAVA_VER", BuildConfig.EnvVar{Value: "26.0.1"})
	want := BuildConfig.BufaConfig{
		BaseConfig: BuildConfig.BaseConfig{
			Env:  env,
			Deps: BuildConfig.Deps{Bld: []string{"../Dep"}, Src: []string{"../Src"}},
		},
		Windows: BuildConfig.BaseConfig{Cmd: BuildConfig.Cmd{Script: "echo win"}},
		Unix:    BuildConfig.BaseConfig{Cmd: BuildConfig.Cmd{Script: "echo unix"}},
	}
	setInfo(t, w, "U", want)
	reply := getInfo(t, w, "U")
	got := reply.Config
	if !reply.Found || got.Windows.Cmd != want.Windows.Cmd ||
		got.Unix.Cmd != want.Unix.Cmd ||
		len(got.Deps.Bld) != 1 || got.Deps.Bld[0] != "../Dep" ||
		len(got.Deps.Src) != 1 || got.Deps.Src[0] != "../Src" ||
		!slices.Equal(envNames(got.Env), []string{"ANTLR_VER", "JAVA_VER"}) ||
		envValue(got.Env, "ANTLR_VER").File != "antlr.ver" || envValue(got.Env, "JAVA_VER").Value != "26.0.1" {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestConfig_InvalidatedOnFileChange(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setInfo(t, w, "U", BuildConfig.BufaConfig{})
	writeFile(t, filepath.Join(buildDir, "x.txt"), "a")

	if !waitFor(t, func() bool { return !getInfo(t, w, "U").Found }, 2*time.Second) {
		t.Fatal("configCache entry \"U\" not invalidated after file change")
	}
}

func TestBuildHash_NotInvalidatedOnFileChange(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setBuild(t, w, "Ccombined", "Dout")
	writeFile(t, filepath.Join(buildDir, "x.txt"), "a")

	// Wait long enough for any invalidation to drain (there should be none).
	time.Sleep(300 * time.Millisecond)
	if got := getBuild(t, w, "Ccombined"); got != "Dout" {
		t.Fatalf("buildHashCache got %q, want Dout (must survive FS events)", got)
	}
}

func TestPathBuildHash_CurrentReportsAndUpdates(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()

	pathBuildHashCurrent := func(combined, srcDir string) bool {
		t.Helper()
		var reply GetBuildHashReply
		if err := w.GetBuildHash(GetBuildHashArgs{Combined: combined, SrcDir: srcDir}, &reply); err != nil {
			t.Fatalf("GetBuildHash: %v", err)
		}
		return reply.PathBuildHashCurrent
	}
	setRoot := func(srcDir, combined string) {
		t.Helper()
		var reply Util.Nothing
		if err := w.SetPathBuildHash(SetPathBuildHashArgs{SrcDir: srcDir, Combined: combined}, &reply); err != nil {
			t.Fatalf("SetPathBuildHash: %v", err)
		}
	}

	// Absent record ⇒ not current, so the daemon can only cause a redundant readback.
	if pathBuildHashCurrent("Bcombined", "U") {
		t.Fatal("absent pathBuildHashCache entry reported current")
	}
	setRoot("U", "Bcombined")
	if !pathBuildHashCurrent("Bcombined", "U") {
		t.Fatal("recorded combined not reported current")
	}
	if pathBuildHashCurrent("Bother", "U") {
		t.Fatal("different combined reported current (would skip a needed re-point)")
	}
	if !pathBuildHashCurrent("Bcombined", "u") {
		t.Fatal("srcDir lookup not normalized")
	}
	setRoot("U", "Bnext")
	if pathBuildHashCurrent("Bcombined", "U") {
		t.Fatal("stale combined still reported current after re-point")
	}
	if !pathBuildHashCurrent("Bnext", "U") {
		t.Fatal("updated combined not reported current")
	}
}

func TestPathBuildHash_PrunedOnDirRename(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "A")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	var reply Util.Nothing
	if err := w.SetPathBuildHash(SetPathBuildHashArgs{SrcDir: "A", Combined: "Bcombined"}, &reply); err != nil {
		t.Fatalf("SetPathBuildHash: %v", err)
	}
	if err := os.Rename(buildDir, filepath.Join(dir, "B")); err != nil {
		t.Fatal(err)
	}

	current := func() bool {
		var r GetBuildHashReply
		if err := w.GetBuildHash(GetBuildHashArgs{Combined: "Bcombined", SrcDir: "A"}, &r); err != nil {
			t.Fatalf("GetBuildHash: %v", err)
		}
		return r.PathBuildHashCurrent
	}
	if !waitFor(t, func() bool { return !current() }, 2*time.Second) {
		t.Fatal("pathBuildHashCache entry for moved dir \"A\" not pruned")
	}
}

func TestBufaTomlEvent_ClearsConfigToo(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bufaToml := filepath.Join(buildDir, Store.BuildConfigName)
	writeFile(t, bufaToml, "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setInfo(t, w, "U", BuildConfig.BufaConfig{})
	setBuild(t, w, "Ccombined", "Dout")

	if err := os.Remove(bufaToml); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, func() bool { return !getInfo(t, w, "U").Found }, 2*time.Second) {
		t.Fatal("configCache not cleared after BUFA remove")
	}
	// buildHashCache survives — content-addressed, not FS-invalidated.
	if got := getBuild(t, w, "Ccombined"); got != "Dout" {
		t.Fatalf("buildHashCache got %q, want Dout after graph change", got)
	}
}

// The fall-through owner invalidation is required on create/remove: the declaration flips
// skipNested on the dir it names.
func TestVirtualConfigEvent_InvalidatesVirtualDir(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "Build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "") // real parent Build/BUFA

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setInfo(t, w, "Build/clj", BuildConfig.BufaConfig{})
	setHash(t, w, "Build/clj", "Dvirtsrc")
	setInfo(t, w, "Build", BuildConfig.BufaConfig{})

	writeFile(t, filepath.Join(buildDir, "clj.BUFA"), "") // declares Build/clj

	if !waitFor(t, func() bool { return !getInfo(t, w, "Build/clj").Found }, 2*time.Second) {
		t.Fatal("virtual config \"Build/clj\" not invalidated after clj.BUFA event")
	}
	if got := getHash(t, w, "Build/clj"); got != "" {
		t.Errorf("virtual src hash not invalidated after clj.BUFA event: got %q", got)
	}
	if !waitFor(t, func() bool { return !getInfo(t, w, "Build").Found }, 2*time.Second) {
		t.Error("declaring dir's owner must be invalidated by the fall-through owner walk")
	}
}

// Regression: the new declaration prunes the real subtree out of Build's staged source, so
// Build's cached src hash must drop.
func TestVirtualConfigCreate_InvalidatesOwnersSource(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	docsDir := filepath.Join(dir, "Build", "docs")
	if err := os.MkdirAll(filepath.Join(docsDir, "md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "Build", Store.BuildConfigName), "") // Build is a build dir
	writeFile(t, filepath.Join(docsDir, "md", "gen.txt"), "content")     // real subtree in Build's source

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "Build", "Dparent")
	setInfo(t, w, "Build", BuildConfig.BufaConfig{})

	writeFile(t, filepath.Join(docsDir, "md.BUFA"), "") // declares Build/docs/md

	if !waitFor(t, func() bool { return getHash(t, w, "Build") == "" }, 2*time.Second) {
		t.Fatal("owner Build's src hash not invalidated: docs/md just left its staged source")
	}
	if getInfo(t, w, "Build").Found {
		t.Error("owner Build's config not invalidated by the docs/md.BUFA declaration")
	}
	if got := w.resolveOwningDir(filepath.Join(docsDir, "md")); got != "build/docs/md" {
		t.Errorf("resolveOwningDir(Build/docs/md) = %q, want \"build/docs/md\"", got)
	}
}

func TestDirCreateAtVirtualPath_InvalidatesVirtualDir(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "Build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "") // real parent Build/BUFA

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setInfo(t, w, "Build/clj", BuildConfig.BufaConfig{VirtualDir: true})
	setHash(t, w, "Build/clj", "Dvirtsrc")

	if err := os.Mkdir(filepath.Join(buildDir, "clj"), 0o755); err != nil { // real dir materializes
		t.Fatal(err)
	}

	if !waitFor(t, func() bool { return !getInfo(t, w, "Build/clj").Found }, 2*time.Second) {
		t.Fatal("virtual config \"Build/clj\" not invalidated by a real dir created at its path")
	}
	if got := getHash(t, w, "Build/clj"); got != "" {
		t.Errorf("virtual src hash not invalidated by a real dir created at its path: got %q", got)
	}
}

func TestDirtySetGet_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()

	if got := getDirty(t, w, "U"); got != "" {
		t.Fatalf("empty dirty cache Get = %q, want \"\"", got)
	}
	setDirty(t, w, filepath.FromSlash("Sub/Dir"), "Dtree")
	if got := getDirty(t, w, "sub/dir"); got != "Dtree" {
		t.Fatalf("dirty hash Get (normalized) = %q, want Dtree", got)
	}

	if got := getSkipHash(t, w, "U"); got != "" {
		t.Fatalf("empty skip-hash cache Get = %q, want \"\"", got)
	}
	setSkipHash(t, w, filepath.FromSlash("Sub/Dir"), "Bfp")
	if got := getSkipHash(t, w, "sub/dir"); got != "Bfp" {
		t.Fatalf("skip hash Get (normalized) = %q, want Bfp", got)
	}
}

func TestDirtyHash_InvalidatedOnFileChange(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(filepath.Join(buildDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setDirty(t, w, "U", "Dtree")
	writeFile(t, filepath.Join(buildDir, "sub", "deep.txt"), "a")

	if !waitFor(t, func() bool { return getDirty(t, w, "U") == "" }, 2*time.Second) {
		t.Fatal("dirty hash \"U\" not invalidated after deep file change")
	}
}

// The skip-hash shadow mirrors an on-disk file that source-tree events never touch.
func TestDirtySkipHash_NotInvalidatedOnFileChange(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "U")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setSkipHash(t, w, "U", "Bfp")
	writeFile(t, filepath.Join(buildDir, "x.txt"), "a")

	// Wait long enough for any invalidation to drain (there should be none).
	time.Sleep(300 * time.Millisecond)
	if got := getSkipHash(t, w, "U"); got != "Bfp" {
		t.Fatalf("dirtySkipHashCache got %q, want Bfp (must survive FS events)", got)
	}
}

// A dirty-materialized virtual dir has no BUFA of its own; without the sibling declaration its
// dirty hash would never invalidate.
func TestDirtyHash_MaterializedVirtualDirOwnsEvents(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "Build")
	virtualDir := filepath.Join(buildDir, "clj")
	if err := os.MkdirAll(virtualDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "") // real parent Build/BUFA
	writeFile(t, filepath.Join(buildDir, "clj.BUFA"), "")            // declares Build/clj

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setDirty(t, w, "Build/clj", "Dtree")
	writeFile(t, filepath.Join(virtualDir, "out.txt"), "built")

	if !waitFor(t, func() bool { return getDirty(t, w, "Build/clj") == "" }, 2*time.Second) {
		t.Fatal("materialized virtual dir \"Build/clj\" not invalidated by its own file change")
	}
}

// A *directory* named BUFA is a contract violation the build side reports — the daemon must not
// panic on it.
func TestFileChange_DirNamedBufaDoesNotPanicDaemon(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "Build")
	cljDir := filepath.Join(buildDir, "clj")
	if err := os.MkdirAll(filepath.Join(cljDir, Store.BuildConfigName), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "") // real parent Build/BUFA

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "Build", "Dtree")
	writeFile(t, filepath.Join(cljDir, "a.txt"), "a")

	if !waitFor(t, func() bool { return getHash(t, w, "Build") == "" }, 2*time.Second) {
		t.Fatal("parent \"Build\" not invalidated by event under clj (dir clj/BUFA must not count, daemon must not panic)")
	}
}

// A *directory* named <suffix>.BUFA does not declare its sibling virtual, so the owning-dir walk
// must attribute the sibling's events to the parent.
func TestFileChange_VirtualConfigDirDoesNotOwnSibling(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "Build")
	cljDir := filepath.Join(buildDir, "clj")
	if err := os.MkdirAll(cljDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(buildDir, "clj.BUFA"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "") // real parent Build/BUFA

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "Build", "Dtree")
	writeFile(t, filepath.Join(cljDir, "a.txt"), "a")

	if !waitFor(t, func() bool { return getHash(t, w, "Build") == "" }, 2*time.Second) {
		t.Fatal("parent \"Build\" not invalidated by event under clj (dir clj.BUFA must not declare it virtual)")
	}
}

func TestVirtualConfigCreate_PrunesOwningDirCache(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "Build")
	cljDir := filepath.Join(buildDir, "clj")
	if err := os.MkdirAll(cljDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "") // real parent Build/BUFA

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	writeFile(t, filepath.Join(cljDir, "a.txt"), "a")
	owningDir := func() (string, bool) {
		w.mu.RLock()
		defer w.mu.RUnlock()
		got, ok := w.owningDirCache[Util.NormalizePath(cljDir)]
		return got, ok
	}
	if !waitFor(t, func() bool { got, ok := owningDir(); return ok && got == "build" }, 2*time.Second) {
		t.Fatal("setup: owner of undeclared clj dir not memoized as \"build\"")
	}

	writeFile(t, filepath.Join(buildDir, "clj.BUFA"), "") // declares Build/clj

	if !waitFor(t, func() bool { _, ok := owningDir(); return !ok }, 2*time.Second) {
		t.Fatal("stale owner mapping for \"Build/clj\" not pruned after clj.BUFA create")
	}

	setDirty(t, w, "Build/clj", "Dtree")
	writeFile(t, filepath.Join(cljDir, "b.txt"), "b")
	if !waitFor(t, func() bool { return getDirty(t, w, "Build/clj") == "" }, 2*time.Second) {
		t.Fatal("declared dir \"Build/clj\" not invalidated by its own file change")
	}
	if !waitFor(t, func() bool { got, ok := owningDir(); return ok && got == "build/clj" }, 2*time.Second) {
		t.Fatal("owner of declared clj dir not re-memoized as \"build/clj\"")
	}
}

func TestFileChange_SiblingBuildDirUntouched(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	a := filepath.Join(dir, "A")
	b := filepath.Join(dir, "B")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a, Store.BuildConfigName), "")
	writeFile(t, filepath.Join(b, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "A", "HA")
	setHash(t, w, "B", "HB")

	writeFile(t, filepath.Join(a, "touched.txt"), "x")

	if !waitFor(t, func() bool { return getHash(t, w, "A") == "" }, 2*time.Second) {
		t.Fatal("A not invalidated")
	}
	if got := getHash(t, w, "B"); got != "HB" {
		t.Fatalf("B cache got %q, want HB (sibling unaffected)", got)
	}
}

func TestDirRename_InvalidatesMovedBuildDir(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	buildDir := filepath.Join(dir, "A")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(buildDir, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "A", "H1")
	// The owner-of-parent walk resolves srcDir, so only the subtree prune clears the "A" entry.
	if err := os.Rename(buildDir, filepath.Join(dir, "B")); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, func() bool { return getHash(t, w, "A") == "" }, 2*time.Second) {
		t.Fatal("moved build dir \"A\" not invalidated")
	}
}

func TestDirRename_InvalidatesNestedBuildDir(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	inner := filepath.Join(dir, "big", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(inner, Store.BuildConfigName), "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setHash(t, w, "big/inner", "H1")
	// The single dir-rename event does not surface the nested BUFA, so the prefix prune clears it.
	if err := os.Rename(filepath.Join(dir, "big"), filepath.Join(dir, "big2")); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, func() bool { return getHash(t, w, "big/inner") == "" }, 2*time.Second) {
		t.Fatal("nested build dir \"big/inner\" under moved container not invalidated")
	}
}

func withStop(w *watcher) func() bool {
	var fired atomicBool
	w.stopMu.Lock()
	w.stop = func() { fired.set() }
	w.stopMu.Unlock()
	return fired.get
}

// Avoids importing sync/atomic for one flag; the mutex is the memory barrier.
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) set() {
	a.mu.Lock()
	a.v = true
	a.mu.Unlock()
}
func (a *atomicBool) get() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.v
}

func TestSrcDirRemove_ShutsDownDaemon(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := settledWatcher(srcDir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(srcDir); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("srcDir removal did not trigger daemon shutdown")
	}
}

func TestSrcDirRename_ShutsDownDaemon(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := settledWatcher(srcDir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Rename(srcDir, filepath.Join(parent, "proj-renamed")); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("srcDir rename did not trigger daemon shutdown")
	}
}

// Injects a synthetic notify event, bypassing the OS watch, for deterministic routing tests.
type fakeEvent struct {
	path  string
	event notify.Event
}

func (f fakeEvent) Event() notify.Event { return f.event }
func (f fakeEvent) Path() string        { return f.path }
func (f fakeEvent) Sys() interface{}    { return nil }

// Regression: srcDir's own Remove/Rename reaches the subtree channel and must route to the
// base-gone shutdown, not the owner walk, which spins forever holding w.mu.
func TestSrcDirSelfEventOnSubtreeChannel_ShutsDownDaemon(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := settledWatcher(srcDir)
	defer w.close()
	stopFired := withStop(w)

	w.invalidatePathCaches(fakeEvent{path: srcDir, event: notify.Rename})

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("srcDir-self event on the subtree channel did not trigger daemon shutdown")
	}
}

func TestSrcDirSelfWriteEvent_Ignored(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := settledWatcher(srcDir)
	defer w.close()
	stopFired := withStop(w)
	setHash(t, w, "sub", "H1")

	w.invalidatePathCaches(fakeEvent{path: srcDir, event: notify.Write})

	time.Sleep(100 * time.Millisecond)
	if stopFired() {
		t.Fatal("srcDir-self Write event triggered shutdown")
	}
	if got := getHash(t, w, "sub"); got != "H1" {
		t.Fatalf("srcDir-self Write event invalidated caches: got %q, want H1", got)
	}
}

// A rename can surface on the parent channel only under its NEW name; checkBaseGone's existence
// probe must still bring the daemon down.
func TestSrcDirRenameNewNameOnly_ShutsDownDaemon(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(srcDir, filepath.Join(parent, "proj-renamed")); err != nil {
		t.Fatal(err)
	}

	// Bare watcher (no notify watchpoints): the real rename must not race the injected event.
	w := &watcher{srcDir: srcDir}
	stopFired := withStop(w)

	w.checkBaseGone(fakeEvent{path: filepath.Join(parent, "proj-renamed"), event: notify.Rename})

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("new-name rename event did not trigger daemon shutdown")
	}
}

func TestSiblingRemoveEvent_NoShutdown(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := &watcher{srcDir: srcDir}
	stopFired := withStop(w)

	w.checkBaseGone(fakeEvent{path: filepath.Join(parent, "sibling"), event: notify.Remove})

	time.Sleep(100 * time.Millisecond)
	if stopFired() {
		t.Fatal("sibling Remove event triggered shutdown while srcDir is alive")
	}
}

// Regression: the walk-up never reached srcDir and spun forever — filepath.Dir is a fixed point
// at the volume root.
func TestResolveOwningDir_OutsideSrcDirTerminates(t *testing.T) {
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := &watcher{srcDir: srcDir}
	got := make(chan string, 1)
	go func() { got <- w.resolveOwningDir(parent) }()

	select {
	case rel := <-got:
		if rel != "." {
			t.Fatalf("resolveOwningDir(parent) = %q, want \".\"", rel)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resolveOwningDir did not terminate for a path outside srcDir")
	}
}

func TestRootConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	w := settledWatcher(dir)
	defer w.close()

	if r := getRoot(t, w); r.Found {
		t.Fatalf("empty cache GetRootConfig Found=true, want false")
	}
	var rootEnv BuildConfig.EnvTable[string]
	rootEnv.Set("VER", "1")
	setRoot(t, w, BuildConfig.RootConfig{
		Unsafe: BuildConfig.RootUnsafe{TTL: 20 * time.Minute},
		Env:    rootEnv,
	})
	r := getRoot(t, w)
	if !r.Found || r.Config.Unsafe.TTL != 20*time.Minute || r.Config.Env.Len() != 1 ||
		envValue(r.Config.Env, "VER") != "1" {
		t.Fatalf("after Set: GetRootConfig = %+v, want Found ttl=20m env VER=1", r)
	}
}

func TestRootConfig_WriteInvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	// The marker must already exist so a later content change is a Write, not a Create.
	marker := filepath.Join(dir, Store.SrcRootFileName)
	writeFile(t, marker, "")

	w := settledWatcher(dir)
	defer w.close()
	time.Sleep(50 * time.Millisecond)

	setRoot(t, w, BuildConfig.RootConfig{Unsafe: BuildConfig.RootUnsafe{TTL: 20 * time.Minute}})
	if !getRoot(t, w).Found {
		t.Fatal("RootConfig not cached after Set")
	}

	writeFile(t, marker, "[unsafe]\nttl = \"30m\"\n")

	if !waitFor(t, func() bool { return !getRoot(t, w).Found }, 2*time.Second) {
		t.Fatal("marker write did not invalidate the RootConfig cache")
	}
}

func TestBufaRootCreate_ShutsDownDaemon(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	writeFile(t, filepath.Join(dir, Store.SrcRootFileName), "")

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("marker create did not trigger daemon shutdown")
	}
}

func TestBufaRootRemove_ShutsDownDaemon(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	marker := filepath.Join(dir, Store.SrcRootFileName)
	writeFile(t, marker, "")

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("marker remove did not trigger daemon shutdown")
	}
}

func TestGitEntryCreate_ShutsDownDaemon(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Mkdir(filepath.Join(dir, Store.GitRootMarker), 0o755); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal(".git create did not trigger daemon shutdown")
	}
}

func TestGitEntryRemove_ShutsDownDaemon(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	gitDir := filepath.Join(dir, "sub", Store.GitRootMarker)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(gitDir); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal(".git remove did not trigger daemon shutdown")
	}
}

// Events inside .git/** have other basenames and must follow the normal path.
func TestGitInnerWrite_NoShutdown(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	gitDir := filepath.Join(dir, Store.GitRootMarker)
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	writeFile(t, filepath.Join(gitDir, "config"), "[core]")

	time.Sleep(100 * time.Millisecond)
	if stopFired() {
		t.Fatal("a write inside .git triggered shutdown")
	}
}

// An unanchored root's identity dissolves with its BUFA; anchored roots just restart to the same answer.
func TestRootOwnBufaRemove_ShutsDownDaemon(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	rootBufa := filepath.Join(dir, Store.BuildConfigName)
	writeFile(t, rootBufa, "")

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(rootBufa); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, stopFired, 2*time.Second) {
		t.Fatal("root's own BUFA remove did not trigger daemon shutdown")
	}
}

// Create cannot change the topmost answer for any cwd under the root: no shutdown, cache clear only.
func TestRootOwnBufaCreate_NoShutdown(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	writeFile(t, filepath.Join(dir, Store.BuildConfigName), "")

	time.Sleep(100 * time.Millisecond)
	if stopFired() {
		t.Fatal("root's own BUFA create triggered shutdown")
	}
}

func TestNestedBufaRemove_NoShutdown(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	nested := filepath.Join(dir, "sub", Store.BuildConfigName)
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nested, "")

	w := settledWatcher(dir)
	defer w.close()
	stopFired := withStop(w)
	time.Sleep(50 * time.Millisecond)

	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)
	if stopFired() {
		t.Fatal("a nested BUFA remove triggered shutdown")
	}
}
