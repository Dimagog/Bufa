package Store

import (
	"errors"
	"io/fs"
	"slices"
	"strings"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

var layoutRoots = Util.Set[string]{InRoot: {}, OutRoot: {}, BldSandboxRoot: {}, DirtyRoot: {}, TmpRoot: {}, UserRoot: {}}

// The build root's ownership rule, shared by Nuke's refusal and Check's top-level pass: the layout
// roots as directories plus allowedFiles as non-directories.
func ownedTopLevel(allowedFiles []string) func(e fs.FileInfo) bool {
	return func(e fs.FileInfo) bool {
		name := e.Name()
		if e.IsDir() {
			return layoutRoots.Contains(strings.ToLower(name))
		} else {
			return slices.ContainsFunc(allowedFiles, func(f string) bool { return strings.EqualFold(f, name) })
		}
	}
}

func Nuke(bldRoot string, allowedFiles ...string) {
	NukeDir("build dir", bldRoot, ownedTopLevel(allowedFiles))
}

// NukeDir deletes dir entirely, but only after owned admits every entry; anything else refuses
// the whole operation, naming the offenders (dirs with a trailing '/'). A missing dir is a no-op.
func NukeDir(friendlyName, dir string, owned func(e fs.FileInfo) bool) {
	fsys := vfs.NewBasePathFs(vfs.NewOsFs(), dir)
	entries, err := vfs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	c.Checkf(err, "Failed to read directory '%s'", dir)

	var unexpected []string
	for _, e := range entries {
		if !owned(e) {
			display := e.Name()
			if e.IsDir() {
				display += "/"
			}
			unexpected = append(unexpected, display)
		}
	}
	c.Require(len(unexpected) == 0, "Refusing to nuke %s '%s' because of unexpected entries:\n%s\n", friendlyName, dir, strings.Join(unexpected, "\n"))
	// RemoveAll (os-level beneath) clears the Windows read-only attribute and retries.
	c.Checkf(fsys.RemoveAll("."), "Failed to remove directory '%s'", dir)
}
