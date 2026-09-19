package Store

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dustin/go-humanize"

	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

func pathFromLinkName(name string) string {
	rest := strings.TrimPrefix(name, PathLinkPrefix)
	if rest == "" {
		return "."
	}
	return strings.ReplaceAll(rest, PathLinkPrefix, "/")
}

type GCStats struct {
	StalePathLinks       int // ∕<path> links whose source dir is gone / no longer a build dir
	OrphanContent        int // D<hash> content dirs nothing live references
	OrphanBuildLinks     int // out/ B<combined> input links with no surviving ∕<path> roots
	StaleDirtySkipHashes int // dirty/ ∕<path> skip hashes whose source dir is no longer a build dir
	OrphanCacheDirs      int // user/ P<hash> cache dirs no surviving ∕<path> link references
	LeftoverScratchDirs  int // bld/ and tmp/ dirs a failed or interrupted build left behind
	BytesFreed           uint64
}

var scratchRoots = []string{BldSandboxRoot, TmpRoot}

// Over-collection is self-healing: anything wrongly collected is re-staged or re-built on the next build.
func (s *Store) GC(srcPresent func(path string) bool, reportFreedSpace bool) GCStats {
	var st GCStats
	removeTree := func(path string) { c.Check(s.fs.RemoveAll(path)) }
	if reportFreedSpace {
		removeTree = func(path string) {
			pathSize := s.treeSize(path)
			slog.Info("GC: removing", "path", path, "size", humanize.IBytes(pathSize))
			st.BytesFreed += pathSize
			c.Check(s.fs.RemoveAll(path))
		}
	}
	st.OrphanContent = s.gcContentRoot(InRoot, srcPresent, false, Hashing.IsValidDirHash, removeTree, &st)
	st.OrphanContent += s.gcContentRoot(OutRoot, srcPresent, true, Hashing.IsValidDirHash, removeTree, &st)
	st.OrphanCacheDirs = s.gcContentRoot(UserRoot, srcPresent, false, Hashing.IsValidPathHash, removeTree, &st)
	s.gcDirtyRoot(srcPresent, &st)
	s.gcScratchRoots(removeTree, &st)
	return st
}

// Regular files only: a symlink's target is not freed with the tree.
func (s *Store) treeSize(path string) (n uint64) {
	for _, info := range s.listInfos(path) {
		if info.IsDir() {
			n += s.treeSize(filepath.Join(path, info.Name()))
		} else if info.Mode().IsRegular() {
			n += uint64(info.Size())
		}
	}
	return n
}

// A non-directory squatting on a scratch name is check's to report, not gc's to collect.
func (s *Store) gcScratchRoots(removeTree func(path string), st *GCStats) {
	for _, root := range scratchRoots {
		info, err := vfsx.TryLstat(s.fs, root)
		if !os.IsNotExist(err) {
			c.Checkf(err, "Lstat '%s'", root)
			if info.IsDir() {
				// RemoveAll never follows symlinks, so link-staged deps go as link objects.
				removeTree(root)
				st.LeftoverScratchDirs++
				slog.Info("GC: removed leftover scratch dir", "root", root)
			}
		}
	}
}

func (s *Store) gcDirtyRoot(srcPresent func(path string) bool, st *GCStats) {
	for _, name := range s.listNames(DirtyRoot) {
		if !strings.HasPrefix(name, PathLinkPrefix) {
			continue
		}
		path := pathFromLinkName(name)
		if srcPresent(path) {
			continue
		}
		c.Check(s.fs.Remove(filepath.Join(DirtyRoot, name)))
		st.StaleDirtySkipHashes++
		slog.Info("GC: removed stale dirty skip hash", "path", path)
	}
}

// Returns the number of unreferenced content entries (isContent-named dirs) removed; the link
// counters go to st.
func (s *Store) gcContentRoot(root string, srcPresent func(path string) bool, viaBuildLink bool,
	isContent func(name string) bool, removeTree func(path string), st *GCStats,
) (orphanContent int) {
	names := s.listNames(root)

	liveLinked := Util.NewSet[string]()
	for _, name := range names {
		if !strings.HasPrefix(name, PathLinkPrefix) {
			continue
		}
		path := pathFromLinkName(name)
		if srcPresent(path) {
			liveLinked.Add(s.GetLinkTarget(root, name))
		} else {
			s.removeLink(root, name)
			st.StalePathLinks++
			slog.Info("GC: removed stale path link", "root", root, "path", path)
		}
	}

	liveContent := liveLinked
	if viaBuildLink {
		liveContent = Util.NewSet[string]()
		for _, name := range names {
			if !Hashing.IsValidBuildHash(name) {
				continue
			}
			if !liveLinked.Contains(name) {
				s.removeLink(root, name)
				st.OrphanBuildLinks++
				slog.Info("GC: removed orphan build link", "root", root, "name", name)
				continue
			}
			liveContent.Add(s.GetLinkTarget(root, name)) // B<combined> -> D<hash>
		}
	}

	for _, name := range names {
		if !isContent(name) || liveContent.Contains(name) {
			continue
		}
		removeTree(filepath.Join(root, name))
		orphanContent++
		slog.Info("GC: removed orphan content", "root", root, "name", name)
	}
	return orphanContent
}

func (s *Store) listInfos(root string) []fs.FileInfo {
	return vfsx.ReadDirIfExists(s.fs, root)
}

func (s *Store) listNames(root string) []string {
	infos := s.listInfos(root)
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name()
	}
	return names
}
