// Tests of the leaf rule's read side (Doc/Bugs/SymlinkHandling.md). Real OS fs — symlinks
// don't exist on MemMapFs.

package Hashing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	c "github.com/dimagog/bufa/internal/contract"
)

func skipIfNoOSSymlinks(t *testing.T, base string) {
	t.Helper()
	target := filepath.Join(base, "probe-target")
	c.Check(os.WriteFile(target, nil, 0o644))
	if err := os.Symlink(target, filepath.Join(base, "probe-link")); err != nil {
		t.Skipf("symlinks unsupported here (Windows Developer Mode off?): %v", err)
	}
}

func TestHashDir_FollowsFileSymlink(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	data := []byte("artifact-bytes")
	target := filepath.Join(base, "store", "art.bin")
	c.Check(os.MkdirAll(filepath.Dir(target), 0o755))
	c.Check(os.WriteFile(target, data, 0o644))

	real := filepath.Join(base, "real")
	c.Check(os.MkdirAll(real, 0o755))
	c.Check(os.WriteFile(filepath.Join(real, "art.bin"), data, 0o644))

	linked := filepath.Join(base, "linked")
	c.Check(os.MkdirAll(linked, 0o755))
	c.Check(os.Symlink(target, filepath.Join(linked, "art.bin")))

	realHash, linkedHash := HashDir(osFs, real), HashDir(osFs, linked)
	if realHash != linkedHash {
		t.Errorf("linked tree hash %q != real tree hash %q — link must hash as its referent", linkedHash, realHash)
	}
}

func TestHashDir_DirTargetSymlinkHashesReferentSubtree(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	targetDir := filepath.Join(base, "payload")
	c.Check(os.MkdirAll(filepath.Join(targetDir, "sub"), 0o755))
	c.Check(os.WriteFile(filepath.Join(targetDir, "a.txt"), []byte("A"), 0o644))
	c.Check(os.WriteFile(filepath.Join(targetDir, "sub", "b.txt"), []byte("B"), 0o644))

	real := filepath.Join(base, "real")
	c.Check(os.MkdirAll(filepath.Join(real, "tool", "sub"), 0o755))
	c.Check(os.WriteFile(filepath.Join(real, "tool", "a.txt"), []byte("A"), 0o644))
	c.Check(os.WriteFile(filepath.Join(real, "tool", "sub", "b.txt"), []byte("B"), 0o644))

	linked := filepath.Join(base, "linked")
	c.Check(os.MkdirAll(linked, 0o755))
	c.Check(os.Symlink(targetDir, filepath.Join(linked, "tool")))

	realHash, linkedHash := HashDir(osFs, real), HashDir(osFs, linked)
	if realHash != linkedHash {
		t.Errorf("dir-linked tree hash %q != materialized twin %q", linkedHash, realHash)
	}
}

func TestHashDir_DanglingSymlinkFails(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)

	dir := filepath.Join(base, "d")
	c.Check(os.MkdirAll(dir, 0o755))
	target := filepath.Join(base, "no-such-target")
	c.Check(os.Symlink(target, filepath.Join(dir, "gone.bin")))

	err := c.Rescue(func() { HashDir(vfs.NewOsFs(), dir) })
	if err == nil {
		t.Fatal("a dangling symlink must fail the hash walk")
	}
	if !strings.Contains(err.Error(), "gone.bin") || !strings.Contains(err.Error(), "no-such-target") {
		t.Errorf("the error must name the link and its target, got %v", err)
	}
}

func TestHashDir_SymlinkCycleFails(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)

	dir := filepath.Join(base, "d")
	c.Check(os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	c.Check(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A"), 0o644))
	c.Check(os.Symlink(dir, filepath.Join(dir, "sub", "up")))

	err := c.Rescue(func() { HashDir(vfs.NewOsFs(), dir) })
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a link to its own ancestor must fail as a cycle, got %v", err)
	}
}

func TestHashDir_DiamondLinksLegal(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	shared := filepath.Join(base, "shared")
	c.Check(os.MkdirAll(shared, 0o755))
	c.Check(os.WriteFile(filepath.Join(shared, "s.txt"), []byte("S"), 0o644))

	linked := filepath.Join(base, "linked")
	c.Check(os.MkdirAll(linked, 0o755))
	c.Check(os.Symlink(shared, filepath.Join(linked, "one")))
	c.Check(os.Symlink(shared, filepath.Join(linked, "two")))

	real := filepath.Join(base, "real")
	for _, name := range []string{"one", "two"} {
		c.Check(os.MkdirAll(filepath.Join(real, name), 0o755))
		c.Check(os.WriteFile(filepath.Join(real, name, "s.txt"), []byte("S"), 0o644))
	}

	realHash, linkedHash := HashDir(osFs, real), HashDir(osFs, linked)
	if realHash != linkedHash {
		t.Errorf("diamond tree hash %q != materialized twin %q", linkedHash, realHash)
	}
}

// A kept link is a leaf: an empty-referent dir link is NOT pruned like an empty directory.
func TestHashDirFiltered_EmptyReferentLinkKept(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	empty := filepath.Join(base, "empty")
	c.Check(os.MkdirAll(empty, 0o755))

	dir := filepath.Join(base, "d")
	c.Check(os.MkdirAll(dir, 0o755))
	c.Check(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A"), 0o644))
	c.Check(os.Symlink(empty, filepath.Join(dir, "lnk")))

	bare := filepath.Join(base, "bare")
	c.Check(os.MkdirAll(bare, 0o755))
	c.Check(os.WriteFile(filepath.Join(bare, "a.txt"), []byte("A"), 0o644))

	keepAll := func(vfs.Fs, string, os.FileInfo) bool { return false }
	if HashDirFiltered(osFs, dir, keepAll) == HashDirFiltered(osFs, bare, keepAll) {
		t.Error("an empty-referent link must contribute an entry — only real empty dirs are pruned")
	}
}

func TestHash_DanglingLinkArgumentNamesLinkAndTarget(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)

	link := filepath.Join(base, "gone.lnk")
	c.Check(os.Symlink(filepath.Join(base, "no-such-target"), link))

	err := c.Rescue(func() { Hash(vfs.NewOsFs(), link) })
	if err == nil {
		t.Fatal("Hash on a dangling link must fail")
	}
	if !strings.Contains(err.Error(), "gone.lnk") || !strings.Contains(err.Error(), "no-such-target") {
		t.Errorf("the error must name the link and its target, got %v", err)
	}
}

func TestHash_DirLinkArgumentHashesReferent(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	payload := filepath.Join(base, "payload")
	c.Check(os.MkdirAll(filepath.Join(payload, "sub"), 0o755))
	c.Check(os.WriteFile(filepath.Join(payload, "a.txt"), []byte("A"), 0o644))
	c.Check(os.WriteFile(filepath.Join(payload, "sub", "b.txt"), []byte("B"), 0o644))
	link := filepath.Join(base, "tool.lnk")
	c.Check(os.Symlink(payload, link))

	if got, want := Hash(osFs, link), Hash(osFs, payload); got != want {
		t.Errorf("Hash(dir link) = %q, want the referent's %q", got, want)
	}
}

// The shortcut applies at every level of the walk.
func TestHashDir_HashNamedLinkKeysIdenticallyToMaterialized(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	data := []byte("artifact-bytes")
	seed := filepath.Join(base, "cache", "seed")
	c.Check(os.MkdirAll(filepath.Dir(seed), 0o755))
	c.Check(os.WriteFile(seed, data, 0o644))
	entry := filepath.Join(base, "cache", HashFile(osFs, seed))
	c.Check(os.Rename(seed, entry)) // the entry's name IS its content hash

	refdir := filepath.Join(base, "refdir")
	c.Check(os.MkdirAll(refdir, 0o755))
	c.Check(os.Symlink(entry, filepath.Join(refdir, "inner.bin")))

	linked := filepath.Join(base, "linked")
	c.Check(os.MkdirAll(linked, 0o755))
	c.Check(os.Symlink(entry, filepath.Join(linked, "art.bin")))
	c.Check(os.Symlink(refdir, filepath.Join(linked, "sub")))

	twin := filepath.Join(base, "twin")
	c.Check(os.MkdirAll(filepath.Join(twin, "sub"), 0o755))
	c.Check(os.WriteFile(filepath.Join(twin, "art.bin"), data, 0o644))
	c.Check(os.WriteFile(filepath.Join(twin, "sub", "inner.bin"), data, 0o644))

	if got, want := HashDir(osFs, linked), HashDir(osFs, twin); got != want {
		t.Errorf("hash-named linked tree = %q, want materialized twin key %q", got, want)
	}
}

func TestHashDir_HashNamedLinkSkipsReferentRead(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	data := []byte("artifact-bytes")
	mkLinkDir := func(name, targetName string) string {
		target := filepath.Join(base, targetName)
		c.Check(os.WriteFile(target, data, 0o644))
		dir := filepath.Join(base, name)
		c.Check(os.MkdirAll(dir, 0o755))
		c.Check(os.Symlink(target, filepath.Join(dir, "art.bin")))
		return dir
	}
	fakeA := "F" + strings.Repeat("a", 52)
	fakeB := "F" + strings.Repeat("b", 52)
	d1, d2 := mkLinkDir("d1", fakeA), mkLinkDir("d2", fakeB)

	twin := filepath.Join(base, "twin")
	c.Check(os.MkdirAll(twin, 0o755))
	c.Check(os.WriteFile(filepath.Join(twin, "art.bin"), data, 0o644))
	want := HashDir(osFs, twin)

	got1 := HashDir(osFs, d1)
	if got1 == want {
		t.Error("a hash-named target must fold its name instead of streaming the referent")
	}
	if got2 := HashDir(osFs, d2); got1 == got2 {
		t.Error("the target name participates in the key: two names over the same bytes, two keys")
	}

	// The kind gate: a dir-key name on a regular-file referent is no key of its kind — it streams.
	dKind := mkLinkDir("d3", "D"+strings.Repeat("a", 52))
	if got := HashDir(osFs, dKind); got != want {
		t.Errorf("a dir-key name on a file referent must stream: got %q, want %q", got, want)
	}

	dGone := filepath.Join(base, "dgone")
	c.Check(os.MkdirAll(dGone, 0o755))
	c.Check(os.Symlink(filepath.Join(base, "F"+strings.Repeat("c", 52)), filepath.Join(dGone, "art.bin")))
	if err := c.Rescue(func() { HashDir(osFs, dGone) }); err == nil ||
		!strings.Contains(err.Error(), "dangling") {
		t.Errorf("a dangling hash-named link must still fail the walk, got: %v", err)
	}
}

func TestHashDir_SafeHashingStreamsHashNamedLink(t *testing.T) {
	base := t.TempDir()
	skipIfNoOSSymlinks(t, base)
	osFs := vfs.NewOsFs()

	data := []byte("artifact-bytes")
	target := filepath.Join(base, "F"+strings.Repeat("a", 52))
	c.Check(os.WriteFile(target, data, 0o644))
	dir := filepath.Join(base, "d")
	c.Check(os.MkdirAll(dir, 0o755))
	c.Check(os.Symlink(target, filepath.Join(dir, "art.bin")))

	twin := filepath.Join(base, "twin")
	c.Check(os.MkdirAll(twin, 0o755))
	c.Check(os.WriteFile(filepath.Join(twin, "art.bin"), data, 0o644))
	want := HashDir(osFs, twin)

	if HashDir(osFs, dir) == want {
		t.Fatal("precondition: the hash-named link must fold by default")
	}
	SafeHashing = true
	defer func() { SafeHashing = false }()
	if got := HashDir(osFs, dir); got != want {
		t.Errorf("safe hashing must stream the referent: got %q, want %q", got, want)
	}
}
