package Runtime

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Cache"
	"github.com/dimagog/bufa/DaemonClient"
	"github.com/dimagog/bufa/NameServer"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

// Which dirs a per-dir CLI policy (ShowOutput, ForceRebuild) applies to.
type DirScope int

const (
	// the default
	ScopeNone DirScope = iota
	ScopeTarget
	ScopeAll
)

// What the target dir's script step does: run the script, or open an interactive shell session.
type BuildMode int

const (
	// the default: run the script
	ModeBuild BuildMode = iota
	// an interactive shell replaces the script; a session publishes and records nothing
	ModeShell
	// the shell follows the script (whatever its exit status), then the build ends
	ModePostShell
)

type Config struct {
	// Read-only in clean mode (vfs.NewReadOnlyFs rejects writes at runtime); writable for dirty
	// builds, which write the source tree for real.
	SrcFS vfs.Fs
	BldFS vfs.Fs
	Out   io.Writer
	// stdin for the --shell/--post-shell sessions
	In io.Reader

	// Plain strings: vfs.NewReadOnlyFs hides the wrapped BasePathFs, so the roots cannot be
	// recovered through SrcFS.
	SrcRoot string
	BldRoot string

	// reused so the hot path doesn't re-dial the NameServer
	NSAlive bool

	// the only place prod code reads $BUFA_NO_DAEMON
	DaemonDisabled bool

	// ScopeNone ⇒ script output shown only on failure
	ShowOutput   DirScope
	ForceRebuild DirScope
	// applies to TargetDirs only
	BuildMode BuildMode

	TargetDirs []string

	// nil only on the non-building PrepareConfigWithNoDaemon paths
	Daemon *DaemonClient.Client

	// left zero on the non-building PrepareConfigWithNoDaemon paths
	RootConfig BuildConfig.RootConfig

	// captured at startup and used in place of time.Now().UTC()
	BuildStartTimeUTC time.Time

	// .BUFA- or .git-marked root; unanchored answers never enter the NameServer
	rootAnchored bool
}

func (rc Config) BuildResultDir(buildHash string) string {
	c.Require(buildHash != "", "build hash must not be empty")
	return filepath.Join(rc.BldRoot, Store.OutRoot, buildHash)
}

func (rc Config) inScope(scope DirScope, srcDir string) bool {
	switch scope {
	case ScopeAll:
		return true
	case ScopeTarget:
		return slices.Contains(rc.TargetDirs, srcDir)
	default:
		return false
	}
}

func (rc Config) ShowOutputFor(srcDir string) bool {
	return rc.inScope(rc.ShowOutput, srcDir)
}

// A shell session needs the target's cold path, so it is implicitly forced.
func (rc Config) ForceRebuildFor(srcDir string) bool {
	return rc.inScope(rc.ForceRebuild, srcDir) || rc.ShellSessionFor(srcDir)
}

func (rc Config) BuildModeFor(srcDir string) BuildMode {
	if rc.inScope(ScopeTarget, srcDir) {
		return rc.BuildMode
	}
	return ModeBuild
}

func (rc Config) ShellSessionFor(srcDir string) bool {
	return rc.BuildModeFor(srcDir) != ModeBuild
}

func readRootConfig(srcFS vfs.Fs) BuildConfig.RootConfig {
	rootConfig := BuildConfig.DefaultRootConfig()
	data := vfsx.TryReadSmallFileFast(srcFS, Store.SrcRootFileName)
	if data == nil {
		slog.Info("No " + Store.SrcRootFileName + ", using default RootConfig")
		return rootConfig
	}
	slog.Info("Read RootConfig from " + Store.SrcRootFileName)
	BuildConfig.DecodeStrict(data, Store.SrcRootFileName, &rootConfig)
	rootConfig.Validate()
	return rootConfig
}

func (rc Config) getRootConfig() BuildConfig.RootConfig {
	get := Cache.Wrap0_Bool(
		func() BuildConfig.RootConfig { return readRootConfig(rc.SrcFS) },
		rc.Daemon.GetRootConfig,
		rc.Daemon.SetRootConfig,
		"Daemon RootConfig", "no-value",
	)
	return get()
}

// ReadOnlyFs hides the wrapped BasePathFs's RealPath, which Store needs for OS-level copies.
type readOnlyRealPathFs struct {
	vfs.Fs
	UnsafeIO.GetRealPath
}

func PrepareConfig(noDaemon, restartDaemon, writableSrc bool, out io.Writer) Config {
	rc, startDir := prepareBaseConfig(noDaemon, writableSrc, out)
	if restartDaemon {
		DaemonClient.Restart(rc.BldRoot, rc.DaemonDisabled, out)
	}
	rc.Daemon = DaemonClient.Connect(rc.BldRoot, rc.SrcRoot, rc.DaemonDisabled, rc.NSAlive, out)
	// Connect's daemon claims NS before its own sock listens, so NS is up but freshly empty:
	// priming it here saves the next invocation's walk.
	if !rc.NSAlive && !rc.DaemonDisabled && rc.rootAnchored {
		NameServer.SetSrcRoot(startDir, rc.SrcRoot)
	}
	rc.RootConfig = rc.getRootConfig()
	return rc
}

// Daemon enabled (so Stop reaches the sock) but NOT connected.
func PrepareConfigWithNoDaemon(out io.Writer) Config {
	rc, _ := prepareBaseConfig(false /*noDaemon*/, false /*writableSrc*/, out)
	return rc
}

func NewTest(srcFS, bldFS vfs.Fs, srcRoot, bldRoot string, out io.Writer, daemonDisabled bool) Config {
	c.Require(out != nil, "out writer must not be nil")
	rc := Config{
		SrcFS:             srcFS,
		BldFS:             bldFS,
		Out:               out,
		SrcRoot:           srcRoot,
		BldRoot:           bldRoot,
		DaemonDisabled:    daemonDisabled,
		BuildStartTimeUTC: time.Now().UTC(),
	}
	rc.Daemon = DaemonClient.Connect(rc.BldRoot, rc.SrcRoot, rc.DaemonDisabled, rc.NSAlive, out)
	rc.RootConfig = rc.getRootConfig()
	return rc
}

func prepareBaseConfig(noDaemon, writableSrc bool, out io.Writer) (Config, string) {
	c.Require(out != nil, "out writer must not be nil")
	startDir := c.Check2(os.Getwd())
	daemonDisabled := noDaemon || os.Getenv("BUFA_NO_DAEMON") != ""

	srcRoot, anchored, nsAlive := findSrcRoot(startDir, daemonDisabled, out)
	bldRoot := defaultBldRoot(srcRoot)

	// bldRoot creation is deferred to whoever needs it: the fully-cached hot path never touches it,
	// so a MkdirAll here would be a wasted Stat per invocation.
	osFs := vfs.NewOsFs()
	srcFS := vfs.NewBasePathFs(osFs, srcRoot)
	// Dirty builds write the source tree for real, so they get the bare writable fs.
	if !writableSrc {
		srcRealPath, ok := srcFS.(UnsafeIO.GetRealPath)
		c.Require(ok, "src fs %T does not expose RealPath", srcFS)
		srcFS = readOnlyRealPathFs{Fs: vfs.NewReadOnlyFs(srcFS), GetRealPath: srcRealPath}
	}
	return Config{
		SrcFS:             srcFS,
		BldFS:             vfs.NewBasePathFs(osFs, bldRoot),
		Out:               out,
		SrcRoot:           srcRoot,
		BldRoot:           bldRoot,
		NSAlive:           nsAlive,
		DaemonDisabled:    daemonDisabled,
		BuildStartTimeUTC: time.Now().UTC(),
		rootAnchored:      anchored,
	}, startDir
}

func defaultBldRoot(srcRoot string) string {
	parent := filepath.Dir(srcRoot)
	name := ""
	if parent != srcRoot {
		name = filepath.Base(srcRoot)
	}
	name += Store.BuildRootDirSuffix
	if env := os.Getenv("BUFA_BUILD_ROOT"); env != "" {
		return filepath.Join(env, name)
	}
	c.Require(parent != srcRoot,
		"Source root '%s' is a filesystem root, so the '%s' build root cannot be its sibling; set $BUFA_BUILD_ROOT", srcRoot, name)
	return filepath.Join(parent, name)
}

func findSrcRoot(startDir string, daemonDisabled bool, out io.Writer) (string, bool, bool) {
	startDir = c.Check2(filepath.Abs(startDir))
	nsAlive := false
	if !daemonDisabled {
		var srcRoot string
		srcRoot, nsAlive = NameServer.GetSrcRoot(startDir)
		if srcRoot != "" {
			slog.Info("NameServer cache hit at startup", "startDir", startDir, "srcRoot", srcRoot)
			fmt.Fprintf(out, "Src root: %s (cached)\n", srcRoot)
			// Only anchored resolutions are ever written back, so a hit is anchored by construction.
			return srcRoot, true, nsAlive
		}
	}
	srcRoot, kind := walkForSrcRoot(startDir)
	slog.Info("Walked dirs to find srcRoot", "startDir", startDir, "srcRoot", srcRoot, "via", kind)
	anchored := kind != Store.BuildConfigName
	if anchored {
		fmt.Fprintf(out, "Src root: %s (%s)\n", srcRoot, kind)
	} else {
		fmt.Fprintf(out, "Src root: %s (topmost %s, never cached)\n", srcRoot, kind)
	}
	// Unanchored answers never enter the NameServer: nothing in the fs pins "topmost" against a
	// marker later appearing above it, so they are re-walked every invocation.
	if nsAlive && anchored {
		NameServer.SetSrcRoot(startDir, srcRoot) // prime cache for next time
	}
	return srcRoot, anchored, nsAlive
}

// One upward walk; marker precedence: nearest .BUFA marker (always wins, the walk stops there) >
// nearest .git entry > topmost BUFA. The first two anchor the root; a topmost-BUFA
// root is unanchored. Returns the winning marker name as kind.
func walkForSrcRoot(startDir string) (srcRoot, kind string) {
	var gitRepoRoot, topBufa string
	var chain []string
	// dir == prevDir only after the volume-root iteration — filepath.Dir is a fixed point there.
	for dir, prevDir := startDir, ""; dir != prevDir; dir, prevDir = filepath.Dir(dir), dir {
		chain = append(chain, dir)
		if Store.OSConfigExists(filepath.Join(dir, Store.SrcRootFileName)) {
			checkSplitRoots(chain, dir)
			return dir, Store.SrcRootFileName
		}
		// once gitRepoRoot is set, only the .BUFA marker can override it,
		// so the walk continues only to maybe find .BUFA
		if gitRepoRoot == "" {
			if UnsafeIO.OSDirEntryExists(filepath.Join(dir, Store.GitRootMarker)) {
				gitRepoRoot = dir
			} else if UnsafeIO.OSFileExists(filepath.Join(dir, Store.BuildConfigName)) {
				// Silent OSFileExists, NOT OSConfigExists: the chain leaves the project, where a
				// BUFA-named directory (e.g. this very repo's) is legitimate and simply not a marker.
				topBufa = dir // later (higher) hits overwrite: topmost wins
			}
		}
	}
	if gitRepoRoot != "" {
		srcRoot, kind = gitRepoRoot, Store.GitRootMarker
	} else {
		c.Require(topBufa != "", "Root markers %s, %s, or %s not found in '%s' or above",
			Store.SrcRootFileName, Store.GitRootMarker, Store.BuildConfigName, startDir)
		srcRoot, kind = topBufa, Store.BuildConfigName
	}
	checkSplitRoots(chain, srcRoot)
	return srcRoot, kind
}

// The tripwire that makes a root flip arrive with its explanation instead of reading as a
// spontaneous full rebuild.
func checkSplitRoots(chain []string, srcRoot string) {
	for _, dir := range chain {
		// Chain dirs above the resolved root are outside its tree: a store there cannot poison
		// hashing and may belong to a live enclosing project.
		if dir == srcRoot {
			break
		}
		buildStore := dir + Store.BuildRootDirSuffix // the sibling <base>.BUFA store; dir is clean, no Join needed
		info := UnsafeIO.OSTryLstat(buildStore)
		c.Require(info == nil || !info.IsDir(),
			"Build directory '%s' found below the current build root '%s' — may indicate a previous"+
				" build rooted at '%s'; delete the stale build directory, or re-anchor that root"+
				" with a '%s' source-root marker file",
			buildStore, srcRoot, dir, Store.SrcRootFileName)
	}
}
