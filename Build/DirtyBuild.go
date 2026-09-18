package Build

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Cache"
	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

type DirtyBuilder struct {
	BuilderBase
	// srcDir -> current tree hash
	localHashCache Cache.Map[string, string]

	getDirtyHash func(string) string
	setDirtyHash func(srcDir, hash string)
}

func NewDirtyBuilder(rc Runtime.Config) *DirtyBuilder {
	b := &DirtyBuilder{
		BuilderBase:    newBuilderBase(rc),
		localHashCache: Cache.Map[string, string]{},
	}
	b.allowMaterializedVirtualDirs = true

	b.buildCached = Cache.WrapInt(
		b.build,
		b.localBuildCache,
		"Local DirtyBuild")

	getDirtyHash := Cache.Wrap_Def(
		b.computeDirtyHash,
		b.Daemon.GetDirtyHash,
		b.Daemon.SetDirtyHash,
		"Daemon DirtyHash")
	b.getDirtyHash = Cache.WrapInt(
		getDirtyHash,
		b.localHashCache,
		"Local DirtyHash")

	b.setDirtyHash = func(srcDir, hash string) {
		b.localHashCache.Set(srcDir, hash)
		b.Daemon.SetDirtyHash(srcDir, hash)
	}

	b.wireOutputLoaders(b.SrcFS, ".")

	b.wireGetBuildConfig()

	return b
}

// Compiled once per process, lazily — clean-mode invocations never pay the compile.
var noRulesDirtyFilter = sync.OnceValue(func() *FilterFiles.Filter { return srcFilterFor(nil) })

func dirtyFilterFor(rules []string) *FilterFiles.Filter {
	if len(rules) == 0 {
		return noRulesDirtyFilter()
	}
	return srcFilterFor(rules)
}

func (b *DirtyBuilder) computeDirtyHash(srcDir string) string {
	defer c.Context("Dirty hash '%s'", srcDir)

	cfg := b.getBuildConfig(srcDir)
	if cfg.VirtualDir && !vfsx.DirExistsFailOnFile(b.SrcFS, srcDir) {
		return Hashing.EmptyDirHash
	}
	return b.store.HashSrc(b.SrcFS, srcDir, dirtyFilterFor(cfg.Filters.Dirty), true)
}

func (b *DirtyBuilder) Build(srcDir string) string {
	srcDir = filepath.Clean(srcDir)
	slog.Info("+ DirtyBuild", "dir", srcDir)
	defer slog.Info("- DirtyBuild", "dir", srcDir)
	return b.buildCached(srcDir)
}

func (b *DirtyBuilder) build(srcDir string) string {
	defer c.Context("Dirty build '%s'", srcDir)
	defer b.circDepsCheck(srcDir)()

	buildConfig := b.getBuildConfig(srcDir)
	shell := b.shellFor(srcDir, buildConfig)

	bldDeps := effectiveBldDeps(srcDir, buildConfig, shell)
	_, bldDepsHash := depsHashes(srcDir, "bld.dep", bldDeps, b.Build)
	_, srcDepsHash := depsHashes(srcDir, "src.dep", buildConfig.Deps.Src, b.getDirtyHash)

	slog.Info("Config hash:", "kind", "config", "dir", srcDir, "hash", buildConfig.Hash)

	skipHashFn := func(ownHash string) string {
		combinedInputHash := Hashing.BuildPrefix + Hashing.CombineHashesInPlace(
			b.combinedBuildInputs(srcDir, buildConfig.Hash, bldDepsHash, ownHash, srcDepsHash))
		slog.Info("Combined dirty inputs hash:", "dir", srcDir, "hash", combinedInputHash)
		return combinedInputHash
	}
	// Memoized: more than one skip-hash tier folds the same ownHash.
	skipHashFn = Cache.Memoize(skipHashFn, "SkipHash")

	build := func(srcDir string) string {
		return b.realDirtyBuild(srcDir, buildConfig, skipHashFn, shell, bldDeps)
	}

	wrappedBuild, noCallCheck := WrapNoCall(build, func() {
		slog.Info("Dirty build cache hit — skipping build", "dir", srcDir)
		fmt.Fprintln(b.Out, "Already built:", srcDir)
	})
	defer noCallCheck()

	storeCheckSkipHash := func(dir string) string {
		return b.checkSkipHash(dir, b.store.GetDirtySkipHash(dir), skipHashFn)
	}
	daemonCheckSkipHash := func(dir string) string {
		return b.checkSkipHash(dir, b.Daemon.GetDirtySkipHash(dir), skipHashFn)
	}

	if b.ForceRebuildFor(srcDir) {
		slog.Info("Forced dirty rebuild — bypassing skip hashes", "dir", srcDir)
		storeCheckSkipHash = alwaysMiss
		daemonCheckSkipHash = alwaysMiss
	}

	build = Cache.Wrap_Def(
		wrappedBuild,
		storeCheckSkipHash,
		func(dir, postHash string) { b.store.SetDirtySkipHash(dir, skipHashFn(postHash)) },
		"Store DirtySkipHash")

	build = Cache.Wrap_Def(
		build,
		daemonCheckSkipHash,
		func(dir, postHash string) { b.Daemon.SetDirtySkipHash(dir, skipHashFn(postHash)) },
		"Daemon DirtySkipHash")

	return build(srcDir)
}

func (b *DirtyBuilder) checkSkipHash(srcDir, skipHash string, skipHashFn func(string) string) string {
	if skipHash == "" {
		return ""
	}
	var ownHash string
	if err := c.Rescue(func() { ownHash = b.getDirtyHash(srcDir) }); err != nil {
		// The pre-build walk is advisory: a tree that cannot hash simply cannot skip — build instead.
		slog.Warn("Dirty pre-build hash failed — building", "dir", srcDir, "err", err)
		return ""
	}
	slog.Info("Own dirty hash:", "kind", "src", "dir", srcDir, "hash", ownHash)
	if skipHash == skipHashFn(ownHash) {
		return ownHash
	}
	return ""
}

func (b *DirtyBuilder) realDirtyBuild(
	srcDir string,
	buildConfig BuildConfig.BufaConfig,
	skipHashFn func(string) string,
	shell string,
	bldDeps []string,
) string {
	fmt.Fprintln(b.Out, "Dirty-Building dir:", srcDir)

	if buildConfig.VirtualDir {
		c.Check(b.SrcFS.MkdirAll(srcDir, 0o755))
	}

	if b.placeExtDeps(srcDir, buildConfig) && !b.ForceRebuildFor(srcDir) {
		// Heal-skip: placement is bufa's own bookkeeping, not user change — a matching tree must NOT re-run.
		if stored := b.store.GetDirtySkipHash(srcDir); stored != "" {
			fresh := b.computeDirtyHash(srcDir)
			if stored == skipHashFn(fresh) {
				slog.Info("Dirty heal matched skip hash — skipping script", "dir", srcDir, "hash", fresh)
				b.setDirtyHash(srcDir, fresh)
				return fresh
			}
		}
	}

	if !buildConfig.IsScriptOptional() || b.ShellSessionFor(srcDir) {
		b.runDirtyBuildScript(srcDir, buildConfig, shell, bldDeps)
	}

	if b.ShellSessionFor(srcDir) {
		slog.Info("Shell session over — nothing to record", "dir", srcDir)
		return ""
	}

	// computeDirtyHash, NOT the cached getDirtyHash, which still holds the pre-build hash.
	postHash := b.computeDirtyHash(srcDir)
	slog.Info("Post-build dirty hash:", "dir", srcDir, "hash", postHash)
	b.setDirtyHash(srcDir, postHash)
	return postHash
}

func (b *DirtyBuilder) runDirtyBuildScript(srcDir string, cfg BuildConfig.BufaConfig, shell string, bldDeps []string) {
	c.Check(b.BldFS.MkdirAll(Store.TmpRoot, 0o755))
	shellDef := b.loadShellDef(shell)
	env := newEnv(os.Environ())
	b.setBufaEnv(env, b.SrcRoot, srcDir, shellDef.Copy)
	b.setCacheDirEnv(env, srcDir, cfg)
	b.setAllEnvVars(env, srcDir, cfg, b.depEnvsFor(bldDeps))
	b.runScriptStep(srcDir, filepath.Join(b.SrcRoot, srcDir), env, cfg, shellDef)
}
