package main

import "time"

type CLI struct {
	// ${log_levels} interpolates logging.LevelNames, so the legal values live in one place.
	LogLevel    string      `short:"l" help:"Log level." enum:"${log_levels}" default:"none" placeholder:"LEVEL"`
	SafeHashing bool        `help:"Don't trust symlinks pointing to hash-named targets, hash them by content."`
	Version     versionFlag `help:"Print the bufa version and exit."`
	StartDir    string      `aliases:"dir" placeholder:"DIR" help:"Start in DIR: 'bufa --dir=X T' is the same as 'cd X && bufa T' (alias --dir)."`

	Build  BuildCmd  `cmd:"" default:"withargs" aliases:"b" help:"Build each <dir> cleanly in isolation (default: current directory). Default command: 'bufa [<dir>...]' is shorthand for 'bufa build [<dir>...]'."`
	Dirty  DirtyCmd  `cmd:"" aliases:"d" help:"Build each <dir> directly in the source tree, no isolation (default: current directory)."`
	Gc     GcCmd     `cmd:"" help:"Garbage-collect the build store (removes orphaned cache entries)."`
	Check  CheckCmd  `cmd:"" help:"Verify build store integrity: re-hash every content dir and validate index links (stops daemon)."`
	Hash   HashCmd   `cmd:"" help:"Print the content hash of any file or directory."`
	Nuke   NukeCmd   `cmd:"" help:"*DANGER* Delete build dir and/or Global Artifact Cache (stops daemon), or empty cache dir (--cache-only). Refuses when the dir contains anything bufa did not put there."`
	Daemon DaemonCmd `cmd:"" help:"Watcher daemon control."`
}

// Like kong.VersionFlag, but printing to the invocation's out stream.
type versionFlag bool

// build/dirty only. Left nil when omitted — buildMain reads nil as cwd.
type dirArg struct {
	Dirs []string `arg:"" optional:"" help:"Target directories (default: current directory)."`
}

// options shared by the build and dirty subcommands only
type buildFlags struct {
	NoDaemon       bool `short:"d" help:"Do not use the watcher daemon for this build." group:"Daemon"`
	RestartDaemon  bool `short:"r" help:"Shut down the project's watcher daemon before building." group:"Daemon"`
	BuildOutput    bool `short:"o" help:"Show build script output for each dir given on the command line." group:"Output"`
	BuildOutputAll bool `short:"O" help:"Show build script output for every build dir (default: only on failure)." group:"Output"`
	Force          bool `short:"f" help:"Rebuild each dir given on the command line even if its cached result is current." group:"Force"`
	ForceAll       bool `short:"F" help:"Rebuild every build dir needed for the target dirs, cached or not." group:"Force"`
	Shell          bool `short:"s" help:"Open an interactive shell in the target dir's build environment instead of running its build script; nothing is built, published, or cached." group:"Shell" xor:"shell"`
	PostShell      bool `short:"S" help:"Run the target dir's build script in an interactive shell that stays open afterwards; build result is discarded, nothing is published or cached." group:"Shell" xor:"shell"`
}

type BuildCmd struct {
	dirArg
	buildFlags
}

type DirtyCmd struct {
	dirArg
	buildFlags
}

type GcCmd struct {
	NoSize bool `help:"Skip reporting freed disk space (faster for huge dirs)."`
}

type CheckCmd struct {
	Fix bool `short:"f" help:"Delete corrupt content dirs, the index links referencing them, bad index links, and unexpected entries."`
}

// Its own required path arg, not dirArg: any file OR directory, as a plain OS path.
type HashCmd struct {
	Path string `arg:"" help:"File or directory to hash."`
}

type NukeCmd struct {
	Global     bool `help:"Also delete the global artifact cache (shared by all projects)." xor:"scope"`
	GlobalOnly bool `help:"Delete only the global artifact cache, leave the build dir alone." xor:"scope"`
	CacheOnly  bool `help:"Delete only content of every cache dir under BUFA_CACHE_ROOT." xor:"scope"`
	Yes        bool `help:"Skip the confirmation prompt."`
}

type DaemonCmd struct {
	Stop  DaemonStopCmd  `cmd:"" help:"Stop the project's watcher daemon (if running) and the global name server; does not wait for the daemon to exit."`
	Reset DaemonResetCmd `cmd:"" help:"Restart the project's watcher daemon and the global name server with empty caches. Unlike stop, waits for the old daemon to exit: 'bufa daemon reset && bufa build' guarantees the build cannot use the stale (still stopping) daemon."`
}

type DaemonStopCmd struct{}

type DaemonResetCmd struct{}

// Parallel grammar for the internal watcher-serve mode; parsed by its own kong.New, so it never
// appears in the main help. No enum/default on LogLevel: "" keeps Configure's $LOG_LEVEL fallback.
type daemonCLI struct {
	LogLevel string        `short:"l" help:"Log level." placeholder:"LEVEL"`
	SockPath string        `arg:"" help:"Daemon socket path."`
	SrcDir   string        `arg:"" help:"Source root to watch."`
	Idle     time.Duration `arg:"" optional:"" default:"${daemon_idle}" help:"Idle shutdown timeout."`
}
