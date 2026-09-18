package Store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

func writeFile(t *testing.T, fsys vfs.Fs, path string, data []byte) {
	t.Helper()
	c.Check(fsys.MkdirAll(filepath.Dir(path), 0o755))
	c.Check(vfs.WriteFile(fsys, path, data, 0o644))
}

func exists(t *testing.T, fsys vfs.Fs, path string) bool {
	t.Helper()
	return vfsx.Exists(fsys, path)
}

func compile(rules ...string) *FilterFiles.Filter { return FilterFiles.Compile(rules) }

var includeAll = compile("+**")

// Shape-valid D/B keys (not the hash of any tree): gc/check recognize entries by the full key shape, not the letter.
func dirKey(seed string) string { return Hashing.DirPrefix + Hashing.HashBytes([]byte(seed)) }

func buildKey(seed string) string { return Hashing.BuildPrefix + Hashing.HashBytes([]byte(seed)) }

func osStore(t *testing.T) (*Store, vfs.Fs) {
	t.Helper()
	root := vfs.NewBasePathFs(vfs.NewOsFs(), t.TempDir())
	return NewStore(root), root
}

// Probes inside tmp/ (a layout root — no foreign top-level entry) and wipes it after, or gc counts a leftover.
func skipIfNoSymlinks(t *testing.T, s *Store) {
	t.Helper()
	err := c.Rescue(func() { s.Link(TmpRoot, "probe", "x") })
	c.Check(s.fs.RemoveAll(TmpRoot))
	if err != nil {
		t.Skipf("symlinks unsupported here (Windows Developer Mode off?): %v", err)
	}
}

func TestStore_RoundTripAndKey(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	writeFile(t, src, filepath.Join("u", "sub", "b.txt"), []byte("B"))

	dst := vfs.NewMemMapFs()
	s := NewStore(dst)
	key := s.Store(InRoot, src, "u", false, nil, true)

	if key != Hashing.HashDir(src, "u") {
		t.Errorf("key = %q, want HashDir of source %q", key, Hashing.HashDir(src, "u"))
	}
	if !s.Contains(InRoot, key) {
		t.Errorf("Contains(%s,%s) = false after publish", InRoot, key)
	}
	if exists(t, dst, TmpRoot) {
		t.Error("tmp dir must be gone after a successful publish")
	}
	if !exists(t, dst, filepath.Join(InRoot, key, "sub", "b.txt")) {
		t.Error("published tree missing nested file")
	}
}

func TestStore_IdempotentSingleEntry(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	s := NewStore(vfs.NewMemMapFs())

	k1 := s.Store(InRoot, src, "u", false, nil, true)
	k2 := s.Store(InRoot, src, "u", false, nil, true)
	if k1 != k2 {
		t.Fatalf("keys differ for identical content: %q vs %q", k1, k2)
	}
	got := c.Check2(vfs.ReadDir(s.fs, InRoot))
	if len(got) != 1 {
		t.Errorf("in/ has %d entries, want exactly 1", len(got))
	}
	if exists(t, s.fs, TmpRoot) {
		t.Error("tmp dir must be gone after idempotent publish")
	}
}

func TestStoreAs_PublishesUnderSuppliedHash(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	s := NewStore(vfs.NewMemMapFs())

	const key = "Dcustom"
	s.StoreAsHash(InRoot, src, "u", false, nil, true, key)

	if !s.Contains(InRoot, key) {
		t.Errorf("Contains(%s,%s) = false after StoreAs", InRoot, key)
	}
	if !exists(t, s.fs, filepath.Join(InRoot, key, "a.txt")) {
		t.Error("StoreAs did not publish staged file under supplied key")
	}
	if exists(t, s.fs, TmpRoot) {
		t.Error("tmp dir must be gone after StoreAs")
	}

	s.StoreAsHash(InRoot, src, "u", false, nil, true, key)
	if exists(t, s.fs, TmpRoot) {
		t.Error("tmp dir must be discarded on idempotent StoreAs")
	}
}

func TestStore_PreexistingKeyDiscardsTmp(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	s := NewStore(vfs.NewMemMapFs())
	key := Hashing.HashDir(src, "u")

	writeFile(t, s.fs, filepath.Join(InRoot, key, "sentinel"), []byte("keep"))

	if got := s.Store(InRoot, src, "u", false, nil, true); got != key {
		t.Errorf("key = %q, want %q", got, key)
	}
	if !exists(t, s.fs, filepath.Join(InRoot, key, "sentinel")) {
		t.Error("pre-existing entry was overwritten")
	}
	if exists(t, s.fs, TmpRoot) {
		t.Error("tmp dir must be discarded when key already present")
	}
}

func TestLink_RoundTripIdempotentAndMisses(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	if got := s.GetLinkTarget(InRoot, "Imissing"); got != "" {
		t.Errorf("LinkTarget on absent name = %q, want \"\"", got)
	}

	s.Link(InRoot, "Iabc", "Dxyz")
	if got := s.GetLinkTarget(InRoot, "Iabc"); got != "Dxyz" {
		t.Errorf("LinkTarget = %q, want \"Dxyz\"", got)
	}
	s.Link(InRoot, "Iabc", "Dxyz")
	if got := s.GetLinkTarget(InRoot, "Iabc"); got != "Dxyz" {
		t.Errorf("after re-Link: %q", got)
	}

	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "Dreal"), 0o755))
	if got := s.GetLinkTarget(InRoot, "Dreal"); got != "" {
		t.Errorf("LinkTarget on a non-symlink = %q, want \"\"", got)
	}
}

// A forced rebuild may publish a different tree under the same B key; the key must follow it.
func TestLink_RepointsChangedTarget(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	s.Link(OutRoot, "Bcombined", "Dold")
	s.Link(OutRoot, "Bcombined", "Dnew")
	if got := s.GetLinkTarget(OutRoot, "Bcombined"); got != "Dnew" {
		t.Errorf("after re-Link with a new target: %q, want \"Dnew\"", got)
	}
}

func TestPathLinkName_NormalizesAndEncodes(t *testing.T) {
	cases := map[string]string{
		"A/b/C":    "∕A∕b∕C", // leading ∕ prefix; casing preserved, "/" -> ∕
		"Build/go": "∕Build∕go",
		".":        "∕", // root build: bare ∕ marker, no trailing "."
	}
	for in, want := range cases {
		if got := pathToLinkName(in); got != want {
			t.Errorf("pathLinkName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkPath_CreatesAndRepoints(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, "Dh1"), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, "Dh2"), 0o755))

	s.LinkPath(OutRoot, "a/b/c", "Dh1")
	if got := s.GetLinkTarget(OutRoot, "∕a∕b∕c"); got != "Dh1" {
		t.Fatalf("LinkPath target = %q, want %q", got, "Dh1")
	}

	s.LinkPath(OutRoot, "a/b/c", "Dh2")
	if got := s.GetLinkTarget(OutRoot, "∕a∕b∕c"); got != "Dh2" {
		t.Fatalf("after re-point: %q, want %q", got, "Dh2")
	}

	s.LinkPath(OutRoot, "a/b/c", "Dh2")
	if got := s.GetLinkTarget(OutRoot, "∕a∕b∕c"); got != "Dh2" {
		t.Fatalf("after idempotent re-link: %q, want %q", got, "Dh2")
	}
}

// Regression: on a Contains hit SrcPrep must still re-point the in/∕ GC root, or GC treats the
// intervening hash as live and reclaims the genuinely current one.
func TestSrcPrep_RefreshesPathLinkOnHit(t *testing.T) {
	s, fsys := osStore(t)
	skipIfNoSymlinks(t, s)

	prep := func(data string) string {
		writeFile(t, fsys, filepath.Join("u", "a.txt"), []byte(data))
		return s.SrcPrep(fsys, "u", includeAll, true)
	}

	hashA := prep("A")
	if got := s.GetLinkTarget(InRoot, "∕u"); got != hashA {
		t.Fatalf("after A: in/∕u = %q, want %q", got, hashA)
	}

	hashB := prep("B")
	if hashB == hashA {
		t.Fatal("hash did not change with content")
	}
	if got := s.GetLinkTarget(InRoot, "∕u"); got != hashB {
		t.Fatalf("after B: in/∕u = %q, want %q", got, hashB)
	}

	if hashA2 := prep("A"); hashA2 != hashA {
		t.Fatalf("revert hash = %q, want %q", hashA2, hashA)
	}
	if got := s.GetLinkTarget(InRoot, "∕u"); got != hashA {
		t.Fatalf("after revert: in/∕u = %q, want %q (stale link not refreshed)", got, hashA)
	}
}

func TestRestore_CopiesAndContracts(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	writeFile(t, src, filepath.Join("u", "d", "b.txt"), []byte("B"))
	s := NewStore(vfs.NewMemMapFs())
	key := s.Store(OutRoot, src, "u", false, nil, true)

	s.Restore(OutRoot, key, "dest")
	if !exists(t, s.fs, filepath.Join("dest", "d", "b.txt")) {
		t.Error("Restore did not copy nested file")
	}

	if err := c.Rescue(func() { s.Restore(OutRoot, "Dnope", "dest2") }); err == nil {
		t.Error("Restore of missing key must panic")
	}

	writeFile(t, s.fs, filepath.Join("nonempty", "x"), []byte("x"))
	if err := c.Rescue(func() { s.Restore(OutRoot, key, "nonempty") }); err == nil {
		t.Error("Restore into non-empty dir must panic")
	}
}

func TestRestoreLink_LinksToContent(t *testing.T) {
	s, fsys := osStore(t)
	skipIfNoSymlinks(t, s)
	writeFile(t, fsys, filepath.Join("u", "a.txt"), []byte("A"))
	key := s.Store(OutRoot, fsys, "u", false, nil, true)

	dst := filepath.Join("bld", "deep", "tool")
	s.RestoreLink(OutRoot, key, dst)
	if !exists(t, fsys, filepath.Join(dst, "a.txt")) {
		t.Error("store content not readable through the link")
	}
	if got := s.GetLinkTarget(filepath.Join("bld", "deep"), "tool"); got != key {
		t.Errorf("GetLinkTarget = %q, want the content key %q", got, key)
	}

	if err := c.Rescue(func() { s.RestoreLink(OutRoot, key, dst) }); err == nil {
		t.Error("RestoreLink onto an occupied path must panic")
	}
	if err := c.Rescue(func() { s.RestoreLink(OutRoot, "Dnope", "elsewhere") }); err == nil {
		t.Error("RestoreLink of a missing key must panic")
	}
}

func TestMoveStore_PrunedSymlinkRemovedAsLinkObject(t *testing.T) {
	s, fsys := osStore(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, fsys, filepath.Join("dep", "payload.txt"), []byte("DEP"))
	depKey := s.Store(OutRoot, fsys, "dep", false, nil, true)

	writeFile(t, fsys, filepath.Join("stage", "own.txt"), []byte("OWN"))
	s.RestoreLink(OutRoot, depKey, filepath.Join("stage", "tool"))

	pruneDirs := Util.NewSet[string]()
	pruneDirs.Add("tool")
	// The pruned link must die by pruneDirs set membership, not by kind accident.
	key := s.MoveStore(OutRoot, "stage", PublishPlan{PruneDirs: pruneDirs, AllowLinks: true})
	if exists(t, fsys, filepath.Join(OutRoot, key, "tool")) {
		t.Error("pruned link must not publish")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "own.txt")) {
		t.Error("own output must publish")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, depKey, "payload.txt")) {
		t.Error("store tree behind the pruned link must survive the publish")
	}
}

func buildUnitTree(t *testing.T, fsys vfs.Fs) {
	t.Helper()
	writeFile(t, fsys, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, fsys, filepath.Join("u", "BUFA"), []byte("[windows]"))
	writeFile(t, fsys, filepath.Join("u", "nested", "BUFA"), []byte("[unix]"))
	writeFile(t, fsys, filepath.Join("u", "nested", "gen.txt"), []byte("NESTED"))
}

func TestSourceFilter_HashSrcAndCopyTreeAgree(t *testing.T) {
	src := vfs.NewMemMapFs()
	buildUnitTree(t, src)

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, want, filepath.Join("u", "BUFA"), []byte("[windows]"))

	s := NewStore(vfs.NewMemMapFs())
	if got := s.HashSrc(src, "u", includeAll, true); got != Hashing.HashDir(want, "u") {
		t.Errorf("HashSrc(filtered) = %q, want %q", got, Hashing.HashDir(want, "u"))
	}

	dst := vfs.NewMemMapFs()
	NewStore(dst).copyTree(src, "u", "staged", true, includeAll, true)
	if !exists(t, dst, filepath.Join("staged", "own.txt")) ||
		!exists(t, dst, filepath.Join("staged", "BUFA")) {
		t.Error("filtered copy dropped the unit's own files")
	}
	if exists(t, dst, filepath.Join("staged", "nested")) {
		t.Error("filtered copy kept a pruned subtree")
	}
	if got := Hashing.HashDir(dst, "staged"); got != Hashing.HashDir(want, "u") {
		t.Errorf("staged tree hash %q != filtered source hash %q", got, Hashing.HashDir(want, "u"))
	}

	raw := vfs.NewMemMapFs()
	NewStore(raw).copyTree(src, "u", "all", false, nil, true)
	if !exists(t, raw, filepath.Join("all", "nested", "gen.txt")) {
		t.Error("unfiltered copy must be verbatim")
	}
}

// pruneNested without a filter is unsupported — src staging always carries one.
func TestSourceFilter_PruneNestedRequiresFilter(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "own.txt"), []byte("OWN"))
	s := NewStore(vfs.NewMemMapFs())

	if err := c.Rescue(func() { s.HashSrc(src, "u", nil, true) }); err == nil {
		t.Error("HashSrc with nil filter must panic (pruneNested requires a filter)")
	}
	if err := c.Rescue(func() { s.SrcPrep(src, "u", nil, true) }); err == nil {
		t.Error("SrcPrep with nil filter must panic (pruneNested requires a filter)")
	}
	if err := c.Rescue(func() { NewStore(vfs.NewMemMapFs()).copyTree(src, "u", "x", false, nil, false) }); err == nil {
		t.Error("copyTree with nil filter and the no-links policy must panic")
	}
}

// Bare .BUFA lost its degenerate-build-root silent prune when it became the source-root marker.
func TestSourceFilter_BareBufaDirPanicsCaseInsensitively(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, src, filepath.Join("u", BuildConfigName), []byte("[windows]"))
	writeFile(t, src, filepath.Join("u", ".Bufa", "junk"), []byte("JUNK"))

	err := c.Rescue(func() { NewStore(vfs.NewMemMapFs()).copyTree(src, "u", "staged", true, includeAll, true) })
	if err == nil {
		t.Error("a bare .BUFA dir must panic like any *.BUFA dir")
	} else if !strings.Contains(err.Error(), "build root") {
		t.Errorf("panic should hint at a leftover build root, got: %v", err)
	}
}

func TestSourceFilter_PrunesNestedBufaRoot(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, src, filepath.Join("u", BuildConfigName), []byte("[windows]"))
	writeFile(t, src, filepath.Join("u", "foreign", SrcRootFileName), nil)
	writeFile(t, src, filepath.Join("u", "foreign", "leak.txt"), []byte("LEAK"))

	dst := vfs.NewMemMapFs()
	NewStore(dst).copyTree(src, "u", "staged", true, includeAll, true)
	if exists(t, dst, filepath.Join("staged", "foreign")) {
		t.Error("nested source-root-marker subtree must be pruned")
	}

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, want, filepath.Join("u", BuildConfigName), []byte("[windows]"))
	s := NewStore(vfs.NewMemMapFs())
	if got := s.HashSrc(src, "u", includeAll, true); got != Hashing.HashDir(want, "u") {
		t.Errorf("HashSrc with nested marker = %q, want %q (foreign subtree pruned)", got, Hashing.HashDir(want, "u"))
	}
}

// A directory squatting on a reserved config name must panic loudly, not be pruned silently.
func TestSourceFilter_DirNamedConfigPanics(t *testing.T) {
	for _, reserved := range []string{BuildConfigName, SrcRootFileName} {
		src := vfs.NewMemMapFs()
		writeFile(t, src, filepath.Join("u", "own.txt"), []byte("OWN"))
		c.Check(src.MkdirAll(filepath.Join("u", "sub", reserved), 0o755))
		s := NewStore(vfs.NewMemMapFs())

		err := c.Rescue(func() { s.HashSrc(src, "u", includeAll, true) })
		if err == nil {
			t.Errorf("HashSrc with a directory named %s must panic (reserved name)", reserved)
		} else if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("panic for directory named %s should name the reservation, got: %v", reserved, err)
		}
	}
}

// Real OS fs: MemMapFs's dir-read behavior differs from the OS's.
func TestTryReadSmallFileFast_DirPanics(t *testing.T) {
	fsys := vfs.NewBasePathFs(vfs.NewOsFs(), t.TempDir())
	c.Check(fsys.MkdirAll(BuildConfigName, 0o755))

	err := c.Rescue(func() { vfsx.TryReadSmallFileFast(fsys, BuildConfigName) })
	if err == nil {
		t.Fatal("TryReadSmallFileFast on a directory must panic (expected a file)")
	}
	if !strings.Contains(err.Error(), "Expected a file") {
		t.Errorf("panic should say a file was expected, got: %v", err)
	}
}

// The reserved-store-name tripwire: after a root relocates upward, the old store sits inside the
// new tree — silent pruning would hide the poison, so it hard-fails instead.
func TestSourceFilter_BufaSuffixedDirPanics(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, src, filepath.Join("u", BuildConfigName), []byte("[windows]"))
	writeFile(t, src, filepath.Join("u", "Foreign.BUFA", "out", "junk"), []byte("JUNK"))

	err := c.Rescue(func() { NewStore(vfs.NewMemMapFs()).copyTree(src, "u", "staged", true, includeAll, true) })
	if err == nil {
		t.Error("staging over a *.BUFA dir must panic (reserved store name)")
	} else if !strings.Contains(err.Error(), "build root") {
		t.Errorf("panic should hint at a leftover build root, got: %v", err)
	}
	if err := c.Rescue(func() { NewStore(vfs.NewMemMapFs()).HashSrc(src, "u", includeAll, true) }); err == nil {
		t.Error("hashing over a *.BUFA dir must panic (reserved store name)")
	}
}

func TestSourceFilter_PrunesMaterializedVirtualDir(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, src, filepath.Join("u", BuildConfigName), []byte("[windows]"))
	writeFile(t, src, filepath.Join("u", "clj"+VirtualConfigSuffix), []byte("[windows]"))
	writeFile(t, src, filepath.Join("u", "clj", "gen.txt"), []byte("DIRTY-OUTPUT"))
	writeFile(t, src, filepath.Join("u", SrcRootFileName), nil)
	writeFile(t, src, filepath.Join("u", "root"+VirtualConfigSuffix), nil)
	writeFile(t, src, filepath.Join("u", "root", "gen.txt"), []byte("DIRTY-OUTPUT"))

	dst := vfs.NewMemMapFs()
	NewStore(dst).copyTree(src, "u", "staged", true, includeAll, true)
	if exists(t, dst, filepath.Join("staged", "clj")) {
		t.Error("materialized virtual dir must be pruned")
	}
	if !exists(t, dst, filepath.Join("staged", "clj"+VirtualConfigSuffix)) {
		t.Error("the declaring config file itself must not be structurally pruned")
	}
	if exists(t, dst, filepath.Join("staged", "root")) {
		t.Error("a root-named dir is virtual-declarable like any other since the marker rename")
	}

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, want, filepath.Join("u", BuildConfigName), []byte("[windows]"))
	writeFile(t, want, filepath.Join("u", "clj"+VirtualConfigSuffix), []byte("[windows]"))
	writeFile(t, want, filepath.Join("u", SrcRootFileName), nil)
	writeFile(t, want, filepath.Join("u", "root"+VirtualConfigSuffix), nil)
	s := NewStore(vfs.NewMemMapFs())
	if got := s.HashSrc(src, "u", includeAll, true); got != Hashing.HashDir(want, "u") {
		t.Errorf("HashSrc with materialized virtual dir = %q, want %q (dir pruned)", got, Hashing.HashDir(want, "u"))
	}
}

func TestDirtySkipHash_RoundTrip(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())

	if got := s.GetDirtySkipHash("u"); got != "" {
		t.Fatalf("missing skip hash = %q, want empty", got)
	}
	s.SetDirtySkipHash("u", "Bfirst")
	if got := s.GetDirtySkipHash("u"); got != "Bfirst" {
		t.Errorf("skip hash = %q, want Bfirst", got)
	}
	s.SetDirtySkipHash("u", "Bsecond")
	if got := s.GetDirtySkipHash("u"); got != "Bsecond" {
		t.Errorf("overwritten skip hash = %q, want Bsecond", got)
	}

	s.SetDirtySkipHash(filepath.Join("A", "b"), "Bnested")
	if !exists(t, s.fs, filepath.Join(DirtyRoot, "∕A∕b")) {
		t.Error("nested skip hash must use the ∕-encoded, casing-preserved name")
	}
	s.SetDirtySkipHash(".", "Broot")
	if got := s.GetDirtySkipHash("."); got != "Broot" {
		t.Errorf("root skip hash = %q, want Broot", got)
	}
	if !exists(t, s.fs, filepath.Join(DirtyRoot, PathLinkPrefix)) {
		t.Error("root skip hash must be the bare ∕ marker")
	}
}

func TestGC_DirtySkipHashes(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())
	s.SetDirtySkipHash("live", "Blive")
	s.SetDirtySkipHash("gone", "Bgone")

	st := s.GC(func(path string) bool { return path == "live" }, false)
	if st.StaleDirtySkipHashes != 1 {
		t.Errorf("StaleDirtySkipHashes = %d, want 1", st.StaleDirtySkipHashes)
	}
	if got := s.GetDirtySkipHash("live"); got != "Blive" {
		t.Errorf("live skip hash collected: %q, want Blive", got)
	}
	if got := s.GetDirtySkipHash("gone"); got != "" {
		t.Errorf("stale skip hash survived: %q, want empty", got)
	}
}

func TestSourceFilter_UserFilterDropsFilesAndAgrees(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "keep.txt"), []byte("KEEP"))
	writeFile(t, src, filepath.Join("u", "README.md"), []byte("DOC"))
	writeFile(t, src, filepath.Join("u", BuildConfigName), []byte("[windows]"))

	f := compile("-*.md")

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "keep.txt"), []byte("KEEP"))
	writeFile(t, want, filepath.Join("u", BuildConfigName), []byte("[windows]"))

	s := NewStore(vfs.NewMemMapFs())
	if got := s.HashSrc(src, "u", f, true); got != Hashing.HashDir(want, "u") {
		t.Errorf("HashSrc(filtered) = %q, want %q", got, Hashing.HashDir(want, "u"))
	}

	dst := vfs.NewMemMapFs()
	NewStore(dst).copyTree(src, "u", "staged", true, f, true)
	if exists(t, dst, filepath.Join("staged", "README.md")) {
		t.Error("filter did not drop README.md")
	}
	if !exists(t, dst, filepath.Join("staged", "keep.txt")) {
		t.Error("filter dropped a kept file")
	}
	if got := Hashing.HashDir(dst, "staged"); got != Hashing.HashDir(want, "u") {
		t.Errorf("staged tree hash %q != filtered source hash %q", got, Hashing.HashDir(want, "u"))
	}
}

func TestSourceFilter_PrunesEmptyDir(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "keep.txt"), []byte("KEEP"))
	writeFile(t, src, filepath.Join("u", "docs", "readme.md"), []byte("DOC"))
	writeFile(t, src, filepath.Join("u", BuildConfigName), []byte("[windows]"))

	f := compile("-*.md")

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "keep.txt"), []byte("KEEP"))
	writeFile(t, want, filepath.Join("u", BuildConfigName), []byte("[windows]"))

	s := NewStore(vfs.NewMemMapFs())
	if got := s.HashSrc(src, "u", f, true); got != Hashing.HashDir(want, "u") {
		t.Errorf("HashSrc = %q, want %q (empty docs/ pruned)", got, Hashing.HashDir(want, "u"))
	}

	dst := vfs.NewMemMapFs()
	NewStore(dst).copyTree(src, "u", "staged", true, f, true)
	if exists(t, dst, filepath.Join("staged", "docs")) {
		t.Error("empty docs/ dir must be pruned from staged tree")
	}
	if got := Hashing.HashDir(dst, "staged"); got != Hashing.HashDir(want, "u") {
		t.Errorf("staged hash %q != reference %q", got, Hashing.HashDir(want, "u"))
	}
}

func TestCopyTree_VerbatimKeepsEmptyDir(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	c.Check(src.MkdirAll(filepath.Join("u", "empty"), 0o755))

	dst := vfs.NewMemMapFs()
	NewStore(dst).copyTree(src, "u", "all", false, nil, true)
	if !exists(t, dst, filepath.Join("all", "empty")) {
		t.Error("verbatim copy must keep a genuinely-empty dir")
	}
}

func TestStore_BldIncludeFilter(t *testing.T) {
	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "gen", "out.txt"), []byte("OUT"))
	writeFile(t, src, filepath.Join("u", "BUFA.cmd"), []byte("script"))
	writeFile(t, src, filepath.Join("u", "scratch", "tmp.bin"), []byte("TMP"))

	f := compile("+/gen/")

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "gen", "out.txt"), []byte("OUT"))

	s := NewStore(vfs.NewMemMapFs())
	key := s.Store(OutRoot, src, "u", false, f, true)
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("bld key = %q, want %q (only gen/ kept)", key, Hashing.HashDir(want, "u"))
	}
	if exists(t, s.fs, filepath.Join(OutRoot, key, "BUFA.cmd")) {
		t.Error("bld filter must drop BUFA.cmd")
	}
	if exists(t, s.fs, filepath.Join(OutRoot, key, "scratch")) {
		t.Error("bld filter must drop the emptied scratch/ dir")
	}
	if !exists(t, s.fs, filepath.Join(OutRoot, key, "gen", "out.txt")) {
		t.Error("bld filter dropped gen/ contents")
	}
}

func TestMove_UnfilteredPublishesAndRemovesSource(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("bld", "u", "a.txt"), []byte("A"))
	c.Check(fsys.MkdirAll(filepath.Join("bld", "u", "empty"), 0o755))

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "a.txt"), []byte("A"))
	c.Check(want.MkdirAll(filepath.Join("u", "empty"), 0o755))

	s := NewStore(fsys)
	key := s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{AllowLinks: true})
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("move key = %q, want %q", key, Hashing.HashDir(want, "u"))
	}
	if exists(t, fsys, filepath.Join("bld", "u")) {
		t.Error("Move must remove the source tree after publishing")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "a.txt")) {
		t.Error("Move did not publish the file")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "empty")) {
		t.Error("unfiltered Move must preserve empty dirs")
	}
	if exists(t, fsys, TmpRoot) {
		t.Error("Move must not leave tmp behind")
	}
}

func TestPathFromLinkName_RoundTrips(t *testing.T) {
	cases := map[string]string{
		"A/b/C": "A/b/C", // casing preserved, ∕ re-decoded to /
		".":     ".",     // bare ∕ marker
	}
	for in, want := range cases {
		if got := pathFromLinkName(pathToLinkName(in)); got != want {
			t.Errorf("pathFromLinkName(pathToLinkName(%q)) = %q, want %q", in, got, want)
		}
	}
}

func TestVirtualConfigBijection(t *testing.T) {
	if got := VirtualConfigPathForDir("."); got != "" {
		t.Errorf("VirtualConfigPathForDir(\".\") = %q, want \"\"", got)
	}
	// Bare BUFA is shorter than the suffix; bare .BUFA is the marker — its empty suffix never decodes.
	for _, name := range []string{"BUFA", ".BUFA", ".bufa", "BUFA.clj"} {
		if got := VirtualSuffix(name); got != "" {
			t.Errorf("VirtualSuffix(%q) = %q, want \"\"", name, got)
		}
	}
	// "root" lost its reservation when the marker was renamed root.BUFA -> .BUFA.
	for name, want := range map[string]string{"clj.BUFA": "clj", "root.BUFA": "root"} {
		if got := VirtualSuffix(name); got != want {
			t.Errorf("VirtualSuffix(%q) = %q, want %q", name, got, want)
		}
	}
	if got, want := VirtualConfigPathForDir(filepath.Join("Build", "clj")), filepath.Join("Build", "clj.BUFA"); got != want {
		t.Errorf("VirtualConfigPathForDir(Build/clj) = %q, want %q", got, want)
	}
	if got, want := VirtualConfigPathForDir("root"), "root"+VirtualConfigSuffix; got != want {
		t.Errorf("VirtualConfigPathForDir(root) = %q, want %q", got, want)
	}
}

// BuildRootDirSuffix and VirtualConfigSuffix are distinct naming choices whose values must
// coincide: skipNested and Build's srcFiltersOverride both assume it.
func TestVirtualConfigSuffix_CouplesToBuildRootDirSuffix(t *testing.T) {
	if VirtualConfigSuffix != BuildRootDirSuffix {
		t.Errorf("VirtualConfigSuffix = %q, BuildRootDirSuffix = %q — skipNested and Build's srcFiltersOverride assume one *.BUFA namespace; change them together with the constants",
			VirtualConfigSuffix, BuildRootDirSuffix)
	}
}

// Nothing else drops the source-root marker from the source root's own staged source.
func TestSrcRootFileName_CouplesToVirtualConfigSuffix(t *testing.T) {
	if !strings.HasSuffix(SrcRootFileName, VirtualConfigSuffix) {
		t.Errorf("SrcRootFileName = %q does not end with VirtualConfigSuffix %q — Build's srcFiltersOverride assumes one *.BUFA namespace; change it together with the constants",
			SrcRootFileName, VirtualConfigSuffix)
	}
}

func TestGC_InAndOut(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	live := dirKey("live")
	orphan := dirKey("orphan")
	old := dirKey("old")
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, live), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, orphan), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "Dnotakey"), 0o755)) // D-prefixed non-key: not content, gc leaves it
	s.LinkPath(InRoot, "kept", live)
	s.LinkPath(InRoot, "gone", orphan)

	bKept := buildKey("kept")
	bOld := buildKey("old")
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, live), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, old), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, "Bnotakey"), 0o755)) // B-prefixed non-key: not a build link, gc leaves it
	s.Link(OutRoot, bKept, live)
	s.Link(OutRoot, bOld, old)
	s.LinkPath(OutRoot, "kept", bKept)
	s.LinkPath(OutRoot, "gone", bOld)

	st := s.GC(func(path string) bool { return path == "kept" }, false)

	if st != (GCStats{StalePathLinks: 2, OrphanContent: 2, OrphanBuildLinks: 1}) {
		t.Errorf("GCStats = %+v, want {2,2,1}", st)
	}

	if exists(t, s.fs, filepath.Join(InRoot, "∕gone")) {
		t.Error("in/∕gone (stale) must be removed")
	}
	if exists(t, s.fs, filepath.Join(InRoot, orphan)) {
		t.Error("in/<orphan> must be removed")
	}
	if !exists(t, s.fs, filepath.Join(InRoot, live)) || s.GetLinkTarget(InRoot, "∕kept") != live {
		t.Error("in/<live> and in/∕kept must survive")
	}
	if !exists(t, s.fs, filepath.Join(InRoot, "Dnotakey")) {
		t.Error("in/Dnotakey is not a content key: check's to report, not gc's to collect")
	}

	if exists(t, s.fs, filepath.Join(OutRoot, old)) {
		t.Error("out/<old> (orphan) must be removed")
	}
	if exists(t, s.fs, filepath.Join(OutRoot, bOld)) {
		t.Error("out/<bOld> (unrooted) must be removed")
	}
	if s.GetLinkTarget(OutRoot, "∕kept") != bKept || s.GetLinkTarget(OutRoot, bKept) != live {
		t.Error("out/∕kept -> <bKept> -> <live> chain must survive")
	}
	if !exists(t, s.fs, filepath.Join(OutRoot, live)) {
		t.Error("out/<live> must survive")
	}
	if !exists(t, s.fs, filepath.Join(OutRoot, "Bnotakey")) {
		t.Error("out/Bnotakey is not a build key: check's to report, not gc's to collect")
	}
}

func TestGC_OutSupersededBuildLinkKeepsSharedContent(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	shared := dirKey("shared")
	bKept := buildKey("kept")
	bStale := buildKey("stale")
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, shared), 0o755))
	s.Link(OutRoot, bKept, shared)
	s.Link(OutRoot, bStale, shared)
	s.LinkPath(OutRoot, "kept", bKept)  // live build dir
	s.LinkPath(OutRoot, "gone", bStale) // superseded: source path no longer a build dir

	st := s.GC(func(path string) bool { return path == "kept" }, false)

	if st != (GCStats{StalePathLinks: 1, OrphanBuildLinks: 1}) {
		t.Errorf("GCStats = %+v, want {1,0,1}", st)
	}
	if exists(t, s.fs, filepath.Join(OutRoot, bStale)) {
		t.Error("superseded out/<bStale> must be reclaimed")
	}
	if !exists(t, s.fs, filepath.Join(OutRoot, shared)) {
		t.Error("shared out/<shared> must survive (still rooted via <bKept>)")
	}
	if s.GetLinkTarget(OutRoot, bKept) != shared {
		t.Error("out/<bKept> -> <shared> must survive")
	}
}

// Regression: ∕ link names must preserve casing, or a case-exact srcPresent rejects the decoded
// path and collects the live root on every GC pass.
func TestGC_PathLinkCasingPreserved(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	live := dirKey("live")
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, live), 0o755))
	s.LinkPath(InRoot, "Build/clj", live)

	st := s.GC(func(path string) bool { return path == "Build/clj" }, false)

	if st != (GCStats{}) {
		t.Errorf("GCStats = %+v, want zero (live mixed-case root must survive)", st)
	}
	if s.GetLinkTarget(InRoot, "∕Build∕clj") != live {
		t.Error("in/∕Build∕clj must survive with casing intact")
	}
}

func TestGC_MissingRootsNoop(t *testing.T) {
	s, _ := osStore(t)
	if st := s.GC(func(string) bool { return true }, false); st != (GCStats{}) {
		t.Errorf("GC on empty store = %+v, want zero stats", st)
	}
}

// A bld/ leftover holds link-staged deps pointing into store content: RemoveAll must drop the
// link objects, never the trees behind them.
func TestGC_LeftoverScratchDirs(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	live := dirKey("live")
	bKept := buildKey("kept")
	writeFile(t, s.fs, filepath.Join(OutRoot, live, "a.txt"), []byte("A"))
	s.Link(OutRoot, bKept, live)
	s.LinkPath(OutRoot, "kept", bKept)

	genFile := filepath.Join(BldSandboxRoot, "kept", "gen.txt")
	writeFile(t, s.fs, genFile, []byte("G"))
	c.Check(s.fs.Chmod(genFile, 0o444))
	s.RestoreLink(OutRoot, live, filepath.Join(BldSandboxRoot, "kept", "dep"))
	writeFile(t, s.fs, filepath.Join(TmpRoot, "BUFA.cmd"), []byte("script"))

	st := s.GC(func(path string) bool { return path == "kept" }, false)

	if st != (GCStats{LeftoverScratchDirs: 2}) {
		t.Errorf("GCStats = %+v, want {LeftoverScratchDirs: 2}", st)
	}
	if exists(t, s.fs, BldSandboxRoot) || exists(t, s.fs, TmpRoot) {
		t.Error("bld/ and tmp/ leftovers must be removed")
	}
	if !exists(t, s.fs, filepath.Join(OutRoot, live, "a.txt")) {
		t.Error("store content behind the link-staged dep must survive")
	}
}

// Only regular files inside removed trees count: the link-staged dep is a link object, its
// target lives on; the live tree isn't removed at all.
func TestGC_BytesFreed(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	live := dirKey("live")
	orphan := dirKey("orphan")
	writeFile(t, s.fs, filepath.Join(InRoot, live, "a.txt"), []byte("AAAA"))
	writeFile(t, s.fs, filepath.Join(InRoot, orphan, "b.txt"), []byte("BBB"))
	writeFile(t, s.fs, filepath.Join(InRoot, orphan, "sub", "c.txt"), []byte("CCCCC"))
	s.LinkPath(InRoot, "kept", live)
	writeFile(t, s.fs, filepath.Join(BldSandboxRoot, "kept", "gen.txt"), []byte("GG"))
	s.RestoreLink(InRoot, live, filepath.Join(BldSandboxRoot, "kept", "dep"))
	writeFile(t, s.fs, filepath.Join(TmpRoot, "BUFA.cmd"), []byte("script"))

	st := s.GC(func(path string) bool { return path == "kept" }, true)

	want := GCStats{OrphanContent: 1, LeftoverScratchDirs: 2, BytesFreed: 3 + 5 + 2 + 6}
	if st != want {
		t.Errorf("GCStats = %+v, want %+v", st, want)
	}
	if !exists(t, s.fs, filepath.Join(InRoot, live, "a.txt")) {
		t.Error("live content must survive")
	}
}

// A non-directory squatting on a scratch name is check's unexpected entry, not gc's leftover.
func TestGC_ScratchNameFileLeftForCheck(t *testing.T) {
	s, _ := osStore(t)
	writeFile(t, s.fs, BldSandboxRoot, []byte("squatter"))

	if st := s.GC(func(string) bool { return true }, false); st != (GCStats{}) {
		t.Errorf("GCStats = %+v, want zero", st)
	}
	if !exists(t, s.fs, BldSandboxRoot) {
		t.Error("file named bld must be left for check")
	}
}

func TestMove_BldIncludeFilter(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("bld", "u", "gen", "out.txt"), []byte("OUT"))
	writeFile(t, fsys, filepath.Join("bld", "u", "BUFA.cmd"), []byte("script"))
	writeFile(t, fsys, filepath.Join("bld", "u", "scratch", "tmp.bin"), []byte("TMP"))

	f := compile("+/gen/")

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "gen", "out.txt"), []byte("OUT"))

	s := NewStore(fsys)
	key := s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{Filter: f, AllowLinks: true})
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("move bld key = %q, want %q (only gen/ kept)", key, Hashing.HashDir(want, "u"))
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "BUFA.cmd")) {
		t.Error("Move bld filter must drop BUFA.cmd")
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "scratch")) {
		t.Error("Move bld filter must drop the emptied scratch/ dir")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "gen", "out.txt")) {
		t.Error("Move bld filter dropped gen/ contents")
	}
	if exists(t, fsys, filepath.Join("bld", "u")) {
		t.Error("Move must move the whole (in-place filtered) tree out of bld/")
	}
	if exists(t, fsys, TmpRoot) {
		t.Error("Move must not stage through tmp")
	}
}

func TestMove_PruneDirsNilFilter(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("bld", "u", "out.txt"), []byte("OUT"))
	writeFile(t, fsys, filepath.Join("bld", "u", "tool", "bin.txt"), []byte("BIN"))
	writeFile(t, fsys, filepath.Join("bld", "u", "a", "b", "dep.txt"), []byte("DEP"))

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "out.txt"), []byte("OUT"))

	s := NewStore(fsys)
	prune := Util.Set[string]{"tool": {}, "a/b": {}, "../sibling": {}, "gone": {}}
	key := s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{PruneDirs: prune, AllowLinks: true})
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("move key = %q, want %q (staged deps pruned)", key, Hashing.HashDir(want, "u"))
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "tool")) {
		t.Error("pruneDirs must drop the staged dep dir tool/")
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "a")) {
		t.Error("ancestor a/ left empty by pruning a/b must be dropped")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "out.txt")) {
		t.Error("script output out.txt must survive pruning")
	}
	if exists(t, fsys, filepath.Join("bld", "u")) {
		t.Error("Move must move the whole tree out of bld/")
	}
}

func TestMove_PruneDirsWithFilter(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("bld", "u", "out.txt"), []byte("OUT"))
	writeFile(t, fsys, filepath.Join("bld", "u", "junk.log"), []byte("JUNK"))
	writeFile(t, fsys, filepath.Join("bld", "u", "tool", "bin.txt"), []byte("BIN"))

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "out.txt"), []byte("OUT"))

	s := NewStore(fsys)
	key := s.MoveStore(OutRoot, filepath.Join("bld", "u"),
		PublishPlan{Filter: compile("+**", "-*.log"), PruneDirs: Util.Set[string]{"tool": {}}, AllowLinks: true})
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("move key = %q, want %q (dep pruned + junk filtered)", key, Hashing.HashDir(want, "u"))
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "tool")) {
		t.Error("pruneDirs must drop tool/ alongside the filter")
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "junk.log")) {
		t.Error("filter must drop junk.log alongside the prune")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "out.txt")) {
		t.Error("out.txt must survive")
	}
}

func TestMove_PruneDirsRemovesFileAtPrunedPath(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("bld", "u", "keep.txt"), []byte("K"))
	writeFile(t, fsys, filepath.Join("bld", "u", "tool"), []byte("SCRIPT-MADE"))

	s := NewStore(fsys)
	key := s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{PruneDirs: Util.Set[string]{"tool": {}}, AllowLinks: true})
	if exists(t, fsys, filepath.Join(OutRoot, key, "tool")) {
		t.Error("pruneDirs is kind-blind: a file at a pruned path must not publish")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "keep.txt")) {
		t.Error("ordinary script output must publish")
	}
}

func TestMove_ExtPruneRemovesOccupantAndSweepsParents(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("bld", "u", "out.txt"), []byte("OUT"))
	writeFile(t, fsys, filepath.Join("bld", "u", "libs", "art.bin"), []byte("ART"))
	writeFile(t, fsys, filepath.Join("bld", "u", "gone", "sub", "junk.txt"), []byte("J"))
	c.Check(fsys.MkdirAll(filepath.Join("bld", "u", "empty"), 0o755))

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "out.txt"), []byte("OUT"))
	c.Check(want.MkdirAll(filepath.Join("u", "empty"), 0o755))

	s := NewStore(fsys)
	extPrune := Util.Set[string]{"libs/art.bin": {}, "gone/sub": {}}
	key := s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{ExtPrune: extPrune, AllowLinks: true})
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("move key = %q, want %q", key, Hashing.HashDir(want, "u"))
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "libs")) {
		t.Error("the parent dir the artifact's removal left empty must be swept")
	}
	if exists(t, fsys, filepath.Join(OutRoot, key, "gone")) {
		t.Error("a directory occupying a pruned ext name must die whole, its emptied parent swept")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "empty")) {
		t.Error("extPrune must not drop unrelated empty dirs from a verbatim publish")
	}
	if !exists(t, fsys, filepath.Join(OutRoot, key, "out.txt")) {
		t.Error("script output must survive")
	}
}

func TestIsReservedName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"BUFA", true},
		{"bUfA", true},      // case-insensitive
		{"root.BUFA", true}, // ordinary virtual-config name
		{".BUFA", true},     // the source-root marker; also the build-root dir name
		{"x.BUFA", true},    // virtual-config namespace
		{"x.bufa", true},    // suffix case-insensitive
		{"xBUFA", false},    // no dot — outside the suffix
		{"BUFA.txt", false}, // suffix elsewhere
		{"antlr.jar", false},
		{"", false},
	} {
		if got := IsReservedName(tc.name); got != tc.want {
			t.Errorf("IsReservedName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOSDirEntryExists(t *testing.T) {
	dir := t.TempDir()
	if UnsafeIO.OSDirEntryExists(filepath.Join(dir, "absent")) {
		t.Error("absent path must not exist")
	}
	file := filepath.Join(dir, "f.txt")
	c.Check(os.WriteFile(file, []byte("x"), 0o644))
	if !UnsafeIO.OSDirEntryExists(file) {
		t.Error("regular file must exist")
	}
	sub := filepath.Join(dir, "empty")
	c.Check(os.Mkdir(sub, 0o755))
	if !UnsafeIO.OSDirEntryExists(sub) {
		t.Error("empty directory must exist")
	}
	// The probe's raison d'être: a dangling symlink is an occupant — Lstat sees the link object.
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "gone"), dangling); err != nil {
		t.Skipf("symlinks unsupported here (Windows Developer Mode off?): %v", err)
	}
	if !UnsafeIO.OSDirEntryExists(dangling) {
		t.Error("dangling symlink must count as an occupant")
	}
}

func TestRemoveDirIfEmpty(t *testing.T) {
	// BasePathFs like prod's BldFS — deletions must stay jailed to the root.
	dir := t.TempDir()
	fsys := vfs.NewBasePathFs(vfs.NewOsFs(), dir)
	if vfsx.RemoveDirIfEmpty(fsys, "absent") {
		t.Error("missing path must report false")
	}
	writeFile(t, fsys, "f.txt", []byte("x"))
	if vfsx.RemoveDirIfEmpty(fsys, "f.txt") {
		t.Error("a file must report false")
	}
	if !exists(t, fsys, "f.txt") {
		t.Error("a file must survive untouched")
	}
	writeFile(t, fsys, filepath.Join("full", "kid.txt"), []byte("k"))
	if vfsx.RemoveDirIfEmpty(fsys, "full") {
		t.Error("non-empty dir must report false")
	}
	if !exists(t, fsys, "full") {
		t.Error("non-empty dir must survive untouched")
	}
	c.Check(fsys.Mkdir("empty", 0o755))
	if !vfsx.RemoveDirIfEmpty(fsys, "empty") {
		t.Error("empty dir must be removed and report true")
	}
	if exists(t, fsys, "empty") {
		t.Error("empty dir must be gone")
	}
}
