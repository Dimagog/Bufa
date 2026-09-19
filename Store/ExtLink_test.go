// Tests of the symlink leaf rule at the Store engines (Doc/Bugs/SymlinkHandling.md). Real OS fs.

package Store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

func osStoreAt(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return NewStore(vfs.NewBasePathFs(vfs.NewOsFs(), dir)), dir
}

// A root whose literal spelling is not its EvalSymlinks form, like macOS /var or an 8.3 TEMP.
func osStoreBehindLink(t *testing.T) (*Store, string) {
	t.Helper()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	return NewStore(vfs.NewBasePathFs(vfs.NewOsFs(), link)), link
}

func osSymlink(target, link string) {
	c.Check(os.MkdirAll(filepath.Dir(link), 0o755))
	c.Check(os.Symlink(target, link))
}

func writeArtifact(t *testing.T, data []byte) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "art.bin")
	c.Check(os.WriteFile(target, data, 0o644))
	return target
}

func TestMoveStore_UnsafeFilterKeepsIncludedLinkDropsExcluded(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	target := writeArtifact(t, []byte("artifact-bytes"))

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	osSymlink(target, filepath.Join(root, "bld", "art.bin"))
	osSymlink(target, filepath.Join(root, "bld", "drop.bin"))

	hash := s.MoveStore(OutRoot, "bld", PublishPlan{Filter: compile("+**", "-drop.bin"), AllowLinks: true})

	published := filepath.Join(root, OutRoot, hash, "art.bin")
	info := c.Check2(os.Lstat(published))
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("published art.bin mode %v, want a surviving symlink", info.Mode())
	}
	if exists(t, s.fs, filepath.Join(OutRoot, hash, "drop.bin")) {
		t.Error("excluded link must be removed by the filter pass")
	}
	if got, _ := runCheck(s, false); got.HasProblems() {
		t.Errorf("published linked tree fails check: %+v", got)
	}
}

func TestMoveStore_SafeKeepLinksShipsLinkObject(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	data := []byte("artifact-bytes")
	target := writeArtifact(t, data)

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	osSymlink(target, filepath.Join(root, "bld", "art.bin"))
	keep := Util.NewSet[string]()
	keep.Add("art.bin")
	hash := s.MoveStore(OutRoot, "bld", PublishPlan{KeepLinks: keep})

	info := c.Check2(os.Lstat(filepath.Join(root, OutRoot, hash, "art.bin")))
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("a keepLinks entry must ship as a link object in safe mode")
	}

	writeFile(t, s.fs, filepath.Join("bld2", "keep.txt"), []byte("K"))
	writeFile(t, s.fs, filepath.Join("bld2", "art.bin"), data)
	if twin := s.MoveStore(OutRoot, "bld2", PublishPlan{}); twin != hash {
		t.Errorf("byte-shipped twin key %q != linked key %q", twin, hash)
	}
}

// Safe mode must fail the publish scan before ANY deletion.
func TestMoveStore_SafeForeignLinkFailsBeforeDeleting(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	target := writeArtifact(t, []byte("artifact-bytes"))

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	writeFile(t, s.fs, filepath.Join("bld", "drop.txt"), []byte("D"))
	osSymlink(target, filepath.Join(root, "bld", "rogue.bin"))

	err := c.Rescue(func() {
		s.MoveStore(OutRoot, "bld", PublishPlan{Filter: compile("+**", "-drop.txt")})
	})
	if err == nil {
		t.Fatal("an undeclared link must fail a safe-mode publish")
	}
	if msg := err.Error(); !strings.Contains(msg, "rogue.bin") ||
		!strings.Contains(msg, "[[deps.ext]]") || !strings.Contains(msg, "unsafe") {
		t.Errorf("the error must name the link with the deps.ext/unsafe hint, got %q", msg)
	}
	if !exists(t, s.fs, filepath.Join("bld", "drop.txt")) {
		t.Error("a failed scan must not have deleted the filter-excluded file")
	}
	if !UnsafeIO.OSDirEntryExists(filepath.Join(root, "bld", "rogue.bin")) {
		t.Error("a failed scan must not have deleted the offending link")
	}
	if exists(t, s.fs, OutRoot) {
		t.Error("nothing must publish after a failed scan")
	}
}

func TestMoveStore_SafePolicyScanKeepsEmptyDirs(t *testing.T) {
	s, _ := osStoreAt(t)

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	c.Check(s.fs.MkdirAll(filepath.Join("bld", "empty"), 0o755))

	hash := s.MoveStore(OutRoot, "bld", PublishPlan{})
	if !exists(t, s.fs, filepath.Join(OutRoot, hash, "empty")) {
		t.Error("the safe-mode policy scan must not drop empty dirs from a verbatim publish")
	}
}

func TestMoveStore_UnsafeDirLinkPublishes(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	// The referent lives outside the published tree (under tmp/, which check never enters), so it
	// survives the publish rename.
	writeFile(t, s.fs, filepath.Join(TmpRoot, "payload", "a.txt"), []byte("A"))
	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	osSymlink(filepath.Join(root, TmpRoot, "payload"), filepath.Join(root, "bld", "tool"))

	hash := s.MoveStore(OutRoot, "bld", PublishPlan{AllowLinks: true})

	info := c.Check2(os.Lstat(filepath.Join(root, OutRoot, hash, "tool")))
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("a dir-target link must publish as a link object, not vanish")
	}
	writeFile(t, s.fs, filepath.Join("bld2", "keep.txt"), []byte("K"))
	writeFile(t, s.fs, filepath.Join("bld2", "tool", "a.txt"), []byte("A"))
	if twin := s.MoveStore(OutRoot, "bld2", PublishPlan{AllowLinks: true}); twin != hash {
		t.Errorf("materialized twin key %q != dir-linked key %q", twin, hash)
	}
	if got, _ := runCheck(s, false); got.HasProblems() {
		t.Errorf("published dir-linked tree fails check: %+v", got)
	}
}

// A relative target escaping the tree is absolutized before hashing — the rename would otherwise
// leave the stored tree mismatching its own key.
func TestMoveStore_RelativeTargetEscapingTreeAbsolutized(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join(TmpRoot, "payload", "art.bin"), []byte("P"))
	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	relTarget := filepath.Join("..", TmpRoot, "payload", "art.bin")
	c.Check(os.Symlink(relTarget, filepath.Join(root, "bld", "art.bin")))

	hash := s.MoveStore(OutRoot, "bld", PublishPlan{AllowLinks: true})

	published := filepath.Join(root, OutRoot, hash, "art.bin")
	got := c.Check2(os.Readlink(published))
	if !filepath.IsAbs(got) {
		t.Errorf("escaping relative target must be absolutized, got %q", got)
	}
	if data := c.Check2(os.ReadFile(published)); !bytes.Equal(data, []byte("P")) {
		t.Errorf("published link reads %q, want the referent's bytes", data)
	}
	if st, _ := runCheck(s, false); st.HasProblems() {
		t.Errorf("re-pointed tree fails check: %+v", st)
	}
}

// An intra-tree relative target stays relative: absolutizing would pin it to the doomed
// pre-rename location.
func TestMoveStore_IntraTreeRelativeTargetKept(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join("bld", "sub", "real.txt"), []byte("R"))
	relTarget := filepath.Join("sub", "real.txt")
	c.Check(os.Symlink(relTarget, filepath.Join(root, "bld", "lnk")))

	hash := s.MoveStore(OutRoot, "bld", PublishPlan{Filter: compile("+**"), AllowLinks: true})

	published := filepath.Join(root, OutRoot, hash, "lnk")
	if got := c.Check2(os.Readlink(published)); got != relTarget {
		t.Errorf("intra-tree relative target must survive verbatim, got %q want %q", got, relTarget)
	}
	if data := c.Check2(os.ReadFile(published)); !bytes.Equal(data, []byte("R")) {
		t.Errorf("published link reads %q, want the referent's bytes", data)
	}
	if st, _ := runCheck(s, false); st.HasProblems() {
		t.Errorf("intra-tree-linked store tree fails check: %+v", st)
	}
}

func TestRestore_PreservesLinkObject(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	data := []byte("artifact-bytes")
	target := writeArtifact(t, data)

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	osSymlink(target, filepath.Join(root, "bld", "art.bin"))
	hash := s.MoveStore(OutRoot, "bld", PublishPlan{AllowLinks: true})

	s.Restore(OutRoot, hash, "restored")

	restored := filepath.Join(root, "restored", "art.bin")
	info := c.Check2(os.Lstat(restored))
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("restore must recreate the link object, not materialize bytes")
	}
	if got := c.Check2(os.Readlink(restored)); got != target {
		t.Errorf("restored link targets %q, want %q", got, target)
	}
	if got := c.Check2(os.ReadFile(restored)); !bytes.Equal(got, data) {
		t.Errorf("bytes through restored link = %q, want %q", got, data)
	}
}

func TestStore_UnsafeStagingPreservesLinkObjectSafeErrors(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	data := []byte("artifact-bytes")
	target := writeArtifact(t, data)

	srcRoot := t.TempDir()
	srcFs := vfs.NewBasePathFs(vfs.NewOsFs(), srcRoot)
	writeFile(t, srcFs, filepath.Join("u", "own.txt"), []byte("OWN"))
	c.Check(os.Symlink(target, filepath.Join(srcRoot, "u", "art.bin")))
	writeFile(t, srcFs, filepath.Join("twin", "own.txt"), []byte("OWN"))
	writeFile(t, srcFs, filepath.Join("twin", "art.bin"), data)

	key := s.Store(InRoot, srcFs, "u", true, includeAll, true)
	if twin := Hashing.HashDir(srcFs, "twin"); key != twin {
		t.Errorf("linked source key %q != materialized twin %q", key, twin)
	}
	staged := filepath.Join(root, InRoot, key, "art.bin")
	info := c.Check2(os.Lstat(staged))
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unsafe staging must preserve the link object")
	}
	if got := c.Check2(os.Readlink(staged)); got != target {
		t.Errorf("staged link targets %q, want the absolute %q", got, target)
	}

	for name, walk := range map[string]func(){
		"hash": func() { s.HashSrc(srcFs, "u", includeAll, false) },
		"copy": func() { s.Store(InRoot, srcFs, "u", true, includeAll, false) },
	} {
		err := c.Rescue(walk)
		if err == nil || !strings.Contains(err.Error(), "[[deps.ext]]") {
			t.Errorf("%s walk must fail on a safe-mode source link with the hint, got %v", name, err)
		}
	}
}

func TestCheck_DanglingFileSymlinkReadsCorrupt(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	target := writeArtifact(t, []byte("artifact-bytes"))

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	osSymlink(target, filepath.Join(root, "bld", "art.bin"))
	hash := s.MoveStore(OutRoot, "bld", PublishPlan{AllowLinks: true})

	writeFile(t, s.fs, filepath.Join("bld2", "ok.txt"), []byte("OK"))
	okHash := s.MoveStore(OutRoot, "bld2", PublishPlan{AllowLinks: true})

	c.Check(os.Remove(target)) // cache entry deleted out-of-band

	st, report := runCheck(s, false)
	if st.CorruptContent != 1 {
		t.Errorf("CorruptContent = %d, want 1 (the dangling link must read as corrupt, not crash)", st.CorruptContent)
	}
	if !strings.Contains(report, hash) {
		t.Errorf("report must name the corrupt entry %q, got %q", hash, report)
	}
	if strings.Contains(report, okHash) {
		t.Errorf("the healthy entry %q must verify clean", okHash)
	}
}

// Filter before policy: a filter-excluded link never stages, so safe mode has no say on it.
func TestSafeSource_FilterExcludedLinkBypassesPolicy(t *testing.T) {
	s, _ := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	target := writeArtifact(t, []byte("artifact-bytes"))

	srcRoot := t.TempDir()
	srcFs := vfs.NewBasePathFs(vfs.NewOsFs(), srcRoot)
	writeFile(t, srcFs, filepath.Join("u", "own.txt"), []byte("OWN"))
	writeFile(t, srcFs, filepath.Join("u", ".git", "config"), []byte("C"))
	c.Check(os.Symlink(target, filepath.Join(srcRoot, "u", ".git", "lnk")))
	c.Check(os.Symlink(target, filepath.Join(srcRoot, "u", "drop.lnk")))

	filter := compile("+**", "-.**", "-drop.lnk")
	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("u", "own.txt"), []byte("OWN"))

	if got := s.HashSrc(srcFs, "u", filter, false); got != Hashing.HashDir(want, "u") {
		t.Errorf("safe hash with excluded links = %q, want %q (links skipped, not rejected)", got, Hashing.HashDir(want, "u"))
	}
	key := s.Store(InRoot, srcFs, "u", true, filter, false)
	if key != Hashing.HashDir(want, "u") {
		t.Errorf("safe staging with excluded links = %q, want %q", key, Hashing.HashDir(want, "u"))
	}
	if vfsx.Exists(s.fs, filepath.Join(InRoot, key, "drop.lnk")) || vfsx.Exists(s.fs, filepath.Join(InRoot, key, ".git")) {
		t.Error("excluded links must not stage")
	}
}

func TestMoveStore_SafeFilterExcludedLinkRemovedNotRejected(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)
	target := writeArtifact(t, []byte("artifact-bytes"))

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	osSymlink(target, filepath.Join(root, "bld", "drop.lnk"))

	hash := s.MoveStore(OutRoot, "bld", PublishPlan{Filter: compile("+**", "-*.lnk")})
	if exists(t, s.fs, filepath.Join(OutRoot, hash, "drop.lnk")) {
		t.Error("the excluded link must be removed by the filter, not shipped")
	}
	if !exists(t, s.fs, filepath.Join(OutRoot, hash, "keep.txt")) {
		t.Error("kept output must publish")
	}
}

func TestMoveStore_AbsoluteTargetIntoMovingTreeRejected(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join("bld", "data.txt"), []byte("D"))
	osSymlink(filepath.Join(root, "bld", "data.txt"), filepath.Join(root, "bld", "self.lnk"))

	err := c.Rescue(func() {
		s.MoveStore(OutRoot, "bld", PublishPlan{AllowLinks: true})
	})
	if err == nil || !strings.Contains(err.Error(), "inside the moving output tree") {
		t.Fatalf("a target inside the moving tree must be rejected, got %v", err)
	}
	if !exists(t, s.fs, filepath.Join("bld", "data.txt")) || !UnsafeIO.OSDirEntryExists(filepath.Join(root, "bld", "self.lnk")) {
		t.Error("a failed scan must leave the sandbox intact")
	}
	if exists(t, s.fs, OutRoot) {
		t.Error("nothing must publish after a failed scan")
	}
}

// Rejected in both the literal and the EvalSymlinks-resolved form, whichever form the root itself is in.
func TestMoveStore_TargetIntoBldSandboxRejected(t *testing.T) {
	// alias -> <root>/bld lives OUTSIDE bld, so only EvalSymlinks exposes the sandbox referent.
	throughLink := func(root string) string {
		c.Check(os.Symlink(filepath.Join(root, "bld"), filepath.Join(root, "alias")))
		return filepath.Join(root, "alias", "data.txt")
	}
	for name, tc := range map[string]struct {
		storeAt    func(t *testing.T) (*Store, string)
		linkTarget func(root string) string
	}{
		"raw":                   {osStoreAt, func(root string) string { return filepath.Join(root, "bld", "data.txt") }},
		"resolved-through-link": {osStoreAt, throughLink},
		// Regression: the resolved target was matched against the literal root, which a root behind a
		// link never equals, so the chain was accepted — on CI only, where TEMP is such a root.
		"root-behind-link": {osStoreBehindLink, throughLink},
	} {
		t.Run(name, func(t *testing.T) {
			s, root := tc.storeAt(t)
			skipIfNoSymlinks(t, s)

			// The moving tree is bld/u — strictly below the bld sandbox root.
			writeFile(t, s.fs, filepath.Join("bld", "data.txt"), []byte("D"))
			writeFile(t, s.fs, filepath.Join("bld", "u", "keep.txt"), []byte("K"))
			osSymlink(tc.linkTarget(root), filepath.Join(root, "bld", "u", "esc.lnk"))

			err := c.Rescue(func() {
				s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{AllowLinks: true})
			})
			if err == nil || !strings.Contains(err.Error(), "inside the bld sandbox") {
				t.Fatalf("a target inside the bld sandbox must be rejected, got %v", err)
			}
			if !exists(t, s.fs, filepath.Join("bld", "u", "keep.txt")) {
				t.Error("a failed scan must leave the sandbox intact")
			}
		})
	}
}

func TestMoveStore_RelativeTargetEscapingIntoBldRejected(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join("bld", "data.txt"), []byte("D"))
	writeFile(t, s.fs, filepath.Join("bld", "u", "keep.txt"), []byte("K"))
	c.Check(os.Symlink(filepath.Join("..", "data.txt"), filepath.Join(root, "bld", "u", "esc.lnk")))

	err := c.Rescue(func() {
		s.MoveStore(OutRoot, filepath.Join("bld", "u"), PublishPlan{AllowLinks: true})
	})
	if err == nil || !strings.Contains(err.Error(), "inside the bld sandbox") {
		t.Fatalf("a relative target escaping into the bld sandbox must be rejected, got %v", err)
	}
}

func TestMoveStore_DanglingKeptLinkFailsAtScan(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join("bld", "keep.txt"), []byte("K"))
	writeFile(t, s.fs, filepath.Join("bld", "drop.txt"), []byte("D"))
	gone := filepath.Join(root, "no-such-target")
	osSymlink(filepath.Join(root, "bld", "keep.txt"), filepath.Join(root, "bld", "probe.lnk"))
	c.Check(os.Remove(filepath.Join(root, "bld", "probe.lnk")))
	c.Check(os.Symlink(gone, filepath.Join(root, "bld", "gone.lnk")))

	err := c.Rescue(func() {
		s.MoveStore(OutRoot, "bld", PublishPlan{Filter: compile("+**", "-drop.txt"), AllowLinks: true})
	})
	if err == nil || !strings.Contains(err.Error(), "gone.lnk") || !strings.Contains(err.Error(), "no-such-target") {
		t.Fatalf("a dangling kept link must fail the scan naming link and target, got %v", err)
	}
	if !exists(t, s.fs, filepath.Join("bld", "drop.txt")) {
		t.Error("the scan must fail before the filter deletes anything")
	}
	if exists(t, s.fs, OutRoot) {
		t.Error("nothing must publish after a failed scan")
	}
}

// Proven by corrupting the stored tree, which the fold does not notice.
func TestHashSrc_FoldsTrustedStoreDirLink(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join("stage", "data.txt"), []byte("payload"))
	key := s.MoveStore(OutRoot, "stage", PublishPlan{})

	writeFile(t, s.fs, filepath.Join("u", "a.txt"), []byte("A"))
	osSymlink(filepath.Join(root, OutRoot, key), filepath.Join(root, "u", "dep"))

	writeFile(t, s.fs, filepath.Join("twin", "a.txt"), []byte("A"))
	writeFile(t, s.fs, filepath.Join("twin", "dep", "data.txt"), []byte("payload"))
	want := Hashing.HashDir(s.fs, "twin")

	if got := s.HashSrc(s.fs, "u", includeAll, true); got != want {
		t.Errorf("trusting HashSrc = %q, want materialized twin key %q", got, want)
	}

	writeFile(t, s.fs, filepath.Join(OutRoot, key, "junk.txt"), []byte("J"))
	if got := s.HashSrc(s.fs, "u", includeAll, true); got != want {
		t.Errorf("trusting walk must fold the D name without reading the referent, got %q", got)
	}
}
