package Build

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/ArtifactCache"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Cache"
	"github.com/dimagog/bufa/FilterFiles"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

// The build entry point both Builder and DirtyBuilder expose.
type IBuild interface {
	Build(srcDir string) string
}

type BuilderBase struct {
	Runtime.Config
	store *Store.Store
	// circular build dependency guard
	buildingNow Util.Set[string]
	// srcDir -> build result hash: published output (clean), post-build tree hash (dirty)
	localBuildCache Cache.Map[string, string]
	// srcDir -> parsed BUFA
	localConfigCache Cache.Map[string, BuildConfig.BufaConfig]
	// srcDir -> staged-source hash; dirty fills it but never reads it
	localSrcCache Cache.Map[string, string]
	// resolved shell -> loaded definition
	localShellDefCache Cache.Map[string, BuildConfig.ShellDef]
	// dep dir -> its published BUFA.env exports (zero vars when absent)
	localDepEnvCache Cache.Map[string, depEnv]

	allowMaterializedVirtualDirs bool

	// RootConfig.GetHash(): the root marker's settings that re-key every dir
	rootConfigHash string

	artifactCache func() *ArtifactCache.Cache

	// wired by the concrete constructors
	buildCached    func(string) string
	getBuildConfig func(string) BuildConfig.BufaConfig
	loadShellDef   func(string) BuildConfig.ShellDef
	loadDepEnv     func(string) depEnv
}

func newBuilderBase(rc Runtime.Config) BuilderBase {
	rootConfigHash := rc.RootConfig.GetHash()
	slog.Info("Root config hash:", "kind", "root.config", "hash", rootConfigHash)
	Store.AllowBufaDir = rc.RootConfig.AllowBufaDir
	return BuilderBase{
		Config:             rc,
		store:              Store.NewStore(rc.BldFS),
		buildingNow:        Util.NewSet[string](),
		localBuildCache:    Cache.Map[string, string]{},
		localConfigCache:   Cache.Map[string, BuildConfig.BufaConfig]{},
		localSrcCache:      Cache.Map[string, string]{},
		localShellDefCache: Cache.Map[string, BuildConfig.ShellDef]{},
		localDepEnvCache:   Cache.Map[string, depEnv]{},
		rootConfigHash:     rootConfigHash,
		artifactCache: sync.OnceValue(func() *ArtifactCache.Cache {
			return ArtifactCache.New(ArtifactCache.GetCacheDir())
		}),
	}
}

func (b *BuilderBase) newShellDefLoader(fs vfs.Fs, fsRoot string) func(string) BuildConfig.ShellDef {
	return Cache.WrapInt(
		func(shell string) BuildConfig.ShellDef { return loadShellDef(shell, fs, fsRoot) },
		b.localShellDefCache,
		"ShellDef", "no-value",
	)
}

// Both loaders read a dep's published output; one wiring keeps them on the same mode-output tree.
func (b *BuilderBase) wireOutputLoaders(fs vfs.Fs, fsRoot string) {
	b.loadShellDef = b.newShellDefLoader(fs, fsRoot)
	b.loadDepEnv = b.newDepEnvLoader(fs, fsRoot)
}

// A method, not part of newBuilderBase: the method value must bind the base embedded in the
// finished builder, not the pre-copy temporary a closure there would capture.
func (b *BuilderBase) wireGetBuildConfig() {
	b.getBuildConfig = Cache.WrapInt(
		b.fetchBuildConfig,
		b.localConfigCache,
		"Local BuildConfig", "no-value")
}

// A src hash is content-addressed, so an already-present entry must match.
func (b *BuilderBase) mergeKnownHashes(knownHashes map[string]string) {
	newHashes := 0
	for dir, hash := range knownHashes {
		if existing, ok := b.localSrcCache.Get(dir); ok {
			c.Require(existing == hash, "srcHash mismatch for '%s': cached '%s' != bundled '%s'", dir, existing, hash)
			continue
		}
		b.localSrcCache.Set(dir, hash)
		newHashes++
	}
	slog.Info("Merged GetBuildConfig-bundled src hashes into local src cache:", "received", len(knownHashes), "new", newHashes)
}

func (b *BuilderBase) fetchBuildConfig(srcDir string) BuildConfig.BufaConfig {
	if buildConfig, knownHashes, ok := b.Daemon.GetBuildConfig(srcDir); ok {
		c.Assert(buildConfig.Hash != "", "daemon returned config without content hash for '%s'", srcDir)
		b.requireVirtualDirAbsent(srcDir, buildConfig)
		slog.Info("Cache 'Daemon BuildConfig' hit:", "dir", srcDir)
		b.mergeKnownHashes(knownHashes)
		return buildConfig
	}
	buildConfig := b.readBuildConfig(srcDir)
	b.requireVirtualDirAbsent(srcDir, buildConfig)
	b.Daemon.SetBuildConfig(srcDir, buildConfig)
	return buildConfig
}

func (b *BuilderBase) requireVirtualDirAbsent(srcDir string, cfg BuildConfig.BufaConfig) {
	c.Require(b.allowMaterializedVirtualDirs || !cfg.VirtualDirMaterialized,
		"Virtual build config '%s' exists, so '%s' must not exist in source (perhaps dirty-build output needs cleaning)",
		Store.VirtualConfigPathForDir(srcDir), srcDir)
}

// "-*.BUFA" is UNANCHORED, and "*" matches the empty run too, so it covers ".BUFA" but never
// bare "BUFA" — which "-/BUFA" takes, root-anchored.
var (
	srcFiltersDefault  = []string{"-.**"}
	srcFiltersOverride = []string{"-/" + Store.BuildConfigName, "-*" + Store.VirtualConfigSuffix}
)

func srcFilterFor(rules []string) *FilterFiles.Filter {
	return FilterFiles.Compose(srcFiltersDefault, rules, srcFiltersOverride)
}

var buildPlatform = runtime.GOOS + "/" + runtime.GOARCH

func (b *BuilderBase) setBufaEnv(env *Env, osBuildRoot, srcDir, copyOrMoveVerb string) {
	env.Set("BUFA_BUILD_ROOT", osBuildRoot)
	env.Set("BUFA_BUILD_DIR", srcDir)
	env.Set("BUFA_CACHE_ROOT", filepath.Join(b.BldRoot, Store.UserRoot))
	if copyOrMoveVerb != "" {
		env.Set("BUFA_COPY_OR_MOVE", copyOrMoveVerb)
	}
}

// Not hoisted into build(): a cache hit must never touch user/.
func (b *BuilderBase) setCacheDirEnv(env *Env, srcDir string, cfg BuildConfig.BufaConfig) {
	if cfg.Deps.CacheDir {
		cacheDir := b.store.EnsureCacheDir(srcDir)
		slog.Info("Cache dir", "dir", srcDir, "cacheDir", cacheDir)
		env.Set("BUFA_CACHE_DIR", cacheDir)
	}
}

func depsHashes(srcDir, kind string, deps []string, getHash func(string) string) ([]Hashing.NamedEntry, string) {
	hashes := make([]Hashing.NamedEntry, 0, len(deps))
	for _, dep := range deps {
		hash := getHash(dep)
		slog.Info("Dep hash:", "kind", kind, "dir", dep, "hash", hash)
		hashes = append(hashes, Hashing.NamedEntry{Name: dep, Hash: hash})
	}
	combined := Hashing.CombineHashesInPlace(hashes)
	slog.Info("Combined deps hash:", "kind", kind, "dir", srcDir, "hash", combined)
	return hashes, combined
}

func (b *BuilderBase) combinedBuildInputs(srcDir, configHash, bldDepsHash, srcHash, srcDepsHash string) []Hashing.NamedEntry {
	return []Hashing.NamedEntry{
		{Name: "bld.deps", Hash: bldDepsHash},
		{Name: "config", Hash: configHash},
		{Name: "srcPath", Hash: Hashing.HashPath(filepath.ToSlash(srcDir))},
		{Name: "platform", Hash: buildPlatform},
		{Name: "root.config", Hash: b.rootConfigHash},
		{Name: "src", Hash: srcHash},
		{Name: "src.deps", Hash: srcDepsHash},
	}
}

// Forced-rebuild getter: swapped in for a cache tier's getter, its setter stays.
func alwaysMiss(string) string { return "" }

func WrapNoCall[In, Out any](
	funcToWrap func(in In) Out,
	noCallAction func(),
) (wrappedFunc func(in In) Out, noCallCheck func()) {
	called := false
	wrappedFunc = func(in In) Out {
		called = true
		return funcToWrap(in)
	}
	noCallCheck = func() {
		if !called {
			noCallAction()
		}
	}
	return
}

func (b *BuilderBase) circDepsCheck(srcDir string) func() {
	c.Require(!b.buildingNow.Contains(srcDir), "circular build dependency involving %s", srcDir)
	b.buildingNow.Add(srcDir)
	return func() {
		b.buildingNow.Remove(srcDir)
	}
}

func (b *BuilderBase) readBuildConfig(srcDir string) BuildConfig.BufaConfig {
	slog.Info("Reading BuildConfig for", "dir", srcDir)
	data, cfgPath, virtual, materialized := b.locateBuildConfig(srcDir)

	var cfg BuildConfig.BufaConfig
	BuildConfig.DecodeConfigOrScript(data, cfgPath, &cfg)
	cfg.Hash = Hashing.FilePrefix + Hashing.HashBytes(data)
	cfg.VirtualDir = virtual
	cfg.VirtualDirMaterialized = materialized

	for i, dir := range cfg.Deps.Bld {
		cfg.Deps.Bld[i] = resolveRelDir(srcDir, dir)
	}
	for i, dir := range cfg.Deps.Src {
		cfg.Deps.Src[i] = resolveRelDir(srcDir, dir)
	}
	// Before normalize: the default ext Name derives from the FINAL url.
	b.resolveEnvVars(srcDir, cfg.Env, cfg.VirtualDir)
	interpolateExtDepUrls(cfg.Deps.Ext, cfg.Env)
	cfg.Deps.Ext = normalizeExtDeps(cfg.Deps.Ext)
	resolveExportDeps(srcDir, &cfg.Deps)
	return cfg
}

func (b *BuilderBase) locateBuildConfig(srcDir string) (data []byte, cfgPath string, virtual, materialized bool) {
	directPath := filepath.Join(srcDir, Store.BuildConfigName)
	directData := vfsx.TryReadSmallFileFast(b.SrcFS, directPath) // nil ⇒ no real BUFA

	virtualPath := Store.VirtualConfigPathForDir(srcDir)
	virtualExists := virtualPath != "" && vfsx.FileExists(b.SrcFS, virtualPath)

	if directData != nil {
		slog.Info("Found normal build config for", "dir", srcDir, "path", directPath)
		c.Require(!virtualExists, "Both '%s' and '%s' exist; a virtual build config must not collide with a normal one", directPath, virtualPath)
		return directData, directPath, false, false
	}

	c.Require(virtualPath != "", "No '%s' exists", directPath)
	c.Require(virtualExists, "No '%s' or '%s' exist", directPath, virtualPath)

	slog.Info("Found virtual build config for", "dir", srcDir, "path", virtualPath)
	// One Stat serves both probes — DirExistsFailOnFile panics on a non-directory internally.
	materialized = vfsx.DirExistsFailOnFile(b.SrcFS, srcDir)
	return vfsx.ReadSmallFileFast(b.SrcFS, virtualPath), virtualPath, true, materialized
}

func getPlatformScript(cfg BuildConfig.BufaConfig) string {
	c.Require(cfg.Cmd.Script != "", "%s has neither a root cmd nor a %s.cmd entry (use cmd = false for a scriptless dir)",
		Store.BuildConfigName, BuildConfig.PlatformSectionNames)
	return cfg.Cmd.Script
}

// Absolute path: dirty mode's cwd is in the source tree, and the build root may sit on another drive.
func (b *BuilderBase) writeBuildScript(cfg BuildConfig.BufaConfig, shell BuildConfig.ShellDef) string {
	buildScript := getPlatformScript(cfg)
	scriptFile := filepath.Join(Store.TmpRoot, Store.BuildConfigName+shell.Ext)
	c.Check(vfs.WriteFile(b.BldFS, scriptFile, []byte(buildScript), 0o755))
	return filepath.Join(b.BldRoot, scriptFile)
}

// One dir's script step: its script, an interactive shell in the script's place, or the script
// run by an interactive shell that stays — all with the same cwd and env.
func (b *BuilderBase) runScriptStep(srcDir, osDir string, env *Env, cfg BuildConfig.BufaConfig, shell BuildConfig.ShellDef) {
	mode := b.BuildModeFor(srcDir)
	script := ""
	if !cfg.IsScriptOptional() {
		script = b.writeBuildScript(cfg, shell)
	}
	if mode == Runtime.ModeBuild {
		c.Assert(script != "", "no script and no shell for '%s'", srcDir)
		cmd := scriptCommand(shell, script)
		cmd.Dir = osDir
		cmd.Env = env.Environ()
		slog.Info("Running build script", "dir", srcDir, "shell", shell.Name, "script", script)
		b.runScript(srcDir, cmd)
		// Success only: a failure keeps the script for debugging, as does a session below.
		c.Check(b.BldFS.RemoveAll(Store.TmpRoot))
	} else {
		b.runInteractiveShell(srcDir, osDir, env, script, mode, shell)
	}
}

// The script's exit status is not inspected: a post-script session is not a build.
func (b *BuilderBase) runInteractiveShell(srcDir, osDir string, env *Env, script string, mode Runtime.BuildMode, shell BuildConfig.ShellDef) {
	shellScript := ""
	if mode == Runtime.ModePostShell {
		shellScript = script
	}
	cmd := sessionCommand(shell, env, shellScript)
	cmd.Dir = osDir
	cmd.Stdin = b.In
	cmd.Stdout = b.Out
	cmd.Stderr = b.Out

	fmt.Fprintf(b.Out, "----- Shell Start: %s ('exit' to continue) -----\n", srcDir)
	fmt.Fprintf(b.Out, "Dir: %s\n", osDir)
	if script != "" {
		fmt.Fprintf(b.Out, "Script: %s\n", script)
	}
	fmt.Fprintln(b.Out)

	slog.Info("Running interactive shell", "dir", srcDir, "shell", shell.Name, "cwd", osDir, "script", shellScript)

	// Ctrl+C belongs to the shell for the session. Caught, not Ignored: an ignored disposition
	// is inherited across exec, a handler resets to default.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	err := cmd.Run()
	signal.Stop(interrupts)

	fmt.Fprintln(b.Out)
	fmt.Fprintf(b.Out, "-----   Shell End: %s -----\n", srcDir)
	// The shell's exit status is its last command's, not a build result.
	if _, exited := errors.AsType[*exec.ExitError](err); !exited {
		c.Checkf(err, "run interactive shell")
	}
}

// Both streams share one writer value, so os/exec serializes their writes.
func (b *BuilderBase) runScript(srcDir string, cmd *exec.Cmd) {
	var err error
	if b.ShowOutputFor(srcDir) {
		err = b.runScriptStreaming(srcDir, cmd)
	} else {
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err = cmd.Run()
		if err != nil {
			b.writeScriptOutput(srcDir, out.Bytes(), err)
		}
	}
	c.Checkf(err, "run %s", Store.BuildConfigName)
}

func (b *BuilderBase) runScriptStreaming(srcDir string, cmd *exec.Cmd) error {
	b.writeFrameHeader(srcDir)
	// os/exec unwraps an *os.File and hands the child the fd itself — no pipe, real console.
	cmd.Stdout = b.Out
	cmd.Stderr = b.Out
	err := cmd.Run()
	fmt.Fprintln(b.Out)
	b.writeFrameFooter(srcDir, err)
	return err
}

func (b *BuilderBase) writeScriptOutput(srcDir string, output []byte, err error) {
	b.writeFrameHeader(srcDir)
	b.Out.Write(output)
	if n := len(output); n > 0 && output[n-1] != '\n' {
		fmt.Fprintln(b.Out)
	}
	b.writeFrameFooter(srcDir, err)
}

func (b *BuilderBase) writeFrameHeader(srcDir string) {
	fmt.Fprintf(b.Out, "----- Build Start: %s -----\n", srcDir)
}

func (b *BuilderBase) writeFrameFooter(srcDir string, err error) {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		fmt.Fprintf(b.Out, "-----   Build End: %s -----\n", srcDir)
	case errors.As(err, &exitErr):
		fmt.Fprintf(b.Out, "----- Build FAILED (exit code %d): %s -----\n", exitErr.ExitCode(), srcDir)
	default:
		fmt.Fprintf(b.Out, "----- Build FAILED: %s -----\n", srcDir)
	}
}

func resolveRelDir(baseDir, dir string) string {
	var res string
	switch {
	case strings.HasPrefix(dir, "/"):
		res = filepath.Clean(dir[1:])
	case filepath.IsAbs(dir):
		c.Fail("dep '%s' must not be absolute", dir)
	default:
		res = filepath.Join(baseDir, dir)
	}
	c.Require(res == "." || !strings.HasPrefix(res, ".."), "dep '%s' escapes build root", dir)
	return res
}

// Runs after Bld/Src/Ext are resolved, so membership compares resolved paths. The ext `export = true`
// shortcut becomes a plain list entry first (a normalized Name is dir-relative dep grammar) and is false
// afterwards — the list is the only source of truth.
func resolveExportDeps(srcDir string, deps *BuildConfig.Deps) {
	for i := range deps.Ext {
		if deps.Ext[i].Export {
			deps.Export = append(deps.Export, deps.Ext[i].Name)
			deps.Ext[i].Export = false
		}
	}
	for i, dir := range deps.Export {
		res := resolveRelDir(srcDir, dir)
		isExt := slices.ContainsFunc(deps.Ext, func(dep BuildConfig.ExtDep) bool { return extDepPath(srcDir, dep) == res })
		if !isExt {
			c.Require(slices.Contains(deps.Bld, res) || slices.Contains(deps.Src, res),
				"deps.export '%s' in '%s' is not a deps.bld, deps.src, or deps.ext dependency", dir, srcDir)
			c.Require(Util.StrictlyBelow(srcDir, res),
				"deps.export '%s' in '%s' must be a dependency nested under the build dir", dir, srcDir)
		}
		deps.Export[i] = res
	}
}
