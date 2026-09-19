# CLAUDE.md

AI coding agent guidance this repo.

Repo-wide conventions live here. **Per-package detail lives in that package's own `CLAUDE.md`** — see the index under
"## Packages".

## Keep these files up-to date

* When adding a new package — create `<Package>/CLAUDE.md` and add a line to the index under "## Packages".
* When making significant changes to an existing package - update that package's `CLAUDE.md` to reflect changes.
* Only repo-wide facts belong in this file; package internals belong in the package's own file.
* An index hook is a **routing signal**, not a summary: it must let a reader holding only this file decide whether the
  answer to their question is in that package's file. Name the decisions/invariants/formats documented there, not just
  the package's job.
* A fact that constrains **two** packages goes in **both** files' "## Cross-package contracts" section, worded from each
  side. One-sided is how such a fact goes missing — the other side's reader never sees it.
* Keep lines wrapped at max 120 columns (wrap at spaces, never inside backtick spans; continuation lines indented under
  bullet content).

## Versioning

* After making significant changes, bump the version in root `version.txt` — unless it's already bumped in this
  change-set (check `git diff` / `git status` for `version.txt`). **Minor** for a new feature or changed semantics
  (reset patch to `0`); **patch** is reserved for bug and security fixes. Exactly one semver line, **no trailing
  newline** (embedded untrimmed; `TestVersionFormat` enforces).
* A version bump also flushes daemon state: the new binary's version check restarts a running daemon in place, so
  daemon-cached values (configs, hashes) never go stale across an upgrade — no manual `bufa daemon reset` needed.
  A **minor** bump also re-keys every build dir: the `x.y` of `Version` is folded into `RootConfig.GetHash()` (see
  [BuildConfig](BuildConfig/CLAUDE.md)), Build's `root.config` cache-key term, so a minor upgrade is a total
  rebuild — semantic changes with no config edit never serve a stale store link. A patch bump is not: a fix that
  alters what a build produces must bump the minor.

## Don't

- Don't rename PascalCase paths/packages to lowercase.
- Don't modify TODO file, user manages it.
- Don't bring up that symlinks require Windows Developer Mode — in specs, docs, brainstorms, or discussion. It is
  known and was a conscious design decision; symlink use never needs to be justified or flagged on that ground.

## Project

`bufa` — "Build for Adults" build system

## Layout conventions (non-idiomatic — intentional)

- Library dirs + source files use **PascalCase**: `FilterFiles/FilterFiles.go`, `FilterFiles/FilterFiles_test.go`.
  Package decl `package FilterFiles`. Violates Go all-lowercase-package convention, may trip `go vet`/`golint` — owner
  preferred style. No "fix".
- CLI dirs under `cmd/` use kebab-case (`cmd/filter-files/`), lowercase `main.go`/`main_test.go`. Standard for binaries.
- Imports use PascalCase path: `"github.com/dimagog/bufa/FilterFiles"`.

## Coding conventions

- All prod-code file I/O goes through an injected `vfs.Fs`, an [internal/vfsx](internal/vfsx/CLAUDE.md) helper, or an
  [internal/UnsafeIO](internal/UnsafeIO/CLAUDE.md) escape hatch — vfsx and UnsafeIO are the only packages allowed to
  call `os.*` file APIs, and UnsafeIO is the only one translating vfs paths to OS paths or probing raw OS paths.
  Excluded by design: UDS socket lifecycle (Daemon/DaemonClient/NameServer) and process/env APIs (`Getwd`,
  `Chdir`, `Executable`, `TempDir`, `UserCacheDir`, `Environ`).
- Add comments only when necessary to explain something very non-obvious, no comments is fine in most cases
- Comments must be very brief — one line wherever possible, a few at most
- Package-level rationale and design notes belong in the package's `CLAUDE.md`, not in source comments

### When a comment earns its place

The test: **a competent reader, holding the code in front of them *and* the package's `CLAUDE.md`, would still get
it wrong.** "Does this sentence add information?" is the wrong bar — almost anything passes it. Keep only:

- **Foreign-API gotcha** — surprising stdlib/third-party behavior, invisible at the call site. E.g.
  `os.Symlink won't overwrite`, `Windows refuses an open dir removal`, `cases.Caser is not safe for concurrent use`,
  `filepath.IsLocal(".") is true`, `hash.Hash.Write never returns an error`.
- **Anti-"fix" guard** — code that looks wrong, redundant, or misordered, which a maintainer would "simplify". E.g.
  `Not a const only for Unit Test's sake`, `Success only — NOT deferred`,
  `computeDirtyHash, NOT the cached getDirtyHash`.
- **Silent coupling** — breaking it fails no compiler, `go vet`, or test. E.g.
  `Hard-coded; TestBase32AlphabetMask re-derives it`,
  `Both tables ordered before the fold: a platform override then lands in its top-level key's position`.
- **Inline arg label** on a bare literal — `false /*noDaemon*/`, `true /*fix*/`, `// B<combined> -> D<buildHash>`.
- **Field key→value shape** the field name doesn't carry — `// build-dir-rel → staged-source hash`. The shape only;
  lifetime and invalidation rules go in the package's `CLAUDE.md`.
- **Package doc** — one line, on one file per package.
- **Tests** — the cause of the regression a test guards, when it isn't visible from the test body; plus a one-line
  scenario where the test name is opaque.

Everything else goes. Explicitly:

- Restating the name or signature — including `"" on a miss` and `panics on error`, which are repo-wide conventions,
  not per-function facts.
- Paraphrasing the next few lines.
- Design rationale, invariants, layout, cross-package contracts — those belong in the package's `CLAUDE.md`.
- Test narration: the test name plus its assertions are the spec.
- Section banners (`// --- fixtures ---`) and helper docs whose name already says it.

## Module root (`package Bufa`)

- `ver.go`, `package Bufa` at the repo root. `Version` = raw `go:embed` of root `version.txt` — the single place the
  version number is managed (edit to bump). Embedded **untrimmed**, so the file must be exactly one semver line with
  **no trailing newline**; `cmd/bufa/version_test.go`'s `TestVersionFormat` (full `^\d+\.\d+\.\d+$` match) is the
  build-time gate enforcing that (a compile-time assert is impossible — embed yields a var, not a const), kept with the
  consumer so the root package stays a single file. The root package exists because a `go:embed` path cannot leave the
  package dir, and the file belongs at the root. Read by cmd/bufa's `--version` and by Watcher/DaemonClient for the
  daemon version check; imports nothing from the repo.

## Packages

- [FilterFiles](FilterFiles/CLAUDE.md) — `+`/`-` glob rules: pattern syntax (`*` vs `**`, trailing `/`, anchored vs
  basename-at-any-depth, case-insensitive), last-match-wins with no-match ⇒ excluded, and who supplies the implicit
  `+**` baseline — `Compile`'s positional heuristic vs `Compose`'s three tiers.
- [Hashing](Hashing/CLAUDE.md) — the `F…`/`D…` key format itself (base32 alphabet, name-sorted manifest,
  `EmptyDirHash`, prefix-less `CombineHashes` the caller labels), the filtered-walk contract (children-only skip,
  empty-dir prune matched to Store's copy), the symlink leaf rule, the trusted hash-named-link fold + `SafeHashing`,
  and the parallel per-dir file walk on a `Util.ForkJoin` (one pool per walk threaded down the recursion, workers
  that never recurse, the ctx-tree stop rule — a parent's failure stops children, not vice versa — rescued
  file-goroutine panics, `Cleanup` reaping on a walker failure).
- [Cache](Cache/CLAUDE.md) — memoization wrappers over a real getter: which miss marker each flavor uses (`Wrap_Def`'s
  zero value vs `_Bool`'s explicit `ok`), why a zero from realGet is never written back, and `Map`/`Memoize`/`FromMap`
  + the `cacheName` hit-logging surface.
- [Store](Store/CLAUDE.md) — content-addressed store: the `in`/`out`/`bld`/`dirty`/`tmp`/`user` layout and its
  `D`/`B`/`P`/`∕` name encoding (out/ is a two-hop ∕→B→D chain; `Link` re-points a `B`/`∕` whose target moved — a
  forced rebuild's right to supersede; `user/` holds each build dir's path-keyed, never-inspected `BUFA_CACHE_DIR`
  entry as a `P<pathHash>` dir + `∕<path>` link pair, and `nuke --cache-only`'s empty-but-keep engine),
  stage-then-rename publish vs `MoveStore`'s scan-before-mutate
  `PublishPlan`, the one prune/filter/symlink skip rule shared by the hash and copy walks (incl. the hard error on a
  leftover `<base>.BUFA` store *directory* in source), the reserved-`BUFA` namespace (with `BUFA.shell` and
  `BUFA.env` deliberately outside it) and virtual-config bijection, which existence probe panics and which stays
  silent, and
  `bufa gc`/`bufa check`
  (gc also sweeps a failed build's or shell session's leftover `bld/`/`tmp/` and measures freed bytes — regular
  files of removed trees
  only, symlinks count 0; fix removes everything check reports, unexpected entries
  included — the build root's own top level too, judged by the one ownership predicate `Nuke`'s verify-then-delete
  of the whole build root refuses on).
- [ArtifactCache](ArtifactCache/CLAUDE.md) — global per-user cache of pinned `[[deps.ext]]` artifacts: the
  verify-at-insert / trust-on-hit admission gate, flat `F<hash>` entry naming (what lets a `large` link fold instead of
  stream), url links named by the look-alike-Unicode-encoded url text (overlong ⇒ trimmed + `_U<hash>` tail),
  read-only entries, same-dir staging + atomic-rename publish under lock-free concurrent writers,
  `$BUFA_GLOBAL_CACHE_DIR` resolution, the `hash = "?"` bootstrap, why nothing ever evicts except `Nuke`'s
  user-requested whole-cache delete, and `bufa check`'s cache half (`Check`) with the ownership rule it shares with
  `Nuke`.
- [BuildConfig](BuildConfig/CLAUDE.md) — the `BUFA`/`.BUFA`-marker shapes + the two TOML gateways: runtime-only
  `toml:"-"` fields (`Hash`, `VirtualDir`, `VirtualDirMaterialized`) vs user-settable ones, the ordered `EnvTable[V]`
  both `[env]` tables decode into (document order restored from `toml.MetaData.Keys()` inside the gateway, a platform
  override keeping its top-level position), the `cmd` string-or-`false` union (`cmd = false` ⇒ a deliberately
  scriptless stage+publish dir — the only script-optional opt-in), `deps.ext`/`deps.export`/
  `deps.cacheDir`/`largeOutput`/`shell`/`[env]` semantics with `EnvVar`'s literal-vs-`{ file }` dispatch, the
  `[windows]`/`[unix]`/`[linux]`/`[macos]` fold tree (four top-level tables; `root ← [unix] ← [linux] | [macos]`
  via `PlatformSections`, same rules at each level — presence-not-truthiness scalars incl. `shell` and
  `deps.cacheDir`, root-first list concat, per-key env merge — sections zeroed afterwards), unknown-key hard-fail
  vs the
  unparseable-TOML-is-a-raw-script fallback and its parser-progress guard (`LastKey` or a whole-line prefix that
  yields a key ⇒ broken config, so a raw script must not open with a `NAME=…` line; the first-statement hole), the
  shared `[env]` name/value rules (`CheckEnvVarNames`/
  `NormalizeEnvVarValue`), and the root marker's `RootConfig` — `[unsafe].ttl` including the `0` sentinel, the
  literals-only root `[env]` that `Validate` trims in place, the alias-or-absolute `shell` default + `[shells]`
  catalogue, and
  `GetHash()` (which marker settings re-key every dir — `[env]` and `shell`, plus the non-marker `Bufa.Version`'s
  `x.y` — and why `ttl`, `[shells]`, and the patch number are not among them); plus the `BUFA.shell` schema
  `ShellDef` (`IsShellPath`'s name-vs-path grammar, `DecodeShellDef`'s validation, nil-vs-`[]` session argv) and the
  `BUFA.env` dotenv decode `DecodeEnvFile` (first-`=` split, verbatim value, line-trim, which malformed lines
  hard-fail).
- [Runtime](Runtime/CLAUDE.md) — invocation-wide `Config`: srcRoot discovery (NameServer-first, then one walk with
  marker precedence `.BUFA` > nearest `.git` > topmost `BUFA`; anchored-only NS
  back-fill, the split-roots hard error, the kind-tagged `Src root:` line), config-absent `.BUFA`-marker defaults,
  the sibling `<base>.BUFA` build root (an fs-root source demands `$BUFA_BUILD_ROOT`),
  `$BUFA_BUILD_ROOT`/`$BUFA_NO_DAEMON`, the RO-src FS that still forwards
  `RealPath`, the `ShowOutput`/`ForceRebuild`/`BuildMode`/`TargetDirs` per-dir policies + the shell's `In` (which the
  CLI assigns post-`Prepare*`; `ForceRebuildFor` is also true for the shell target; `BuildMode`, not `Shell` — which
  shell is BuildConfig's key),
  and which `Prepare*` leaves `Daemon`/`RootConfig` unset.
- [Build](Build/CLAUDE.md) — clean + dirty builders: the `B<combined>` cache-key terms (config/deps/platform/the root
  marker's `GetHash()` — whole `[env]` table + `shell` default + bufa `x.y` version/`unsafe` TTL bucket),
  ancestors-first
  staging and `largeOutput` link
  staging with its
  leaf rule, publish pruning and its `deps.export` exemption (a nested dep the script accumulates into ships with
  the output; not-a-dep/non-nested/link-staged exported entries hard-fail), virtual `<suffix>.BUFA` units, the
  src/bld/dirty filter tiers, `[[deps.ext]]` and
  `[env]` (root entries, then each direct bld dep's published `BUFA.env` in dep order — implied shell provider
  last, direct dependents only, zero new key terms, each dep file a closed unit whose refs bake at its own layer,
  a dep-vs-dep plain overwrite rejected unless the value is identical or chains on the name — then dir entries, a
  later namesake
  wins; `${NAME}` expansion at
  script-env assembly, document
  order, each value seeing only what precedes it — a root↔dir ref to a name (re)defined later is a hard error,
  self-refs
  exempt and chaining layer on layer; file-sourced
  values included), the hermetic script env (the
  two root/relative `BUFA_*` pairs — `BUFA_BUILD_ROOT`/`BUFA_BUILD_DIR`, `BUFA_CACHE_ROOT`/`BUFA_CACHE_DIR` — and
  why the relative halves are what scripts bake into generated files; the per-dir persistent cache dir under
  `deps.cacheDir` — ensured just before the script, same value in
  every mode, zero cache-key terms), the zero-FS hot path, dirty mode's stacked skip-hash tiers, which tiers a
  forced rebuild (`--force`/`--force-all`) bypasses in each mode (getters swapped for `alwaysMiss`, setters kept),
  the `--shell`/`--post-shell` sessions (the script's own interpreter, cwd, and env; never a build — neither
  publishes nor records; post-shell runs the script first, failure or not), and **Shells** — the `cmd`/`bash`
  presets vs a provider dir's published `BUFA.shell`, the dir > marker > native resolution and `[shells]` alias
  lookup, the implied provider dep (keyed by path + output hash, no new term; explicit listing and self-reference
  are errors, a preset-shadowing alias resolves its own provider to the preset), cold-path-only definition load from
  the mode's output tree, `./`/bare/absolute `exe` resolution, definition-sourced `BUFA_COPY_OR_MOVE`, and the
  `Examples/` nu + pwsh + elvish + prefix-dev/shell providers.
- [DaemonClient](DaemonClient/CLAUDE.md) — build-side wrapper over the Watcher RPC daemon: the nil-handle
  degrade-to-miss/no-op rule, synchronous `Get` vs fire-and-forget `Set`, the `<bldRoot>/bufa-d.sock` convention, the
  Connect / Stop (no wait) / StopWait / Restart (both wait for sock cleanup) lifecycle and its callers, the
  NameServer-takeover
  trigger, the `GetRootConfig` version check with its in-place daemon restart, and why it takes Config primitives
  instead of `Runtime.Config`.
- [Watcher](Watcher/CLAUDE.md) — recursive source watch exposed as the RPC cache daemon: which of its seven caches each
  event class evicts (and which shadows no event ever invalidates), the owning-build-dir walk, the `BUFA`,
  `<suffix>.BUFA`, `.BUFA`-marker, and `.git` event rules, the two-watchpoint layout and its srcDir-self consolidation
  caveat, the four self-shutdown triggers (srcDir gone, `.BUFA`-marker graph events, `.git` graph events, the root's own
  `BUFA` Remove|Rename), the version stamp in `GetRootConfigReply`, and the exported RPC arg/reply types.
- [NameServer](NameServer/CLAUDE.md) — global `%TEMP%/bufa-ns.sock` cwd→srcRoot cache: the server-never-touches-the-FS
  invariant, the anchored-entries-only rule (hits are trusted because unanchored roots are never written), the `alive`
  vs empty-`srcRoot` tri-state, ancestor back-fill by path arithmetic, socket ownership by
  whichever Watcher daemon wins the bind, client-driven takeover when that daemon dies, and the `Shutdown` RPC +
  `Stop` client behind `bufa daemon stop/reset`.
- [Daemon](Daemon/CLAUDE.md) — single-instance UDS RPC transport rules: the `<exe> --daemon <sockPath> …` self-spawn
  contract, what `alreadyRunning` means, listen-first/dial-probe stale-socket recovery plus the socket unlink on exit,
  MkdirAll-before-Listen, the per-Accept idle deadline (traffic on open conns never resets it), and the tolerated
  bind/listen orphan race.
- [internal/contract](internal/contract/CLAUDE.md) — panic-based `Require`/`Assert`/`Check*`/`Error*` plus the
  `Catch`/`Rescue` bridge back to `error`: the `With(…).Check2(…)` two-call shape and why, the `"%s\n%w"` context
  wrapping that makes every CLI's error output multi-line, the pre-panic Error log and its level gate, and the
  `Require`-not-`if`+`Fail` house rule.
- [internal/Util](internal/Util/CLAUDE.md) — `Set`/`Nothing` (the empty RPC arg/reply alias), the `IMap`…`IMutMapLen`
  interface family + plain `Map` (Cache's aliased backing), `OrderedMap[K, V]`
  (insertion-ordered, private fields with its own gob encoding — position-keeping `Set`, no delete), `NormalizePath`'s
  cache-key fold (`ToLower`+`ToSlash`, deliberately no clean/absolutize), and the containment predicates and which one
  panics on unrelated paths.
- [internal/UnsafeIO](internal/UnsafeIO/CLAUDE.md) — the raw OS-path escape hatches: `GetRealPath` + the strict
  `RealPath` vs Abs-fallback `OSPath` translation split and which vfsx op rides which, `OSReadlink`'s loud flavor for
  store link reads, and the silent-vs-panicking OS-path probe family (`OSFileExists`, `OSDirEntryExists`,
  `OSTryStat`/`OSTryLstat`) for pre-vfs paths (srcRoot discovery, Watcher event probes).
- [internal/vfsx](internal/vfsx/CLAUDE.md) — os-level symlink/lstat/copy ops over injected fs values: the
  `Lstat`/`Readlink` fallback chains, the X-prefix cross-store ops (`XSymlink`, `XCopyFile` + the `UseCopyFileOS`
  CopyFileW engine), the generic vfs conveniences (kind-specific existence probes, `ReadSmallFileFast`'s 3-OS-call
  contract, `RemoveDirIfEmpty`), and the socket/process-API exclusions from the file-I/O rule.
- [internal/logging](internal/logging/CLAUDE.md) — single-line pretty `slog.Handler`: level precedence
  (arg → `$LOG_LEVEL` → `none`, which sits above Error so the default is silent), the `$LOG_SOURCE` source gate, attr
  quote/flatten rules, and `LevelNames` as the sole source of bufa's `--log-level` enum.
- [internal/runmain](internal/runmain/CLAUDE.md) — the `run(args, in, out, errOut) error` seam every binary shares:
  arg/stream injection so `run` is driveable from tests with buffers, the `<name> ERROR: <err>` stderr format and its
  multi-line rule, and exit 1 **only** on a returned error — panics need `defer contract.Catch(&err)`.
- [cmd](cmd/CLAUDE.md) — arg grammar and printed output of each non-`bufa` binary (dual-mode `daemon`, one-shot
  `daemon-client`, `hash-files`, `filter-files`, the test-only `test-shell` interpreter and why it exits with an
  int); `cmd/bufa` is the one user-visible binary and has [its own file](cmd/bufa/CLAUDE.md).

- [Site](Site/CLAUDE.md) — the https://bufa.build website pipeline, not a Go package. Read only when working on the site.

## Build & Test

```shell
go build ./...
go test ./...
go vet ./...
```

All three must pass before done. CI ([.github/workflows/build.yml](.github/workflows/build.yml)) runs them on
ubuntu/windows/macos, so a Windows-only pass is not the bar (case-sensitive FS, FSEvents, an 8.3 `TEMP`). A `v*` tag
push publishes the release binaries; the tag must equal `v` + `version.txt`.
