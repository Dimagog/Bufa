// Package Store provides a content-addressed directory store over one vfs.Fs.
package Store

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

const (
	InRoot         = "in"
	OutRoot        = "out"
	BldSandboxRoot = "bld"
	DirtyRoot      = "dirty"
	TmpRoot        = "tmp"
	UserRoot       = "user"

	BuildConfigName = "BUFA"

	// A shell provider's published definition, beside its binary. Outside the reserved
	// BUFA/*.BUFA namespace on purpose: it stages and publishes like any source file.
	ShellDefFileName = BuildConfigName + ".shell"

	// A bld dep's published env exports, at its output root. Outside the reserved namespace
	// like BUFA.shell, for the same reason.
	EnvFileName = BuildConfigName + ".env"

	// Appended to the source root's path to form its sibling build dir: C:\Proj -> C:\Proj.BUFA
	BuildRootDirSuffix = "." + BuildConfigName

	// The source-root marker file: detects the project root and holds the root config — nothing
	// else. The bare (empty-suffix) member of the *.BUFA namespace, so no dir can declare a
	// virtual config colliding with it.
	SrcRootFileName = "." + BuildConfigName

	// Appended to a dir's base name to form the virtual config declaring it: clj.BUFA -> virtual
	// build dir clj. The three constants above are distinct naming choices whose values coincide.
	VirtualConfigSuffix = "." + BuildConfigName

	// source-root marker; a dir or a file (worktrees and submodules mark with a file)
	GitRootMarker = ".git"
)

type Store struct{ fs vfs.Fs }

func NewStore(fs vfs.Fs) *Store { return &Store{fs: fs} }

func bufaSuffixed(name string) bool {
	return len(name) >= len(VirtualConfigSuffix) &&
		strings.EqualFold(name[len(name)-len(VirtualConfigSuffix):], VirtualConfigSuffix)
}

func IsReservedName(name string) bool {
	return strings.EqualFold(name, BuildConfigName) || bufaSuffixed(name)
}

func VirtualConfigPathForDir(srcDir string) string {
	base := filepath.Base(srcDir)
	if base == "." {
		return ""
	}
	return filepath.Join(filepath.Dir(srcDir), base+VirtualConfigSuffix)
}

func VirtualSuffix(name string) string {
	if !bufaSuffixed(name) {
		return ""
	}
	suffix := name[:len(name)-len(VirtualConfigSuffix)]
	if suffix == "" {
		return ""
	}
	return suffix
}

// The name is bufa-reserved, so a directory at path is a contract violation and panics — unlike
// the silent vfsx.FileExists. nil info ⇒ absent.
func configExists(info fs.FileInfo, path string) bool {
	if info == nil {
		return false
	}
	c.Require(!info.IsDir(), "'%s' is a directory, but its name is reserved by bufa for a config file", path)
	return true
}

func VFSConfigExists(fs vfs.Fs, path string) bool {
	info, err := fs.Stat(path)
	if err != nil {
		return false
	}
	return configExists(info, path)
}

func OSConfigExists(path string) bool {
	return configExists(UnsafeIO.OSTryStat(path), path)
}

// A BUFA-named directory is ordinary content, not the reserved-name violation. Build sets it once from
// the root marker's allowBufaDir. A package var, not a parameter: the flag is almost never used
// (building bufa itself), not worth threading through every walk signature.
var AllowBufaDir = false

func buildConfigExists(fs vfs.Fs, dir string) bool {
	path := filepath.Join(dir, BuildConfigName)
	if AllowBufaDir {
		return vfsx.FileExists(fs, path)
	}
	return VFSConfigExists(fs, path)
}

// The one existence predicate shared by skipNested and BuildDirPresent, so the two can't drift.
func virtualConfigDeclares(fs vfs.Fs, dir string) bool {
	virtualConfig := VirtualConfigPathForDir(dir)
	return virtualConfig != "" && vfsx.FileExists(fs, virtualConfig)
}

func BuildDirPresent(srcFS vfs.Fs, path string) bool {
	p := filepath.FromSlash(path)
	return buildConfigExists(srcFS, p) || virtualConfigDeclares(srcFS, p)
}

func skipNested(fs vfs.Fs, path string, info fs.FileInfo) bool {
	if !info.IsDir() {
		return false
	}
	c.Require(!bufaSuffixed(filepath.Base(path)),
		"Directory '%s' is named like a bufa build root (<base>%s) — likely left by a previous"+
			" build with a different source root; delete it, or anchor the project it belongs to"+
			" with a '%s' source-root marker file",
		path, BuildRootDirSuffix, SrcRootFileName)
	return buildConfigExists(fs, path) ||
		VFSConfigExists(fs, filepath.Join(path, SrcRootFileName)) ||
		virtualConfigDeclares(fs, path)
}

const linkPolicyHint = "Symlink '%s' is not allowed in a safe build; declare it via [[deps.ext]] or set `unsafe = true`"

// The filter is consulted BEFORE the symlink policy, so a filter-excluded link never reaches it.
// The single point copyTree and HashSrc share, so hash and staged tree can't drift.
func filteredSkip(root string, pruneNested bool, filter *FilterFiles.Filter, allowLinks bool) func(vfs.Fs, string, fs.FileInfo) bool {
	if filter == nil {
		c.Require(!pruneNested, "Store: pruneNested requires a filter")
		c.Require(allowLinks, "Store: the no-links policy requires a filter")
		return nil
	}
	return func(fs vfs.Fs, path string, info fs.FileInfo) bool {
		if info.IsDir() {
			return pruneNested && skipNested(fs, path, info)
		}
		rel := filepath.ToSlash(c.Check2(filepath.Rel(root, path)))
		if !filter.Included(rel) {
			return true // excluded links never stage, so the policy has no say on them
		}
		c.Require(allowLinks || info.Mode()&os.ModeSymlink == 0, linkPolicyHint, path)
		return false
	}
}

func (s *Store) HashSrc(srcFs vfs.Fs, dir string, filter *FilterFiles.Filter, allowLinks bool) string {
	return Hashing.HashDirFiltered(srcFs, dir, filteredSkip(dir, true, filter, allowLinks))
}

func (s *Store) Contains(root, hash string) bool {
	return vfsx.DirExistsFailOnFile(s.fs, filepath.Join(root, hash))
}

// A single Readlink does the work — a separate Lstat probe would double the OS file opens on
// Windows for no extra signal.
func (s *Store) GetLinkTarget(root, name string) string {
	path := filepath.Join(root, name)
	target, err := UnsafeIO.OSReadlink(s.fs, path)
	if err != nil {
		return ""
	}
	// The readback is the absolute OS target linkTo wrote; keys never contain a separator, so
	// the base component is the key.
	return filepath.Base(target)
}

const PathLinkPrefix = "∕"

func (s *Store) Link(root, name, hash string) (prev string) {
	prev = s.GetLinkTarget(root, name)
	if prev == hash {
		slog.Info("Link read", "root", root, "name", name, "hash", hash)
		return prev
	}
	if prev != "" {
		s.removeLink(root, name) // re-point: os.Symlink won't overwrite an existing name
	}
	slog.Info("Link write", "root", root, "name", name, "hash", hash, "old_hash", prev)
	s.linkTo(root, name, hash)
	return prev
}

// Casing is preserved: GC stats the decoded path, so a folded name would miss a real dir on a
// case-sensitive filesystem. "." maps to the bare ∕, avoiding Windows' trailing-dot stripping.
func pathToLinkName(path string) string {
	norm := filepath.ToSlash(path)
	if norm == "." {
		return PathLinkPrefix
	}
	return PathLinkPrefix + strings.ReplaceAll(norm, "/", PathLinkPrefix)
}

func (s *Store) LinkPath(root, path, hash string) {
	s.Link(root, pathToLinkName(path), hash)
}

func (s *Store) GetDirtySkipHash(path string) string {
	return string(vfsx.TryReadSmallFileFast(s.fs, filepath.Join(DirtyRoot, pathToLinkName(path))))
}

func (s *Store) SetDirtySkipHash(path, skipHash string) {
	c.Check(s.fs.MkdirAll(DirtyRoot, 0o755))
	name := filepath.Join(DirtyRoot, pathToLinkName(path))
	slog.Info("SetDirtySkipHash", "path", name, "skipHash", skipHash)
	c.Check(vfs.WriteFile(s.fs, name, []byte(skipHash), 0o644))
}

// Hashes the same slash-form string pathToLinkName encodes, so a ∕ link name and its P target
// agree by construction.
func cacheDirName(path string) string {
	return Hashing.HashPath(filepath.ToSlash(path))
}

func (s *Store) EnsureCacheDir(path string) string {
	name := cacheDirName(path)
	dir := filepath.Join(UserRoot, name)
	if info, err := vfsx.TryLstat(s.fs, dir); os.IsNotExist(err) {
		c.Check(s.fs.MkdirAll(dir, 0o755))
	} else {
		c.Checkf(err, "Lstat '%s'", dir)
		c.Require(info.IsDir(), "Cache dir '%s' of '%s' exists but is not a directory", dir, path)
	}
	s.LinkPath(UserRoot, path, name)
	return name
}

// Keeps the P… dirs and ∕ links themselves; any other name is reported and left untouched.
func (s *Store) EmptyCacheDirs(out io.Writer) (emptied int) {
	for _, info := range s.listInfos(UserRoot) {
		name := info.Name()
		if Hashing.IsValidPathHash(name) && info.IsDir() {
			dir := filepath.Join(UserRoot, name)
			vfsx.RemoveAllUnder(s.fs, dir)
			slog.Info("Emptied cache dir", "dir", dir)
			emptied++
		} else if !(strings.HasPrefix(name, PathLinkPrefix) && info.Mode()&os.ModeSymlink != 0) {
			fmt.Fprintf(out, "UNEXPECTED ENTRY: '%s' left untouched (run 'bufa check' to inspect)\n", UserRoot+"/"+name)
		}
	}
	return emptied
}

func (s *Store) linkTo(root, name, hash string) {
	s.symlinkAt(filepath.Join(root, hash), filepath.Join(root, name))
}

// Resolved to an absolute OS target so Windows makes a directory symlink.
func (s *Store) symlinkAt(targetPath, linkPath string) {
	vfsx.XSymlink(s.fs, targetPath, s.fs, linkPath)
}

// Remove drops the link, not its target — dir symlinks included, on Windows and Unix alike.
func (s *Store) removeLink(root, name string) {
	slog.Debug("removeLink", "root", root, "name", name)
	c.Check(s.fs.Remove(filepath.Join(root, name)))
}

func (s *Store) Restore(root, hash, dstDir string) {
	c.Require(s.Contains(root, hash), "Store: %s/%s not in store", root, hash)
	s.copyTree(s.fs, filepath.Join(root, hash), dstDir, false, nil, true)
}

// dstDir must not exist: os.Symlink refuses an existing name, so Restore's empty-destination
// invariant is self-enforcing here.
func (s *Store) RestoreLink(root, hash, dstDir string) {
	c.Require(s.Contains(root, hash), "Store: %s/%s not in store", root, hash)
	s.symlinkAt(filepath.Join(root, hash), dstDir)
}

func (s *Store) Store(root string, srcFs vfs.Fs, srcDir string, pruneNested bool,
	filter *FilterFiles.Filter, allowLinks bool,
) string {
	s.stage(srcFs, srcDir, pruneNested, filter, allowLinks)
	hash := Hashing.HashDir(s.fs, TmpRoot)
	s.publishTmp(root, hash)
	return hash
}

type PublishPlan struct {
	// acts on files and links by their own srcDir-relative path, never on dirs
	Filter *FilterFiles.Filter
	// deleted kind-blind; unmatched keys are harmless
	PruneDirs, ExtPrune Util.Set[string]
	// the only links a safe-mode publish admits; AllowLinks waives the policy
	KeepLinks  Util.Set[string]
	AllowLinks bool
}

// Scan is read-only and runs to completion before apply mutates, so a failed publish leaves the
// kept-for-debug sandbox exactly as the script left it.
func (s *Store) MoveStore(root string, srcDir string, plan PublishPlan) string {
	s.filterInPlace(srcDir, plan)
	hash := Hashing.HashDir(s.fs, srcDir)
	s.publishDir(root, hash, srcDir)
	return hash
}

func (s *Store) StoreAsHash(root string, srcFs vfs.Fs, srcDir string, pruneNested bool,
	filter *FilterFiles.Filter, allowLinks bool, hash string,
) {
	s.stage(srcFs, srcDir, pruneNested, filter, allowLinks)
	s.publishTmp(root, hash)
}

func (s *Store) SrcPrep(srcFs vfs.Fs, srcDir string, filter *FilterFiles.Filter, allowLinks bool) string {
	srcHash := s.HashSrc(srcFs, srcDir, filter, allowLinks)
	if s.Contains(InRoot, srcHash) {
		slog.Info("SrcPrep cache hit", "dir", srcDir, "hash", srcHash)
	} else {
		s.StoreAsHash(InRoot, srcFs, srcDir, true, filter, allowLinks, srcHash)
		slog.Info("SrcPrep prepared", "dir", srcDir, "hash", srcHash)
	}
	s.LinkPath(InRoot, srcDir, srcHash)
	return srcHash
}

func (s *Store) SrcPrepEmpty(srcDir string) string {
	hash := Hashing.EmptyDirHash
	// An empty tree has no content to stage, so create the final dir directly: MkdirAll is idempotent
	// and needs no tmp-stage + rename, which only exists to hide partial content.
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, hash), 0o755))
	slog.Info("SrcPrepEmpty prepared", "dir", srcDir, "hash", hash)
	s.LinkPath(InRoot, srcDir, hash)
	return hash
}

func (s *Store) stage(srcFs vfs.Fs, srcDir string, pruneNested bool, filter *FilterFiles.Filter, allowLinks bool) {
	c.Check(s.fs.RemoveAll(TmpRoot))
	s.copyTree(srcFs, srcDir, TmpRoot, pruneNested, filter, allowLinks)
}

func (s *Store) publishTmp(root, hash string) { s.publishDir(root, hash, TmpRoot) }

func (s *Store) filterInPlace(dir string, plan PublishPlan) {
	slog.Info("filterInPlace", "dir", dir)
	scan := &filterScan{
		PublishPlan: plan,
		store:       s,
		root:        dir,
		dropEmpty:   plan.Filter != nil || len(plan.PruneDirs) > 0,
		// RealPath does no FS access, and a fs without symlinks never runs the validation, so computing
		// these eagerly is free.
		movingRoot: UnsafeIO.OSPath(s.fs, dir),
		bldRoot:    UnsafeIO.OSPath(s.fs, BldSandboxRoot),
	}
	scan.checkBldRoot = Util.AtOrBelowIgnoreUnrelated(scan.bldRoot, scan.movingRoot)
	// Lazy: only a kept link pays the FS access, and bld/ is never resolved unless checkBldRoot.
	scan.realMovingRoot = sync.OnceValue(func() string { return evalRoot(scan.movingRoot) })
	scan.realBldRoot = sync.OnceValue(func() string { return evalRoot(scan.bldRoot) })
	scan.scanDir(dir)
	scan.apply()
}

type filterScan struct {
	PublishPlan
	store                       *Store
	root                        string
	dropEmpty                   bool
	movingRoot, bldRoot         string        // absolute OS roots for kept-link target validation
	realMovingRoot, realBldRoot func() string // their EvalSymlinks forms, matched against the resolved target
	checkBldRoot                bool          // movingRoot lies inside the bld sandbox

	removePaths     []string   // excluded files and links, pruned files/links, foreign entry kinds: Remove
	removeTrees     []string   // pruned directories: RemoveAll
	removeEmptyDirs []string   // dirs left (or found) empty, post-order — children recorded first
	retargets       []retarget // kept links whose relative target must be absolutized pre-hash
}

type retarget struct{ path, target string }

func (sc *filterScan) scanDir(dir string) (nonEmpty bool) {
	for _, info := range c.With("Read dir '%s'", dir).Check2(vfs.ReadDir(sc.store.fs, dir)) {
		if sc.scanEntry(filepath.Join(dir, info.Name()), info) {
			nonEmpty = true
		}
	}
	return nonEmpty
}

// Kind-blind: whatever occupies an input's path never publishes.
func (sc *filterScan) pruned(rel string) bool {
	return sc.PruneDirs.Contains(rel) || sc.ExtPrune.Contains(rel)
}

func (sc *filterScan) scanEntry(path string, info fs.FileInfo) bool {
	rel := filepath.ToSlash(c.Check2(filepath.Rel(sc.root, path)))
	if info.IsDir() {
		if sc.pruned(rel) {
			sc.removeTrees = append(sc.removeTrees, path) // input tree: never publishes
			return false
		}
		if sc.scanDir(path) {
			return true
		}
		if sc.dropEmpty {
			sc.removeEmptyDirs = append(sc.removeEmptyDirs, path)
			return false
		}
		return true // verbatim publish keeps (found-)empty dirs
	}
	isLink := info.Mode()&os.ModeSymlink != 0
	if !isLink && !info.Mode().IsRegular() {
		// sockets, fifos, devices: never publishable (HashDir would fail on them)
		sc.removePaths = append(sc.removePaths, path)
		return false
	}
	// Prune membership first — a link-staged dep dies by path, not by kind accident.
	if sc.pruned(rel) || (sc.Filter != nil && !sc.Filter.Included(rel)) {
		sc.removePaths = append(sc.removePaths, path)
		return false
	}
	if isLink {
		c.Require(sc.AllowLinks || sc.KeepLinks.Contains(rel), linkPolicyHint, path)
		sc.checkKeptLink(path)
	}
	return true
}

// Both the literal target and its EvalSymlinks-resolved form are checked, or a chain through an
// outside link could smuggle a sandbox target in. Each form against the root in the same form: a
// root behind a symlink (macOS /var) or an 8.3 short name matches only its own spelling.
func (sc *filterScan) checkKeptLink(path string) {
	raw, abs := resolveLinkTarget(sc.store.fs, path)
	isRel := !filepath.IsAbs(raw)
	resolved := c.With("Store: output symlink '%s' -> '%s'", path, raw).Check2(filepath.EvalSymlinks(abs))

	if isRel && Util.AtOrBelowIgnoreUnrelated(sc.movingRoot, abs) {
		return // intra-tree relative: survives the rename verbatim
	}
	c.Require(!inside(sc.movingRoot, sc.realMovingRoot, abs, resolved),
		"Store: output symlink '%s' targets '%s' inside the moving output tree", path, raw)
	c.Require(!sc.checkBldRoot || !inside(sc.bldRoot, sc.realBldRoot, abs, resolved),
		"Store: output symlink '%s' targets '%s' inside the bld sandbox", path, raw)
	if isRel {
		sc.retargets = append(sc.retargets, retarget{path, abs})
	}
}

func inside(root string, realRoot func() string, abs, resolved string) bool {
	return Util.AtOrBelowIgnoreUnrelated(root, abs) || Util.AtOrBelowIgnoreUnrelated(realRoot(), resolved)
}

func evalRoot(root string) string {
	return c.With("Store: resolve root '%s'", root).Check2(filepath.EvalSymlinks(root))
}

// Emptied dirs go in recorded post-order — every parent is empty by the time its Remove runs.
func (sc *filterScan) apply() {
	fs := sc.store.fs
	for _, path := range sc.removeTrees {
		c.Check(fs.RemoveAll(path))
	}
	for _, path := range sc.removePaths {
		c.Check(fs.Remove(path))
	}
	for _, path := range sc.removeEmptyDirs {
		c.Check(fs.Remove(path))
	}
	for _, rt := range sc.retargets {
		c.Check(fs.Remove(rt.path)) // os.Symlink refuses to overwrite
		vfsx.Symlink(fs, rt.path, rt.target)
	}
	// Runs even for a name the scan never matched: a script that consumed the artifact may still
	// have left its parents empty. Sorted — Set iteration order is random.
	for _, name := range slices.Sorted(maps.Keys(sc.ExtPrune)) {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if !vfsx.RemoveDirIfEmpty(fs, filepath.Join(sc.root, filepath.FromSlash(parent))) {
				break
			}
		}
	}
}

func (s *Store) publishDir(root, hash, dir string) {
	targetPath := filepath.Join(root, hash)
	slog.Info("publishDir", "dir", dir, "to", targetPath)

	if s.Contains(root, hash) {
		slog.Warn("Publish skipped: already present", "path", targetPath)
		c.Check(s.fs.RemoveAll(dir))
		return
	}
	c.Check(s.fs.MkdirAll(root, 0o755))
	if err := s.fs.Rename(dir, targetPath); err != nil {
		slog.Error("Store: publish failed", "path", targetPath, "err", err)
		// Lost a publish race for the same content — fine, it's there now.
		c.Require(s.Contains(root, hash), "Store: publish %s/%s failed: %v", root, hash, err)
		c.Check(s.fs.RemoveAll(dir))
	}
}

func (s *Store) copyTree(srcFs vfs.Fs, srcPath string, dstPath string, pruneNested bool,
	filter *FilterFiles.Filter, allowLinks bool,
) {
	slog.Debug("copyTree", "src", srcPath, "dst", dstPath)

	c.Check(s.fs.MkdirAll(dstPath, 0o755))
	skipFilter := filteredSkip(srcPath, pruneNested, filter, allowLinks)
	c.Require(isDirEmpty(s.fs, dstPath), "Store: destination %s is not empty", dstPath)
	s.copyDir(srcFs, srcPath, dstPath, skipFilter)
}

// Reuses vfs.ReadDir's FileInfo — unlike afero.Walk, which re-Lstats every entry (a separate OS
// open per file on Windows). A symlink is never followed.
func (s *Store) copyDir(srcFs vfs.Fs, srcDir, dstDir string, skipFilter func(vfs.Fs, string, fs.FileInfo) bool) {
	createdDirs := Util.NewSet[string]() // dstDirs already MkdirAll'd on the on-demand path
	for _, info := range c.With("Read dir '%s'", srcDir).Check2(vfs.ReadDir(srcFs, srcDir)) {
		src := filepath.Join(srcDir, info.Name())
		dst := filepath.Join(dstDir, info.Name())

		if info.IsDir() {
			if skipFilter != nil && skipFilter(srcFs, src, info) {
				continue // prune nested build unit / filtered-out subtree
			}
			if skipFilter == nil {
				c.Check(s.fs.MkdirAll(dst, 0o755)) // verbatim: keep empty dirs
			}
			s.copyDir(srcFs, src, dst, skipFilter)
			continue
		}
		isLink := info.Mode()&os.ModeSymlink != 0
		if !isLink && !info.Mode().IsRegular() {
			continue // sockets, fifos, devices: never staged
		}
		if skipFilter != nil {
			if skipFilter(srcFs, src, info) {
				continue
			}
			if !createdDirs.Contains(dstDir) {
				c.Check(s.fs.MkdirAll(dstDir, 0o755)) // on-demand
				createdDirs.Add(dstDir)
			}
		}
		if isLink {
			s.copyLink(srcFs, src, dst)
		} else {
			s.copyFile(srcFs, src, dst, info.Mode())
		}
	}
}

// A verbatim relative target would re-resolve against the destination. The referent is not
// consulted, so even a dangling link clones faithfully — the hash walk is where danglers fail.
func (s *Store) copyLink(srcFs vfs.Fs, src, dst string) {
	_, target := resolveLinkTarget(srcFs, src)
	slog.Debug("copyLink", "src", src, "dst", dst, "target", target)
	vfsx.Symlink(s.fs, dst, target)
}

// Shared by the publish scan and the staging clone, so they cannot disagree.
func resolveLinkTarget(fs vfs.Fs, path string) (raw, abs string) {
	raw = c.With("Readlink '%s'", path).Check2(vfsx.Readlink(fs, path))
	abs = raw
	if !filepath.IsAbs(raw) {
		abs = filepath.Join(filepath.Dir(UnsafeIO.OSPath(fs, path)), raw)
	}
	return raw, abs
}

// Reads a single name rather than the whole sorted listing; an empty dir yields io.EOF.
func isDirEmpty(fs vfs.Fs, dir string) bool {
	f := c.With("Open dir '%s'", dir).Check2(fs.Open(dir))
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != io.EOF {
		c.Check(err)
	}
	return len(names) == 0
}

// Every copyTree target is a freshly emptied tree, so a collision means a double-producer bug
// and the panic is wanted.
func (s *Store) copyFile(srcFs vfs.Fs, srcPath string, dstPath string, mode os.FileMode) {
	vfsx.XCopyFile(srcFs, srcPath, s.fs, dstPath, mode)
}

// CopyFileW propagates the source's attributes, and an ArtifactCache source is read-only.
func (s *Store) copyFileWritable(srcFs vfs.Fs, srcPath, dstPath string) {
	s.copyFile(srcFs, srcPath, dstPath, 0o644)
	c.Check(s.fs.Chmod(dstPath, 0o644))
}

func (s *Store) CopyFileIn(srcFs vfs.Fs, srcPath, dstPath string) {
	c.Check(s.fs.MkdirAll(filepath.Dir(dstPath), 0o755))
	s.copyFileWritable(srcFs, srcPath, dstPath)
}
