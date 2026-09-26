package Store

import (
	"errors"
	"io/fs"

	vfs "github.com/spf13/afero"

	c "github.com/dimagog/bufa/internal/contract"
)

const CacheDirTagName = "CACHEDIR.TAG"

const cacheDirTagSignature = "Signature: 8a477f597d28d172789f06886806bc55\n"

const buildRootTagComment = "# bufa build dir: outputs and caches rebuilt from source on demand, safe to delete ('bufa nuke')"

// EnsureRootWithTag creates fsys's root dir and tags it iff this call created it.
func EnsureRootWithTag(fsys vfs.Fs, tagComment string) {
	err := fsys.Mkdir(".", 0o755)
	if errors.Is(err, fs.ErrNotExist) { // the container dir above the root is missing too
		err = fsys.MkdirAll(".", 0o755)
	}
	if !errors.Is(err, fs.ErrExist) {
		c.Check(err)
		c.Check(vfs.WriteFile(fsys, CacheDirTagName, []byte(cacheDirTagSignature+tagComment+"\n"), 0o644))
	}
}

func ensureBuildRoot(bldFS vfs.Fs) { EnsureRootWithTag(bldFS, buildRootTagComment) }

func EnsureBuildRootAt(bldRoot string) { ensureBuildRoot(vfs.NewBasePathFs(vfs.NewOsFs(), bldRoot)) }

// The only way a build subdir comes to exist: a bare MkdirAll would mint the root untagged.
func (s *Store) MakeBuildSubdir(dir string) {
	if !s.rootEnsured {
		ensureBuildRoot(s.fs)
		s.rootEnsured = true
	}
	c.Check(s.fs.MkdirAll(dir, 0o755))
}
