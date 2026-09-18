package Build

import (
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"github.com/dimagog/bufa/ArtifactCache"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

func normalizeExtDeps(deps []BuildConfig.ExtDep) []BuildConfig.ExtDep {
	seen := Util.NewSet[string]()
	for i := range deps {
		dep := &deps[i]
		c.Require(dep.Url != "", "deps.ext entry has no url")
		c.Require(dep.Hash == ArtifactCache.QueryHashSentinel || Hashing.IsValidFileHash(dep.Hash),
			"deps.ext '%s': hash must be a bufa '%s' file hash or \"%s\"",
			dep.Url, Hashing.FilePrefix, ArtifactCache.QueryHashSentinel)
		if dep.Name == "" {
			parsed := c.With("Parse deps.ext url '%s'", dep.Url).Check2(url.Parse(dep.Url))
			dep.Name = path.Base(parsed.Path)
		}
		name := cleanLocalName("deps.ext name", dep.Name)
		key := Util.NormalizePath(name)
		c.Require(!seen.Contains(key), "duplicate deps.ext name '%s'", name)
		seen.Add(key)
		dep.Name = name
	}
	return deps
}

func cleanLocalName(what, raw string) string {
	name := path.Clean(filepath.ToSlash(raw))
	c.Require(Util.RelStrictlyBelow(filepath.FromSlash(name)),
		"%s '%s' must be a strictly-local relative path", what, raw)
	base := path.Base(name)
	c.Require(!Store.IsReservedName(base), "%s '%s': basename '%s' is reserved by bufa", what, raw, base)
	return name
}

func extDepPath(srcDir string, dep BuildConfig.ExtDep) string {
	return filepath.Join(srcDir, filepath.FromSlash(dep.Name))
}

// Lstat, so empty dirs and dangling links count as occupants too.
func (b *Builder) requireExtDepSourceNamesAvailable(srcDir string, cfg BuildConfig.BufaConfig) {
	for _, dep := range cfg.Deps.Ext {
		sourcePath := extDepPath(srcDir, dep)
		c.Require(!vfsx.DirEntryExists(b.SrcFS, sourcePath),
			"deps.ext name '%s' is already occupied in '%s' source (perhaps dirty-build output needs cleaning)", dep.Name, srcDir)
	}
}

func (b *BuilderBase) ensureExtDep(srcDir string, dep BuildConfig.ExtDep) string {
	defer c.Context("External dep '%s' of '%s'", dep.Name, srcDir)
	return b.artifactCache().Ensure(dep.Url, dep.Hash, b.Out)
}

func (b *BuilderBase) ensureExtDepURLBinding(srcDir string, dep BuildConfig.ExtDep) {
	defer c.Context("External dep '%s' of '%s'", dep.Name, srcDir)
	b.artifactCache().EnsureURLBinding(dep.Url, dep.Hash, b.Out)
}

func (b *Builder) stageExtDeps(srcDir string, cfg BuildConfig.BufaConfig, nestedDeps Util.Set[string]) {
	for _, dep := range cfg.Deps.Ext {
		extPath := extDepPath(srcDir, dep)
		for rel := range nestedDeps {
			target := filepath.Join(srcDir, filepath.FromSlash(rel))
			c.Require(!Util.PathsOverlap(target, extPath),
				"deps.ext name '%s' in '%s' collides with the staged tree of dependency '%s'", dep.Name, srcDir, target)
		}
		stagePath := filepath.Join(Store.BldSandboxRoot, extPath)
		c.Require(!vfsx.DirEntryExists(b.BldFS, stagePath),
			"deps.ext name '%s' is already occupied in '%s' sandbox (perhaps dirty-build output needs cleaning)", dep.Name, srcDir)
		slog.Info("Staging ext dep", "dir", srcDir, "name", dep.Name, "byLink", dep.Large)
		b.ensureExtDep(srcDir, dep)
		if dep.Large {
			vfsx.XSymlink(b.artifactCache().Fs(), dep.Hash, b.BldFS, stagePath)
		} else {
			b.store.CopyFileIn(b.artifactCache().Fs(), dep.Hash, stagePath)
		}
	}
}

// exportDeps holds dir-relative slash paths, the form Name already has.
func extDepPublishPlan(cfg BuildConfig.BufaConfig, exportDeps Util.Set[string]) (extPrune, keepLinks Util.Set[string]) {
	if len(cfg.Deps.Ext) == 0 {
		return nil, nil
	}
	extPrune = Util.NewSet[string]()
	keepLinks = Util.NewSet[string]()
	for _, dep := range cfg.Deps.Ext {
		if !exportDeps.Contains(dep.Name) {
			extPrune.Add(dep.Name)
		} else if dep.Large {
			keepLinks.Add(dep.Name)
		}
	}
	return extPrune, keepLinks
}

// A stale placement is re-placed only when provably bufa's own; anything else is a collision,
// never silently replaced.
func (b *DirtyBuilder) placeExtDeps(srcDir string, cfg BuildConfig.BufaConfig) (changed bool) {
	if len(cfg.Deps.Ext) == 0 {
		return false // don't resolve the cache dir for a build that never touches it
	}
	cache := b.artifactCache()
	for _, dep := range cfg.Deps.Ext {
		placePath := extDepPath(srcDir, dep)
		if info, err := vfsx.TryLstat(b.SrcFS, placePath); err == nil { // an occupant; absent ⇒ place below
			c.Require(!info.IsDir(),
				"deps.ext name '%s' in '%s' is occupied by an existing directory", dep.Name, srcDir)
			if info.Mode()&os.ModeSymlink != 0 {
				target := c.With("Readlink '%s'", placePath).Check2(vfsx.Readlink(b.SrcFS, placePath))
				if dep.Large && target == cache.EntryPath(dep.Hash) && cache.Contains(dep.Hash) {
					// Current bytes don't prove the url produced them — the U-link gate must still pass.
					b.ensureExtDepURLBinding(srcDir, dep)
					slog.Info("Ext dep current", "dir", srcDir, "name", dep.Name, "byLink", true)
					continue // current — the link is bufa's own, under the pinned hash
				}
				c.Require(cache.Owns(target),
					"deps.ext name '%s' in '%s' is occupied by a foreign symlink to '%s'", dep.Name, srcDir, target)
				c.Check(b.SrcFS.Remove(placePath))
			} else {
				fileHash := Hashing.HashFile(b.SrcFS, placePath)
				if !dep.Large && fileHash == dep.Hash {
					// Full Ensure, not just the U-link gate: the placement's bytes don't prove the cache entry exists.
					b.ensureExtDep(srcDir, dep)
					slog.Info("Ext dep current", "dir", srcDir, "name", dep.Name, "byLink", false)
					continue
				}
				c.Require(fileHash == dep.Hash || cache.Contains(fileHash),
					"deps.ext name '%s' in '%s' is occupied by a foreign or modified file", dep.Name, srcDir)
				c.Check(b.SrcFS.Remove(placePath))
			}
		}
		slog.Info("Placing ext dep", "dir", srcDir, "name", dep.Name, "byLink", dep.Large)
		b.ensureExtDep(srcDir, dep)
		if dep.Large {
			vfsx.XSymlink(cache.Fs(), dep.Hash, b.SrcFS, placePath)
		} else {
			c.Check(b.SrcFS.MkdirAll(filepath.Dir(placePath), 0o755))
			vfsx.XCopyFile(cache.Fs(), dep.Hash, b.SrcFS, placePath, 0o644)
			c.Check(b.SrcFS.Chmod(placePath, 0o644)) // CopyFileW propagates the cache entry's read-only attribute
		}
		changed = true
	}
	return changed
}
