package Store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	c "github.com/dimagog/bufa/internal/contract"
)

// Re-derives the spec's constant: MD5 of ".IsCacheDirectory", per https://bford.info/cachedir/.
const specSignatureLine = "Signature: 8a477f597d28d172789f06886806bc55\n"

func rootedAt(dir string) vfs.Fs { return vfs.NewBasePathFs(vfs.NewOsFs(), dir) }

func TestEnsureRootWithTag_CreatesAndTags(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")

	EnsureRootWithTag(rootedAt(root), "# the comment")

	got := string(c.Check2(os.ReadFile(filepath.Join(root, CacheDirTagName))))
	if want := specSignatureLine + "# the comment\n"; got != want {
		t.Errorf("tag content = %q, want %q", got, want)
	}
}

func TestEnsureRootWithTag_ExistingRootUntouched(t *testing.T) {
	root := t.TempDir()

	EnsureRootWithTag(rootedAt(root), "# the comment")

	if _, err := os.Lstat(filepath.Join(root, CacheDirTagName)); !os.IsNotExist(err) {
		t.Errorf("a pre-existing root must not be tagged, Lstat err = %v", err)
	}
}

func TestEnsureRootWithTag_MissingContainerCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "container", "root")

	EnsureRootWithTag(rootedAt(root), "# the comment")

	if !strings.HasPrefix(string(c.Check2(os.ReadFile(filepath.Join(root, CacheDirTagName)))), specSignatureLine) {
		t.Error("root under a missing container must be created and tagged")
	}
	if _, err := os.Lstat(filepath.Join(root, "..", CacheDirTagName)); !os.IsNotExist(err) {
		t.Errorf("the container itself must not be tagged, Lstat err = %v", err)
	}
}

// Guards the one-shot rule: the tag is written when the dir is born, never re-checked or restored.
func TestEnsureRootWithTag_DeletedTagNotRecreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	EnsureRootWithTag(rootedAt(root), "# the comment")
	c.Check(os.Remove(filepath.Join(root, CacheDirTagName)))

	EnsureRootWithTag(rootedAt(root), "# the comment")

	if _, err := os.Lstat(filepath.Join(root, CacheDirTagName)); !os.IsNotExist(err) {
		t.Errorf("tag must not be recreated for an existing root, Lstat err = %v", err)
	}
}

func TestEnsureBuildRoot_TagComment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj"+BuildRootDirSuffix)

	ensureBuildRoot(rootedAt(root))

	got := string(c.Check2(os.ReadFile(filepath.Join(root, CacheDirTagName))))
	if want := specSignatureLine + buildRootTagComment + "\n"; got != want {
		t.Errorf("build root tag = %q, want %q", got, want)
	}
}

func TestMakeBuildSubdir_BirthsTaggedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj"+BuildRootDirSuffix)
	s := NewStore(rootedAt(root))

	s.MakeBuildSubdir(filepath.Join(InRoot, "x"))

	if _, err := os.Stat(filepath.Join(root, InRoot, "x")); err != nil {
		t.Errorf("subdir must exist: %v", err)
	}
	if !strings.HasPrefix(string(c.Check2(os.ReadFile(filepath.Join(root, CacheDirTagName)))), specSignatureLine) {
		t.Error("the build root must be born tagged")
	}
}

func TestMakeBuildSubdir_ExistingRootUntouched(t *testing.T) {
	root := t.TempDir()
	s := NewStore(rootedAt(root))

	s.MakeBuildSubdir(OutRoot)

	if _, err := os.Lstat(filepath.Join(root, CacheDirTagName)); !os.IsNotExist(err) {
		t.Errorf("a pre-existing root must not be tagged, Lstat err = %v", err)
	}
}

func TestEnsureBuildRootAt_OSPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj"+BuildRootDirSuffix)

	EnsureBuildRootAt(root)

	if !strings.HasPrefix(string(c.Check2(os.ReadFile(filepath.Join(root, CacheDirTagName)))), specSignatureLine) {
		t.Error("build root at an OS path must be created and tagged")
	}
}

func TestNuke_AcceptsCacheDirTag(t *testing.T) {
	root := makeBldRoot(t, "in/", "CacheDir.Tag", testSockName)

	Nuke(root, testSockName)

	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("build root must be gone, Lstat err = %v", err)
	}
}

// Admitted by name alone: the content is never read, so junk inside is not a problem to report.
func TestCheck_AcceptsCacheDirTag(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())
	writeFile(t, s.fs, CacheDirTagName, []byte("not a signature"))

	st, report := runCheck(s, true /*fix*/)
	if st != (CheckStats{}) || report != "" {
		t.Errorf("tag must be owned: CheckStats = %+v, report:\n%s", st, report)
	}
	if !exists(t, s.fs, CacheDirTagName) {
		t.Error("fix must keep the tag")
	}
}
