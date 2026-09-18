// Package Build builds a project directory.
package Build

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Cache"
	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

type Builder struct {
	BuilderBase

	getSrcHash func(string) string
}

func NewBuilder(rc Runtime.Config) *Builder {
	b := &Builder{
		BuilderBase: newBuilderBase(rc),
	}

	b.buildCached = Cache.WrapInt(
		b.build,
		b.localBuildCache,
		"Local BuildHash",
	)

	getSrcHash := Cache.Wrap_Def(
		b.storeGetSrcHash,
		b.Daemon.GetSrcHash,
		b.Daemon.SetSrcHash,
		"Daemon SrcHash",
	)
	b.getSrcHash = Cache.WrapInt(
		getSrcHash,
		b.localSrcCache,
		"Local SrcHash")

	b.wireOutputLoaders(b.BldFS, Store.BldSandboxRoot)

	b.wireGetBuildConfig()

	return b
}

func (b *Builder) storeGetSrcHash(srcDir string) string {
	defer c.Context("Hash source dir '%s'", srcDir)

	cfg := b.getBuildConfig(srcDir)
	if cfg.VirtualDir {
		return b.store.SrcPrepEmpty(srcDir)
	}
	b.requireExtDepSourceNamesAvailable(srcDir, cfg)
	return b.store.SrcPrep(b.SrcFS, srcDir, srcFilterFor(cfg.Filters.Src), cfg.Unsafe)
}

func bldFilterForOrNil(rules []string) *FilterFiles.Filter {
	if len(rules) == 0 {
		return nil
	}
	return FilterFiles.Compile(rules)
}

func (b *Builder) storeGetBuildHash(combinedInputHash string) string {
	return b.store.GetLinkTarget(Store.OutRoot, combinedInputHash)
}

func (b *Builder) storeSetBuildHash(srcDir, combinedInputHash, buildHash string) {
	prevBuildHash := b.store.Link(Store.OutRoot, combinedInputHash, buildHash) // B<combined> -> D<buildHash>
	// Re-pointed B ⇒ same tracked inputs, different output. Loud, NOT fatal — the new output is cached.
	if prevBuildHash != "" && prevBuildHash != buildHash {
		slog.Warn("Build is not reproducible", "dir", srcDir, "combined", combinedInputHash, "old_hash", prevBuildHash, "hash", buildHash)
		fmt.Fprintf(b.Out,
			"WARNING: Dir '%s' build is not reproducible: different output hash for the same inputs; "+
				"untracked inputs or a non-deterministic script?\n"+
				"         Hash changed %s -> %s\n",
			srcDir, prevBuildHash, buildHash)
	}
}

func (b *Builder) unsafeTTLHash(srcDir string) string {
	ttl := b.RootConfig.Unsafe.TTL
	bucket := b.BuildStartTimeUTC.Truncate(ttl)
	bucketStr := bucket.Format("2006-01-02T15:04:05.000Z07:00")
	ttlHash := "T" + Hashing.HashBytes([]byte(bucketStr))

	slog.Info("Unsafe TTL for", "dir", srcDir, "bucket", bucketStr, "hash", ttlHash)
	return ttlHash
}

func (b *Builder) Build(srcDir string) string {
	srcDir = filepath.Clean(srcDir)
	slog.Info("+ Build", "dir", srcDir)
	defer slog.Info("- Build", "dir", srcDir)
	return b.buildCached(srcDir)
}

func (b *Builder) build(srcDir string) string {
	defer c.Context("Build '%s'", srcDir)
	defer b.circDepsCheck(srcDir)()

	buildConfig := b.getBuildConfig(srcDir)
	shell := b.shellFor(srcDir, buildConfig)

	bldDeps := effectiveBldDeps(srcDir, buildConfig, shell)
	bldDepsHashes, bldDepsHash := depsHashes(srcDir, "bld.dep", bldDeps, b.Build)
	srcDepsHashes, srcDepsHash := depsHashes(srcDir, "src.dep", buildConfig.Deps.Src, b.getSrcHash)

	srcHash := b.getSrcHash(srcDir)
	slog.Info("Src hash:", "kind", "src", "dir", srcDir, "hash", srcHash)

	slog.Info("Config hash:", "kind", "config", "dir", srcDir, "hash", buildConfig.Hash)

	buildInputs := b.combinedBuildInputs(srcDir, buildConfig.Hash, bldDepsHash, srcHash, srcDepsHash)
	if buildConfig.Unsafe {
		buildInputs = append(buildInputs, Hashing.NamedEntry{Name: "unsafe.ttl", Hash: b.unsafeTTLHash(srcDir)})
	}

	combinedInputHash := Hashing.BuildPrefix + Hashing.CombineHashesInPlace(buildInputs)
	slog.Info("Combined inputs hash:", "dir", srcDir, "hash", combinedInputHash)

	build := func(_combinedInputHash string) string {
		return b.realBuild(srcDir, srcHash, srcDepsHashes, bldDepsHashes, bldDeps, buildConfig, shell)
	}

	wrappedBuild, noCallCheck := WrapNoCall(build, func() {
		slog.Info("Build cache hit — skipping build", "dir", srcDir)
		fmt.Fprintln(b.Out, "Already built:", srcDir)
	})
	defer noCallCheck()

	storeGetBuildHash := b.storeGetBuildHash

	pathBuildHashCurrent := false
	daemonGetBuildHash := func(combined string) string {
		var buildHash string
		buildHash, pathBuildHashCurrent = b.Daemon.GetBuildHash(combined, srcDir)
		return buildHash
	}

	if b.ForceRebuildFor(srcDir) {
		slog.Info("Forced rebuild — bypassing build caches", "dir", srcDir)
		storeGetBuildHash = alwaysMiss
		daemonGetBuildHash = alwaysMiss
	}

	build = Cache.Wrap_Def(
		wrappedBuild,
		storeGetBuildHash,
		func(combined, buildHash string) { b.storeSetBuildHash(srcDir, combined, buildHash) },
		"Store BuildHash")

	build = Cache.Wrap_Def(
		build,
		daemonGetBuildHash,
		b.Daemon.SetBuildHash,
		"Daemon BuildHash")

	buildHash := build(combinedInputHash)

	// A shell session builds nothing: no GC root to refresh either.
	if buildHash != "" && !pathBuildHashCurrent {
		b.store.LinkPath(Store.OutRoot, srcDir, combinedInputHash) // GC root: out/∕<srcDir> -> B<combined>
		b.Daemon.SetPathBuildHash(srcDir, combinedInputHash)
	}
	return buildHash
}

func (b *Builder) realBuild(
	srcDir string,
	srcHash string,
	srcDepsHashes []Hashing.NamedEntry,
	bldDepsHashes []Hashing.NamedEntry, // name-sorted by CombineHashesInPlace; bldDeps keeps dep order
	bldDeps []string,
	buildConfig BuildConfig.BufaConfig,
	shell string,
) string {
	fmt.Fprintln(b.Out, "Building dir:", srcDir)
	bldDir := filepath.Join(Store.BldSandboxRoot, srcDir)

	type stageDir struct {
		srcRoot, srcHash, dstPath string
		byLink                    bool
	}

	stageDirs := []stageDir{{Store.InRoot, srcHash, srcDir, false}}
	for _, dep := range srcDepsHashes {
		stageDirs = append(stageDirs, stageDir{Store.InRoot, dep.Hash, dep.Name, false})
	}
	for _, dep := range bldDepsHashes {
		stageDirs = append(stageDirs, stageDir{Store.OutRoot, dep.Hash, dep.Name, b.getBuildConfig(dep.Name).LargeOutput})
	}

	sep := string(filepath.Separator)
	depth := func(name string) int {
		if name == "." {
			return 0
		}
		return strings.Count(name, sep) + 1
	}
	slices.SortStableFunc(stageDirs, func(x, y stageDir) int {
		if d := depth(x.dstPath) - depth(y.dstPath); d != 0 {
			return d
		}
		return strings.Compare(x.dstPath, y.dstPath)
	})

	for i, dir := range stageDirs {
		if dir.byLink {
			for _, dir2 := range stageDirs[i+1:] {
				c.Require(!Util.StrictlyBelow(dir.dstPath, dir2.dstPath),
					"Dependency '%s' marked with largeOutput (staged by link) must be a leaf, but it has dependency '%s' below it", dir.dstPath, dir2.dstPath)
			}
		}
	}

	exportDeps := Util.NewSet[string]()
	for _, dir := range buildConfig.Deps.Export {
		exportDeps.Add(filepath.ToSlash(c.Check2(filepath.Rel(srcDir, dir))))
	}
	nestedDeps := Util.NewSet[string]()
	pruneDirs := Util.NewSet[string]()
	for _, dir := range stageDirs {
		if rel := c.Check2(filepath.Rel(srcDir, dir.dstPath)); Util.RelStrictlyBelow(rel) {
			rel = filepath.ToSlash(rel)
			nestedDeps.Add(rel)
			if exportDeps.Contains(rel) {
				c.Require(!dir.byLink,
					"Dependency '%s' marked with largeOutput (staged by link) cannot be exported with the output of '%s'", dir.dstPath, srcDir)
			} else {
				pruneDirs.Add(rel)
			}
		}
	}

	// RemoveAll never follows symlinks, so leftover link objects go without traversal.
	c.Check(b.BldFS.RemoveAll(Store.BldSandboxRoot))

	for _, dir := range stageDirs {
		slog.Info("Staging", "dir", dir.dstPath, "byLink", dir.byLink)
		dst := filepath.Join(Store.BldSandboxRoot, dir.dstPath)
		if dir.byLink {
			b.store.RestoreLink(dir.srcRoot, dir.srcHash, dst)
		} else {
			b.store.Restore(dir.srcRoot, dir.srcHash, dst)
		}
	}

	b.stageExtDeps(srcDir, buildConfig, nestedDeps)

	if !buildConfig.IsScriptOptional() || b.ShellSessionFor(srcDir) {
		b.runBuildScript(srcDir, bldDir, buildConfig, shell, bldDeps)
	} else {
		slog.Info("No build script in", "dir", srcDir)
	}

	if b.ShellSessionFor(srcDir) {
		// Sandbox kept for inspection, like a failure's; the next build wipes it.
		slog.Info("Shell session over — nothing to publish", "dir", srcDir)
		return ""
	}

	extPrune, keepLinks := extDepPublishPlan(buildConfig, exportDeps)
	buildHash := b.store.MoveStore(Store.OutRoot, bldDir, Store.PublishPlan{
		Filter:     bldFilterForOrNil(buildConfig.Filters.Bld),
		PruneDirs:  pruneDirs,
		ExtPrune:   extPrune,
		KeepLinks:  keepLinks,
		AllowLinks: buildConfig.Unsafe,
	})
	slog.Info("Build hash:", "dir", srcDir, "hash", buildHash)

	// Success only — NOT deferred: a panic above must leave the sandbox intact
	c.Check(b.BldFS.RemoveAll(Store.BldSandboxRoot))
	return buildHash
}

func (b *Builder) runBuildScript(srcDir, bldDir string, cfg BuildConfig.BufaConfig, shell string, bldDeps []string) {
	// Fresh tmp/: a failed build's leftovers must not reach this script through TEMP.
	c.Check(b.BldFS.RemoveAll(Store.TmpRoot))
	c.Check(b.BldFS.MkdirAll(Store.TmpRoot, 0o755))

	osBldDir := filepath.Join(b.BldRoot, bldDir)
	osTmpDir := filepath.Join(b.BldRoot, Store.TmpRoot)
	osSandboxRoot := filepath.Join(b.BldRoot, Store.BldSandboxRoot)
	shellDef := b.loadShellDef(shell)
	env := inheritedEnv(cfg.Unsafe)
	b.setBufaEnv(env, osSandboxRoot, srcDir, shellDef.Move)
	if !cfg.Unsafe {
		env.Set("PATH", cleanSystemPath())
	}
	b.setCacheDirEnv(env, srcDir, cfg)
	setPlatformEnv(env, osTmpDir, cfg.Unsafe)
	// Last on purpose: ${NAME} refs must see the complete env, and an [env] namesake overrides any bufa var.
	b.setAllEnvVars(env, srcDir, cfg, b.depEnvsFor(bldDeps))

	b.runScriptStep(srcDir, osBldDir, env, cfg, shellDef)
}

func inheritedEnv(unsafe bool) *Env {
	environ := os.Environ()
	if unsafe {
		return newEnv(environ)
	}
	return newFilteredEnv(environ, safeInheritedEnv)
}

// Executables and batch files only — no script hosts (stock .VBS…WSH), no installer additions (.PS1, .PY, …).
const cleanPathExt = ".COM;.EXE;.BAT;.CMD"

func setPlatformEnv(env *Env, osTmpDir string, unsafe bool) {
	if runtime.GOOS == "windows" {
		env.Set("TEMP", osTmpDir)
		env.Set("TMP", osTmpDir)
		if !unsafe {
			env.Set("PATHEXT", cleanPathExt)
			env.Set("ComSpec", cleanComSpec())
		}
	} else {
		env.Set("TMPDIR", osTmpDir)
		if !unsafe {
			env.Set("HOME", osTmpDir) // synthetic home, wiped with tmp/
		}
	}
}

var cleanSystemPath = sync.OnceValue(func() string {
	if runtime.GOOS == "windows" {
		// ExpandEnv handles ${VAR}, not cmd's %VAR%; SystemRoot is always set on Windows.
		return os.ExpandEnv(`${SystemRoot}\System32;${SystemRoot};${SystemRoot}\System32\Wbem;${SystemRoot}\System32\WindowsPowerShell\v1.0`)
	}
	return "/usr/bin:/bin:/usr/sbin:/sbin"
})

var cleanComSpec = sync.OnceValue(func() string {
	if runtime.GOOS == "windows" {
		return os.ExpandEnv(`${SystemRoot}\System32\cmd.exe`)
	}
	return "" // unused on Unix
})
