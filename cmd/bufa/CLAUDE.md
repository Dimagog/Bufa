# cmd/bufa

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- The one user-visible binary; a **kong** (`github.com/alecthomas/kong`) struct grammar (`CLI`, types-only in
  `cli.go`; dispatch in `main.go`): `bufa (build | b) [<dir>...]`, `bufa (dirty | d) [<dir>...]`, `bufa gc`,
  `bufa check`, `bufa hash <path>`, `bufa nuke`, `bufa daemon (stop | reset)`. Only build/dirty take `<dir>...`
  (the shared `dirArg`, an `optional:""` slice with no default — nil, which `buildMain` turns into the one target
  `""` = cwd); every other command works on the project **cwd** lies in, `--start-dir` naming another.
- **Targets**: each build `<dir>` is cwd-relative, or **root-relative** when `/`-prefixed (`bufa /test` =
  `<srcRoot>/test`), always inside the root **cwd** resolves to (`bufa test` and `bufa /test` find the same root
  even when `test/` carries its own marker; `resolveVirtualDirAgainstOSPaths` rejects a target outside SrcRoot). An
  absolute `<dir>` is never auto-detected — on unix `/x` is both root-relative and absolute — so a volume-carrying
  one hard-fails pointing at `--start-dir`. Runtime takes no dir at all. **Several targets** build **sequentially in
  command-line order on one builder**, so a dep shared by two targets builds once; each prints its own
  `Build result:` line, the first failure stops the rest, a duplicate target is a local-cache hit. Flags go before
  or after the dir list; `bufa a -f b` is a parse error (kong fills a slice positional from consecutive tokens
  only). build is the **default command** (`default:"withargs"`); command names win over dir names **as the first
  token only** (`bufa a gc` builds `a` and `gc`; escape via `bufa ./gc`).
- **Flags are exact per subcommand** (kong rejects a flag the command's struct doesn't carry). build/dirty embed
  `buildFlags`: `--no-daemon` (`-d`), `--restart-daemon` (`-r`); `--build-output-all` (`-O`) / `--build-output`
  (`-o`); `--force` (`-f`) / `--force-all` (`-F`) → `Runtime.ScopeTarget`/`ScopeAll` (both pairs fold through one
  `dirScope(all, target)`, `-all` wins); `--shell` (`-s`) / `--post-shell` (`-S`) (kong `xor`, → `ModeShell`/
  `ModePostShell`, **exactly one** target — `buildMain` hard-fails a session flag with more than one `<dir>` before
  root resolution, since Runtime would open a session for every target; a session is never a build, so no
  `Build result:` line; both imply `--force` for the target via `Runtime.ForceRebuildFor`; `buildMain` assigns
  `rc.BuildMode` and `rc.In` = the `runContext`'s stdin alongside the other per-dir policies). check: `--fix`
  (`-f`), `--no-global`/`--global-only` (kong `xor`).
  nuke: `--global`/`--global-only`/`--cache-only` (kong `xor`) + `--yes`. gc: `--no-size`. Root-level, global to
  every subcommand: `--log-level` (`-l`; default none ⇒ silent success), `--safe-hashing` (long-only — `-s` is
  `--shell`; sets `Hashing.SafeHashing`; check forces it on, and `check -s` prints a redundancy note — `checkMain`
  reads the global as already-true before its own force-set), `--start-dir=DIR` (alias `--dir`, parsed but unlisted
  in help; a real `os.Chdir` in `run` after parsing, before dispatch, so `bufa --dir=X <cmd>` ≡ `cd X && bufa <cmd>`
  with zero per-command plumbing), `--version` (prints `bufa version <Bufa.Version>` to **out** and exits 0: own
  `versionFlag` mirroring `kong.VersionFlag`'s `BeforeReset` hook, so it works alone; the `runContext` is bound at
  `kong.New` so parse-time hooks see it). The whole `kong.New` option set lives in
  `newParser(cli, in, out, errOut)`, which `cli_test.go`'s `parseCLI` calls too, so grammar tests parse with the
  production grammar. Dispatch = kong Run-method convention: each command struct's `Run(*runEnv)` calls its
  `*Main`. Help exits 0 — kong calls `Exit(0)` after printing, so the `kong.Exit` override panics the
  `errCleanExit` sentinel which `parseArgs` recovers (the only Exit paths are help and `--version`; parse errors
  are returned). Bad input prints the command's
  usage summary then propagates the error (exit 1). Help/usage go to errOut.
- The internal watcher-serve mode stays **outside** the main grammar: raw `args[0] == "--daemon"` dispatches into
  the parallel `daemonCLI` grammar (`--daemon <sockPath> <srcDir> [<idle>]` + `--log-level`, no enum/default there
  so `""` keeps `logging.Configure`'s `$LOG_LEVEL` fallback), so it never appears in help; `Daemon.Connect`'s
  self-spawn via `os.Executable()` lands here; default idle **1h** (`defaultDaemonIdleDuration`, fed to the tag via
  the `${daemon_idle}` kong var).
- `gc` — `PrepareConfigWithNoDaemon`, `DaemonClient.Stop` first (its caches reference store content), then
  `Store.GC` with `Store.BuildDirPresent` wrapped in `Cache.Memoize` (all four roots decode the same `∕<path>`
  names; safe because gc never mutates the source tree); reports each non-zero `GCStats` counter, then — unless
  `--no-size` — an indented `    freed <n>` line in `humanize.IBytes` form; a squeaky-clean run prints no freed
  line. `check` — `PrepareConfigWithNoDaemon` + `DaemonClient.Stop`, then
  `Store.Check(out, fix, DaemonClient.SockName)` — the same sock-only allowlist `nuke` hands `Store.Nuke` — then
  `ArtifactCache.New(GetCacheDir()).Check(out, fix)`: two independent halves, store first, each printing its
  problems plus its own summary line (`Check: …` / `Check Global Artifact Cache '<dir>': …`, problem counts folded
  by the shared `checkProblems`). The cache half is **on by default**; `--no-global` drops it, `--global-only`
  drops the store half — root resolution and the daemon stop with it, so it works outside a project. Exits 1 on any
  problem, the error naming the failed half(s) (`store` / `Global Artifact Cache`), after **both** ran. `--fix`
  deletes everything check reports in either half, so a fix run cannot fail on findings, only on a removal error;
  `checkMain` requires `fix || len(failed) == 0`. `hash` — `Hashing.Hash`
  of a plain OS path (own kong arg, not `dirArg`; no project root, store, or daemon). `nuke` — stop the daemon and
  delete the build root (`--global`: plus the artifact cache; `--global-only`: only the cache, skipping root
  resolution so it works outside a project; `--cache-only`: the target becomes `<bldRoot>/user`, emptied via
  `Store.EmptyCacheDirs` keeping entries
  and links, **no daemon stop** since the daemon caches nothing about `user/`). Three independent targets, one flow:
  resolve + existence-probe (`vfsx.DirEntryExists` through the fs handles, no OS-path probes; absent ⇒ reported and
  dropped; none left ⇒ "Nothing to nuke"), one combined y/N `confirm` naming every target (skipped by `--yes`;
  reads `runContext.in`; EOF/anything but y/yes ⇒ "Aborted"), then `DaemonClient.StopWait(bldRoot, false, out)` —
  disabled=false so the sock is reached even under `$BUFA_NO_DAEMON`, and the **wait** matters because the sock's
  dir is deleted next — then `Store.Nuke` with `DaemonClient.SockName`, `EmptyCacheDirs(out)` (reports each foreign
  `user/` name it left untouched, then "Emptied N cache dirs"), and `ArtifactCache.New(GetCacheDir()).Nuke()`. NS is
  **not** stopped: it is global and holds only cwd→srcRoot entries the deletion doesn't invalidate. `daemon stop` —
  `NameServer.Stop` **first** (before root resolution, so it works outside a project; before `DaemonClient.Stop`,
  whose dying daemon may host NS), then `PrepareConfigWithNoDaemon` + `DaemonClient.Stop` (fire-and-forget).
  `daemon reset` — `NameServer.Stop` first (so the fresh daemon's startup claim finds the socket free), then
  `PrepareConfig(restartDaemon=true)` is exactly the flow (stop + wait + spawn + re-prime RootConfig); unlike `stop`
  it waits, so `bufa daemon reset && bufa build` cannot reach the stale daemon. `dirty` — `Build.NewDirtyBuilder`;
  `buildMain` passes `dirty` as `PrepareConfig`'s `writableSrc`; a daemon-**disabled** dirty build first
  `DaemonClient.Stop`s any running daemon, since it writes `dirty/` skip hashes the watcher can't see and a stale
  shadow could wrongly skip a later build; prints the source dir as the build result. Sock: `bufa-d.sock` under the
  build root.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Daemon](../../Daemon/CLAUDE.md) — the self-spawn vector is assembled across **three** packages:
  `DaemonClient.Connect` passes srcRoot as the sole extra arg, `Daemon.Connect` prepends `--daemon <sockPath>` and
  execs `os.Executable()`, and the raw `args[0] == "--daemon"` dispatch here is where it lands. Neither the token,
  its position, nor the argument count is visible from `Connect(sockPath, daemonArgs...)`.
- [Watcher](../../Watcher/CLAUDE.md) — `Watcher.Serve(sockPath, srcDir, idleTimeout)` is reachable **only** through
  that positional argv, and the production **1h** idle default lives here (Daemon's reference binary: 5s).
- [internal/contract](../../internal/contract/CLAUDE.md) — `PanicToError` re-panics any recovered value that is not
  nil, an `error`, or a `string`, which is why `errCleanExit` must be an error **value**; a custom struct or int
  sentinel turns `--help` into an uncaught panic.
- [internal/logging](../../internal/logging/CLAUDE.md) — `enum:"${log_levels}"` interpolates `logging.LevelNames`
  verbatim, so that const's spelling and order are `--log-level`'s legal-values contract.
- [NameServer](../../NameServer/CLAUDE.md) — `daemon stop`/`reset` call `NameServer.Stop` **before**
  `DaemonClient.Stop`/`PrepareConfig(restartDaemon=true)`: stop's order avoids racing the Shutdown RPC against a
  dying NS-owning daemon, reset's frees the socket for the fresh daemon's claim. Reordering usually works but leaves
  NS randomly alive or down-until-takeover.
- [Build](../../Build/CLAUDE.md) — `nuke` and `check` allowlist only `DaemonClient.SockName` at the build root's top
  level because Build keeps its script inside `tmp/`; Build writing any top-level build-root file makes a
  post-failure `nuke` refuse and `check` report it, with no test here catching it.
- [Runtime](../../Runtime/CLAUDE.md) — `Runtime.Config` is copied **by value** into the builder, so `rc.TargetDirs`
  (in the srcRoot-relative key space `resolveVirtualDirAgainstOSPaths` produces), `rc.ShowOutput`, `rc.ForceRebuild`,
  `rc.BuildMode`, and `rc.In` must be assigned before `NewBuilder`/`NewDirtyBuilder`; moving any later silently
  degrades those flags. Runtime's `BuildModeFor` returns the session mode for **every** `TargetDirs` member —
  `buildMain`'s exactly-one-dir guard is the only thing keeping a session to one dir. The `<dir>` grammar lives
  **here only**: Runtime's `prepareBaseConfig` takes no dir (the walk starts at cwd, which `--start-dir`'s chdir has
  set); giving it one re-roots `bufa test` at a nested marker under `test/` and on unix makes `/x` an absolute walk
  start.
