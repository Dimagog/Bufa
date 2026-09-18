# Runtime

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Type `Config` (anti-stutter vs stdlib `runtime`): invocation-wide state the CLI resolves once and threads through
  every `Build`; `Builder` embeds it. Fields: `SrcFS` (read-only in clean mode via the `readOnlyRealPathFs` wrapper,
  which forwards `RealPath` — Store and Hashing read source symlinks through `UnsafeIO.OSPath`, which rides it;
  `writableSrc` (dirty builds) gets the bare writable `BasePathFs`), `BldFS` (RW), `Out`, `In` (the interactive
  shell's stdin; nil outside sessions), `SrcRoot`, `BldRoot`, `NSAlive`, `DaemonDisabled`, `ShowOutput`,
  `ForceRebuild`, `BuildMode`, `TargetDirs`, `BuildStartTimeUTC`, `Daemon *DaemonClient.Client`, `RootConfig`.
- `Daemon` and `RootConfig` are populated **at Config creation**: `PrepareConfig`/`NewTest` call
  `DaemonClient.Connect(BldRoot, SrcRoot, DaemonDisabled, NSAlive)` then `getRootConfig()`. On the
  `PrepareConfigWithNoDaemon` paths (`gc`, `check`, `daemon stop`) `Daemon` stays nil and `RootConfig` zero: they
  don't build and talk to the daemon through `DaemonClient.Stop` directly.
- `getRootConfig()` resolves the `.BUFA` marker: `Daemon.GetRootConfig` first, `readRootConfig` disk parse on a miss
  (then `SetRootConfig` to prime). `readRootConfig` decodes from `DefaultRootConfig()` via `BuildConfig.DecodeStrict`
  (unknown keys and `{ file = … }` entries are hard errors; `afterDecode` restores document order) then `Validate`s
  (which rewrites each literal to its trimmed value, so the primed config is the normalized one). An **absent**
  `.BUFA` yields the defaults (absence ≡ empty file — `.git`-rooted and unanchored projects have no marker);
  non-default settings require creating `.BUFA`, which also anchors the project.
- `DirScope` (`ScopeNone`=default, `ScopeTarget`, `ScopeAll`) is the one per-dir policy type; `ShowOutput` and
  `ForceRebuild` each hold one, and both `*For(srcDir)` predicates are the shared `inScope` (`All` ⇒ every dir,
  `Target` ⇒ any member of `TargetDirs`). `ShowOutput` says whether a script's output streams live or is buffered
  and dumped on failure; `ForceRebuild` which dirs re-run their script past every build-result cache — Build owns
  what that means per mode. `BuildMode` (`ModeBuild`=default, `ModeShell`, `ModePostShell`) is what the target's
  script step does, **target dir only** (named `BuildMode`, not `Shell`, so it can't be read as *which* shell):
  `BuildModeFor(srcDir)` returns it for every `TargetDirs` member and `ModeBuild` elsewhere; `ShellSessionFor` is
  its `!= ModeBuild` bool; `ForceRebuildFor` is **also true** for a session target (a cached result would open no
  shell), so the shell flags imply `--force` without the CLI folding it. None of the per-dir policy fields nor `In`
  rides `Prepare*`: the CLI assigns them on the returned Config. `BuildResultDir(buildHash)` maps an output hash to
  its absolute `out/<hash>` dir.
- `PrepareConfig(noDaemon, restartDaemon, writableSrc, out)` = `prepareBaseConfig` + optional `DaemonClient.Restart`
  (`--restart-daemon`, **before** connecting) + connect + NS back-fill + `getRootConfig` (a daemon version mismatch
  restarts the daemon inside `DaemonClient.GetRootConfig`). The back-fill (when `!NSAlive`, `!DaemonDisabled`,
  and `rootAnchored` ⇒ async `NameServer.SetSrcRoot(startDir, SrcRoot)`): a dead NS at walk time meant the result had
  nowhere to go, but Connect's just-spawned daemon claims NS **before** its own sock listens — NS is up but empty,
  and priming it saves the next invocation's walk. `PrepareConfigWithNoDaemon(out)` = `prepareBaseConfig` only
  (daemon **enabled** so `Stop` reaches the sock, but not connected). `prepareBaseConfig` takes no dir: the walk
  starts at **cwd**, always (cmd/bufa's `--start-dir` is a real chdir; a build target never reaches Runtime). It
  prints `Src root: <srcRoot> (<kind>)` — `cached` (NS hit), `.BUFA`, `.git`, or `topmost BUFA, never cached` —
  builds the FS pair, sets `DaemonDisabled = noDaemon || $BUFA_NO_DAEMON != ""` (**the only place prod code reads
  `BUFA_NO_DAEMON`**), and does **not** MkdirAll bldRoot (deferred to whoever needs it, keeping the hot path
  FS-free). `NewTest(srcFS, bldFS, srcRoot, bldRoot, out, daemonDisabled)` is the test-only fs-injection seam.
- `defaultBldRoot`: **sibling** `<base>.BUFA` next to SrcRoot; `$BUFA_BUILD_ROOT` ⇒ `<env>/<base>.BUFA`. A SrcRoot at
  a drive/fs root (no sibling possible) is a **hard error demanding `$BUFA_BUILD_ROOT`** (then the dir under `<env>`
  is the bare `.BUFA`); a build root never lives inside the source tree.
- `findSrcRoot` queries `NameServer.GetSrcRoot` first (zero FS; a hit is trusted as **anchored** — only anchored
  resolutions are ever written back); on miss `walkForSrcRoot` makes **one upward walk** with marker precedence:
  nearest `.BUFA` marker (the walk stops there) > nearest `.git` entry (dir **or** file, `UnsafeIO.OSDirEntryExists`)
  > topmost `BUFA` (virtual configs don't count; the probe is the **silent** `UnsafeIO.OSFileExists`, not
  `Store.OSConfigExists` — the chain leaves the project, where a directory named `BUFA`, e.g. this very repo's, is
  legitimate). The first two **anchor** the root; only anchored results back-fill NS — nothing pins "topmost"
  against a marker later appearing above, so an unanchored root is re-walked every invocation. No marker ⇒ panic
  naming all three. Deliberately **no home/volume-root bounds**: a `BUFA` there roots there — marker presence is
  consent. **Split-roots tripwire**: each chain dir **strictly below** the resolved root is probed (one Lstat) for a
  sibling `<base>.BUFA` store dir (dirs above are outside the tree and may hold a live enclosing project's store); a
  store found is a **hard error** naming both roots with the delete-or-re-anchor hint, so a root flip arrives with
  its explanation instead of reading as a spontaneous full rebuild. NS hits skip the walk and both probes. Returns
  `(srcRoot, anchored, nsAlive)`. Imports `Store` + `NameServer`; nothing imports it back.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Watcher](../Watcher/CLAUDE.md) — `defaultBldRoot`'s **sibling** placement puts the store and the daemon's socket
  **outside** the watched tree; every watcher design choice that assumes it never sees store writes rests on that.
  `Watcher.Serve` claims the NameServer **before** `Daemon.Serve` listens, which is the only reason
  `PrepareConfig`'s post-`Connect` NS back-fill is both necessary and correct.
- [Build](../Build/CLAUDE.md) — `$BUFA_BUILD_ROOT` is read here by `defaultBldRoot` **and** set by Build into every
  script's env, so a nested `bufa` invoked from a script resolves its build root under the sandbox. `DirtyBuilder`
  writes the source tree through plain `SrcFS` ops — legal only for a `writableSrc=true` Config. Build folds
  `RootConfig.Env` into its `root.config` term **at builder construction**, so `RootConfig` being resolved inside
  `PrepareConfig`/`NewTest` (not lazily) is what makes the marker's `[env]` key every dir. Build bypasses its
  build-result/skip-hash tiers for a session only because `ForceRebuildFor` folds `ShellSessionFor` here; narrowing
  it back to `inScope(ForceRebuild)` makes `--shell` on a cached target silently open no shell.
- [DaemonClient](../DaemonClient/CLAUDE.md) — `Client` methods nil-check the **inner rpc handle**, not the receiver,
  while the `PrepareConfigWithNoDaemon` paths leave `Config.Daemon` nil: adding a daemon cache call to one of those
  paths panics instead of degrading. A version-mismatched daemon is restarted **inside** `GetRootConfig` by swapping
  the `Client`'s innards, so `Config.Daemon` stays valid and `getRootConfig`'s `Wrap0_Bool`-bound method values
  reach the fresh daemon.
- [NameServer](../NameServer/CLAUDE.md) — NS keys entries with `Util.NormalizePath` but stores and returns the
  caller's **raw** `SrcRoot` verbatim, and `findSrcRoot` uses it as `SrcRoot` and to derive `BldRoot` without
  re-checking the FS: the first client to `SetSrcRoot` fixes the casing/separator spelling every later invocation
  inherits. **Only anchored** roots are ever written back — every `SetSrcRoot` call site here gates on it — which is
  the entire basis for trusting a hit as anchored; one ungated write strands unanchored roots in a cache no event
  invalidates.
- [Store](../Store/CLAUDE.md) — every Store symlink op hard-requires its fs to resolve `RealPath`, and the OS copy
  engine needs the source fs to forward it. This package builds the FS pair, so wrapping either half in an afero
  decorator that hides `RealPath` panics inside Store on every link and OS copy.
- [Cache](../Cache/CLAUDE.md) — `getRootConfig` uses `Wrap0_Bool`, not `Wrap_Def`, because a zero `RootConfig` is a
  **legitimate** value (`ttl = 0` is a sentinel); zero-as-miss would make it a permanent miss.
- [BuildConfig](../BuildConfig/CLAUDE.md) — `[unsafe].ttl = 0` makes `BuildStartTimeUTC` a cache-key input that must
  stay fixed for the whole invocation.
- [internal/UnsafeIO](../internal/UnsafeIO/CLAUDE.md) — `UnsafeIO.OSPath` falls back to cwd-relative `filepath.Abs`
  when the fs lacks `GetRealPath`, so clean mode's RO `SrcFS` re-exposing `RealPath` is load-bearing: losing it
  makes every source symlink read resolve against the wrong root **silently**.
- [cmd/bufa](../cmd/bufa/CLAUDE.md) — `Config` is copied **by value** into the builder, so `TargetDirs`,
  `ShowOutput`, `ForceRebuild`, `BuildMode`, and `In` must be assigned before `NewBuilder`/`NewDirtyBuilder`; moving
  any later silently degrades those flags (a nil `In` hands the shell an EOF stdin). `BuildModeFor` returns the
  session mode for **every** `TargetDirs` member — cmd/bufa's exactly-one-dir guard on `--shell`/`--post-shell` is
  what keeps a session to one dir. `prepareBaseConfig` takes no dir on purpose: the `<dir>` grammar (cwd-relative,
  `/`-prefixed root-relative) lives in cmd/bufa alone; a dir parameter here re-splits it — on unix `/x` is also
  `IsAbs` — and re-roots `bufa test` at a nested marker under `test/`.
