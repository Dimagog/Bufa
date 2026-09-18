// Command bufa is the "Build for Adults" build-system CLI.
//
//	bufa [<dir>...]               # shorthand for "bufa build [<dir>...]"
//	bufa (build | b) [<dir>...]   # build directories in isolation
//	bufa (dirty | d) [<dir>...]   # build in place in the source tree
//	bufa gc [<dir>]            # garbage-collect the build store
//	bufa check [<dir>]         # verify build store integrity
//	bufa hash <path>           # print the content hash of a file or directory
//	bufa nuke [<dir>]          # stop the watcher daemon and delete the entire build dir
//	bufa daemon stop [<dir>]   # stop the watcher daemon and the global name server
//	bufa daemon reset [<dir>]  # restart the watcher daemon and name server with empty caches
//
// The internal watcher-serve mode stays outside the main kong grammar: Daemon.Connect self-spawns
// "bufa --daemon <sockPath> <srcDir> [<idle>] [--log-level LEVEL]", dispatched on the raw args to
// the parallel daemonCLI grammar, so it never appears in help.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/dustin/go-humanize"
	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa"
	"github.com/dimagog/bufa/ArtifactCache"
	"github.com/dimagog/bufa/Build"
	"github.com/dimagog/bufa/Cache"
	"github.com/dimagog/bufa/DaemonClient"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/NameServer"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/Watcher"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/logging"
	"github.com/dimagog/bufa/internal/runmain"
	"github.com/dimagog/bufa/internal/vfsx"
)

const defaultDaemonIdleDuration = 1 * time.Hour

// The -all flag is a superset of its target-only sibling, so it wins when both are given.
func dirScope(all, target bool) Runtime.DirScope {
	switch {
	case all:
		return Runtime.ScopeAll
	case target:
		return Runtime.ScopeTarget
	}
	return Runtime.ScopeNone
}

func (f buildFlags) showOutput() Runtime.DirScope {
	return dirScope(f.BuildOutputAll, f.BuildOutput)
}

func (f buildFlags) forceRebuild() Runtime.DirScope {
	return dirScope(f.ForceAll, f.Force)
}

func (f buildFlags) buildMode() Runtime.BuildMode {
	mode := Runtime.ModeBuild
	if f.Shell {
		mode = Runtime.ModeShell
	} else if f.PostShell {
		mode = Runtime.ModePostShell
	}
	return mode
}

type runContext struct {
	in  io.Reader
	out io.Writer
}

func (c *BuildCmd) Run(rc *runContext) error {
	buildMain(rc.out, rc.in, c.Dirs, c.buildFlags, false /*dirty*/)
	return nil
}

func (c *DirtyCmd) Run(rc *runContext) error {
	buildMain(rc.out, rc.in, c.Dirs, c.buildFlags, true /*dirty*/)
	return nil
}

func (c *GcCmd) Run(rc *runContext) error {
	gcMain(rc.out, !c.NoSize)
	return nil
}

func (c *CheckCmd) Run(rc *runContext) error {
	checkMain(rc.out, c.Fix)
	return nil
}

func (c *HashCmd) Run(rc *runContext) error {
	hashMain(rc.out, c.Path)
	return nil
}

func (c *NukeCmd) Run(rc *runContext) error {
	nukeMain(rc.out, rc.in, c)
	return nil
}

func (c *DaemonStopCmd) Run(rc *runContext) error {
	stopDaemonMain(rc.out)
	return nil
}

func (c *DaemonResetCmd) Run(rc *runContext) error {
	resetDaemonMain(rc.out)
	return nil
}

// The same hook point kong.VersionFlag uses, so --version works even when the rest of the command
// line would not parse.
func (versionFlag) BeforeReset(app *kong.Kong, rc *runContext) error {
	fmt.Fprintln(rc.out, "bufa version", Bufa.Version)
	app.Exit(0)
	return nil
}

func main() {
	runmain.Run("bufa", run)
}

func helpFormatter(value *kong.Value) string {
	help := kong.DefaultHelpValueFormatter(value)
	if value.Enum != "" {
		help += " One of: " + value.Enum + "."
	}
	if value.HasDefault {
		help += " Default: '" + value.Default + "'."
	}
	return help
}

// kong calls Exit only right after printing help or --version, so catching this sentinel turns
// those into a clean success. An error, not a bare type, so c.Rescue converts it.
var errCleanExit = errors.New("help or version already shown")

func run(args []string, in io.Reader, out, errOut io.Writer) (err error) {
	defer c.Catch(&err)

	// Configure logging before parsing so a parse error's contract log obeys the level.
	logging.Configure("", errOut)

	if len(args) > 0 && args[0] == "--daemon" {
		daemonMain(args[1:], errOut)
		return nil
	}

	var cli CLI
	ctx, done := parseArgs(newParser(&cli, in, out, errOut), args)
	if done {
		return nil
	}

	logging.Configure(cli.LogLevel, errOut)
	Hashing.SafeHashing = cli.SafeHashing
	if cli.StartDir != "" {
		c.Checkf(os.Chdir(cli.StartDir), "--start-dir '%s'", cli.StartDir)
	}
	c.Check(ctx.Run())
	return nil
}

func newParser(cli *CLI, in io.Reader, out, errOut io.Writer) *kong.Kong {
	return c.Check2(kong.New(cli,
		kong.Name("bufa"),
		kong.Description("BUFA - Build For Adults "+Bufa.Version),
		kong.Writers(errOut, errOut),
		kong.Exit(func(int) { c.Error(errCleanExit) }),
		kong.Vars{"log_levels": logging.LevelNames},
		kong.ValueFormatter(helpFormatter),
		kong.ConfigureHelp(kong.HelpOptions{
			FlagsLast: true,
			Compact:   true,
		}),
		// Bound at parser level, not ctx.Run, so parse-time hooks see it too.
		kong.Bind(&runContext{in: in, out: out}),
	))
}

func parseArgs(parser *kong.Kong, args []string) (ctx *kong.Context, done bool) {
	err := c.Rescue(func() {
		var parseErr error
		ctx, parseErr = parser.Parse(args)
		if pe, ok := errors.AsType[*kong.ParseError](parseErr); ok {
			// Best-effort context for the user; the real failure is parseErr below.
			_ = pe.Context.PrintUsage(true /*summary*/)
		}
		c.Check(parseErr)
	})
	if errors.Is(err, errCleanExit) {
		return nil, true
	}
	c.Check(err)
	return ctx, false
}

func daemonMain(args []string, errOut io.Writer) {
	var cli daemonCLI
	parser := c.Check2(kong.New(&cli,
		kong.Name("bufa --daemon"),
		kong.Description("bufa watcher daemon (internal)"),
		kong.Writers(errOut, errOut),
		kong.Exit(func(int) { c.Error(errCleanExit) }),
		kong.Vars{"daemon_idle": defaultDaemonIdleDuration.String()},
		kong.ValueFormatter(helpFormatter),
	))
	if _, done := parseArgs(parser, args); done {
		return
	}
	logging.Configure(cli.LogLevel, errOut)
	Watcher.Serve(cli.SockPath, cli.SrcDir, cli.Idle)
}

func gcMain(out io.Writer, reportFreedSpace bool) {
	// The daemon is stopped first: its caches reference store content GC may delete.
	rc := Runtime.PrepareConfigWithNoDaemon(out)
	DaemonClient.Stop(rc.BldRoot, rc.DaemonDisabled, out)
	store := Store.NewStore(rc.BldFS)
	// Memoized: every root decodes the same ∕<path> names, and gc never mutates the source tree.
	st := store.GC(Cache.Memoize(func(path string) bool {
		return Store.BuildDirPresent(rc.SrcFS, path)
	}), reportFreedSpace)
	var removed []string
	countIf := func(n int, what string) {
		if n > 0 {
			removed = append(removed, fmt.Sprintf("%d %s", n, what))
		}
	}
	countIf(st.StalePathLinks, "stale path links")
	countIf(st.OrphanContent, "orphan content dirs")
	countIf(st.OrphanBuildLinks, "orphan build links")
	countIf(st.StaleDirtySkipHashes, "stale dirty skip hashes")
	countIf(st.OrphanCacheDirs, "orphan cache dirs")
	countIf(st.LeftoverScratchDirs, "leftover scratch dirs")
	if len(removed) == 0 {
		fmt.Fprintln(out, "GC: already squeaky clean.")
	} else {
		fmt.Fprintf(out, "GC: removed %s\n", strings.Join(removed, ", "))
		if reportFreedSpace {
			fmt.Fprintf(out, "    freed %s\n", humanize.IBytes(st.BytesFreed))
		}
	}
}

func checkMain(out io.Writer, fix bool) {
	// Already true here means the user passed --safe-hashing; must test before the force-set below.
	if Hashing.SafeHashing {
		fmt.Fprintln(out, "Note: check command always hashes safely, so --safe-hashing flag is redundant")
	}
	// Verification must never trust hash-named link targets
	Hashing.SafeHashing = true
	rc := Runtime.PrepareConfigWithNoDaemon(out)
	DaemonClient.Stop(rc.BldRoot, rc.DaemonDisabled, out)
	st := Store.NewStore(rc.BldFS).Check(out, fix, DaemonClient.SockName)
	var problems []string
	countIf := func(n int, what string) {
		if n > 0 {
			problems = append(problems, fmt.Sprintf("%d %s", n, what))
		}
	}
	countIf(st.CorruptContent, "corrupt")
	countIf(st.BadLinks, "bad links")
	countIf(st.UnexpectedEntries, "unexpected entries")
	countIf(st.RemovedEntries, "entries removed")
	suffix := ""
	if len(problems) > 0 {
		suffix = "; " + strings.Join(problems, ", ")
	}
	fmt.Fprintf(out, "Check: %d content dirs hashed, %d links verified%s\n", st.CheckedContent, st.CheckedLinks, suffix)
	// fix removes every reported problem (a failed removal panics), so only a plain check can fail.
	c.Require(fix || st.Ok(), "store check failed")
}

func hashMain(out io.Writer, path string) {
	fmt.Fprintln(out, Hashing.Hash(vfs.NewOsFs(), path))
}

func nukeMain(out io.Writer, in io.Reader, cmd *NukeCmd) {
	bldRoot := ""
	userRoot := ""
	var userStore *Store.Store // --cache-only: set iff user/ exists; targets userRoot instead of bldRoot
	if !cmd.GlobalOnly {
		rc := Runtime.PrepareConfigWithNoDaemon(out)
		if cmd.CacheOnly {
			userRoot = filepath.Join(rc.BldRoot, Store.UserRoot)
			if vfsx.DirEntryExists(rc.BldFS, Store.UserRoot) {
				userStore = Store.NewStore(rc.BldFS)
			} else {
				fmt.Fprintf(out, "Cache dir root '%s' does not exist\n", userRoot)
			}
		} else if vfsx.DirEntryExists(rc.BldFS, ".") {
			bldRoot = rc.BldRoot
		} else {
			fmt.Fprintf(out, "Build dir '%s' does not exist\n", rc.BldRoot)
		}
	}
	var cache *ArtifactCache.Cache
	if cmd.Global || cmd.GlobalOnly {
		ac := ArtifactCache.New(ArtifactCache.GetCacheDir())
		if vfsx.DirEntryExists(ac.Fs(), ".") {
			cache = ac
		} else {
			fmt.Fprintf(out, "Global Artifact Cache '%s' does not exist\n", ac.Dir())
		}
	}

	var targets []string
	if bldRoot != "" {
		targets = append(targets, fmt.Sprintf("* '%s' - build dir", bldRoot))
	}
	if userStore != nil {
		targets = append(targets, fmt.Sprintf("* '%s' - contents of every cache dir", userRoot))
	}
	if cache != nil {
		targets = append(targets, fmt.Sprintf("* '%s' - Global Artifact Cache", cache.Dir()))
	}
	if len(targets) == 0 {
		fmt.Fprintln(out, "Nothing to nuke")
		return
	}
	if !cmd.Yes && !confirm(in, out, "\nNUKE the following dirs:\n\n"+strings.Join(targets, "\n")+"\n\nAre you sure?") {
		fmt.Fprintln(out, "Aborted")
		return
	}

	if bldRoot != "" {
		// daemonDisabled=false so Stop reaches the sock even under $BUFA_NO_DAEMON — the sock's
		// dir is about to be deleted, so a running daemon must die regardless.
		DaemonClient.StopWait(bldRoot, false /*daemonDisabled*/, out)
		Store.Nuke(bldRoot, DaemonClient.SockName)
		fmt.Fprintf(out, "Nuked build dir '%s'\n", bldRoot)
	}
	if userStore != nil {
		// No daemon stop: it caches nothing about user/, and the build root survives.
		n := userStore.EmptyCacheDirs(out)
		fmt.Fprintf(out, "Emptied %d cache dirs under '%s'\n", n, userRoot)
	}
	if cache != nil {
		cache.Nuke()
		fmt.Fprintf(out, "Nuked Global Artifact Cache '%s'\n", cache.Dir())
	}
}

func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func stopDaemonMain(out io.Writer) {
	// NS first — before root resolution, so it stops even outside a project (matching reset), and
	// before DaemonClient.Stop, whose dying daemon may host it.
	NameServer.Stop(out)
	rc := Runtime.PrepareConfigWithNoDaemon(out)
	DaemonClient.Stop(rc.BldRoot, rc.DaemonDisabled, out)
}

// Unlike stop (fire-and-forget), reset waits for the old daemon to exit, so
// `bufa daemon reset && bufa build` cannot reach the stale one.
func resetDaemonMain(out io.Writer) {
	// Before PrepareConfig, so the fresh daemon's NS claim finds the global socket free.
	NameServer.Stop(out)
	// DaemonClient.Restart reports "Daemon restarted"; no message here.
	Runtime.PrepareConfig(false /*noDaemon*/, true /*restartDaemon*/, false /*writableSrc*/, out)
}

func buildMain(out io.Writer, in io.Reader, dirs []string, f buildFlags, dirty bool) {
	if len(dirs) == 0 {
		dirs = []string{""} // cwd
	}
	for _, dir := range dirs {
		c.Require(filepath.VolumeName(dir) == "", "'%s' is an absolute OS path; pass it as --start-dir", dir)
	}
	c.Require(len(dirs) == 1 || f.buildMode() == Runtime.ModeBuild,
		"--shell/--post-shell take exactly one target dir, got %d", len(dirs))
	rc := Runtime.PrepareConfig(f.NoDaemon, f.RestartDaemon, dirty /*writableSrc*/, out)
	srcDirs := make([]string, len(dirs))
	for i, dir := range dirs {
		srcDirs[i] = resolveVirtualDirAgainstOSPaths(rc.SrcRoot, dir)
	}
	rc.TargetDirs = srcDirs // the dirs named on the command line (for --build-output, --force, and the shell flags)
	rc.ShowOutput = f.showOutput()
	rc.ForceRebuild = f.forceRebuild()
	rc.BuildMode = f.buildMode()
	rc.In = in
	// One builder for every target: its local caches are what make a shared dep build once.
	var b Build.IBuild
	if dirty {
		// A daemon-disabled dirty build writes dirty/ skip hashes a live daemon cannot observe, so a
		// stale shadow that MATCHED would wrongly skip a later build. daemonDisabled=false so Stop
		// reaches the sock this invocation itself won't use.
		if rc.DaemonDisabled {
			DaemonClient.Stop(rc.BldRoot, false, out)
		}
		b = Build.NewDirtyBuilder(rc)
	} else {
		b = Build.NewBuilder(rc)
	}
	for _, srcDir := range srcDirs {
		// Empty only after a shell session: it builds nothing, so there is no result to report.
		if buildHash := b.Build(srcDir); buildHash != "" {
			var result string
			if dirty {
				result = filepath.Join(rc.SrcRoot, srcDir)
			} else {
				result = rc.BuildResultDir(buildHash)
			}
			fmt.Fprintln(out, "Build result:", result)
		}
	}
}

// dir resolves against cwd unless "/"-prefixed, which resolves against the source root.
func resolveVirtualDirAgainstOSPaths(srcAbsRoot, dir string) string {
	var p string
	if strings.HasPrefix(dir, "/") {
		p = filepath.Join(srcAbsRoot, dir[1:])
	} else {
		p = filepath.Join(c.Check2(os.Getwd()), dir)
	}
	rel := c.Check2(filepath.Rel(srcAbsRoot, p))
	c.Require(!strings.HasPrefix(rel, ".."), "'%s' is outside the source root", dir)
	return rel
}
