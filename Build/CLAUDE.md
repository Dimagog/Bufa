# Build

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- **`BuilderBase`** (`BuilderBase.go`) is the state + helpers shared by the clean `Builder` and the `DirtyBuilder`;
  `IBuild` (`Build(srcDir) string`) is the one method both expose. It embeds `Runtime.Config`, owns the `Store` over
  `BldFS`, the circular-dep `buildingNow` set, the local caches (`localBuildCache` srcDir→result hash,
  `localConfigCache`, `localSrcCache` srcDir→staged-source hash — dirty fills it through the shared fetcher but never
  reads it — `localShellDefCache`, `localDepEnvCache`; all trusted, never FS-invalidated), the
  `allowMaterializedVirtualDirs` flag (dirty only), the lazy `artifactCache` (`sync.OnceValue`, resolved only when a
  build touches `[[deps.ext]]`), and `rootConfigHash` = `rc.RootConfig.GetHash()` taken **once at construction** (a
  `RootConfig` assigned later is not keyed). The concrete constructors wire the `buildCached`/`getBuildConfig`/
  `loadShellDef`/`loadDepEnv` func fields; `wireOutputLoaders(fs, fsRoot)` wires the shell-definition and dep-env
  loaders together so both read the same mode-output tree; `wireGetBuildConfig()` installs the daemon-first config
  fetcher (daemon disabled ⇒ `readBuildConfig`). `writeBuildScript` writes the platform script to `tmp/BUFA<ext>` and
  returns its **absolute OS path** (dirty's cwd is in the source tree; the build root may be on another drive).
  `runScriptStep(srcDir, osDir, env, cfg, shellDef)` is the one script step both modes call: `ModeBuild` ⇒
  `runScript` over `scriptCommand`, a session mode ⇒ `runInteractiveShell`; it `RemoveAll`s `tmp/` after a
  **successful script run only**. `combinedBuildInputs(srcDir, config, bld.deps, src, src.deps)` is the single
  definition of the seven input terms shared by the clean combined hash and the dirty skip hash (clean appends
  `unsafe.ttl`).
- `Builder.Build(srcDir)` returns the **output content hash** (`HashDir` of the post-script dir), reused by recursive
  bld-dep callers. With the daemon down, every `Daemon.Get*`/`Set*` degrades to the direct Store path.
- **Virtual build units (`<suffix>.BUFA`).** A unit is **real** (a `BUFA` in its own dir) or **virtual** (declared by
  `<base(srcDir)>.BUFA` in the parent — `Build/clj.BUFA` ⇒ `Build/clj` — with **empty** own source).
  `readBuildConfig`: `vfsx.TryReadSmallFileFast(srcDir/BUFA)` non-nil ⇒ real, else `<parent>/<base>.BUFA` ⇒ virtual
  (`cfg.VirtualDir`). The declaring dir need not be a build dir; `srcFiltersOverride`'s unanchored `-*.BUFA` keeps
  the config out of every staged tree. The virtual dir must **not** exist in source: `locateBuildConfig` probes once
  (`vfsx.DirExistsFailOnFile`) — a non-directory panics in both modes, a directory only records
  `cfg.VirtualDirMaterialized`, and `requireVirtualDirAbsent` (on **both** `fetchBuildConfig` branches, so a
  daemon-served config is checked FS-free) enforces it in clean mode; dirty tolerates via
  `allowMaterializedVirtualDirs`. A real BUFA + virtual config for one path is a collision. No suffix is reserved
  (the marker's empty suffix never decodes). Relative deps resolve against the virtual dir (`resolveRelDir`).
- **Source filters are intrinsic per dir.** `storeGetSrcHash(dir)` resolves the dir's own `[filters].src` from
  `getBuildConfig(dir)`, then `Store.SrcPrep` with `allowLinks=cfg.Unsafe` (safe dirs reject links — a followed
  referent is invisible to the watcher; unsafe dirs stage them as link objects), or `Store.SrcPrepEmpty` when
  virtual. A parent's filter never cascades onto its src deps, keeping the per-path daemon `srcHashCache` correct.
  **Every srcDir Build touches must carry a real or virtual BUFA; absence panics in `readBuildConfig`.**
- `srcFilterFor(rules)` = `FilterFiles.Compose(srcFiltersDefault, rules, srcFiltersOverride)`: default `-.**` (user
  rules override) + override `-/BUFA`, `-*.BUFA` (highest — no config or marker is ever staged, even via an explicit
  `+`). `-*.BUFA` is **unanchored** because a `<suffix>.BUFA` declares a virtual unit at any depth; staging one would
  re-key its owner on every edit (`*` matches the empty run, so it matches `.BUFA` but not bare `BUFA`). `-/BUFA`
  stays anchored: a subdir holding `BUFA` is pruned by `skipNested` first. `bldFilterForOrNil(rules)`: nil when
  `[filters].bld` is empty (MoveStore deletes nothing and keeps empty dirs, hidden files included), else
  `FilterFiles.Compile(rules)` — no hidden-files default. `fetchBuildConfig` asserts a daemon-hit config has a
  non-empty `Hash`; `mergeKnownHashes` folds the bundled src hashes into `localSrcCache`.
- **Zero-FS hot path**: a repeat safe build with a populated Watcher cache does no FS I/O — config, src hashes, and
  `combined → buildHash` are daemon-served. The `out/∕<srcDir>` GC root stays current without touching the hot path:
  `daemon.GetBuildHash` also returns `PathBuildHashCurrent` (the `pathBuildHashCache` shadow), and `build()` writes
  `LinkPath` + `SetPathBuildHash` **only when not current**. `in/∕` needs no shadow: the src-hash cache is
  change-invalidated, so `SrcPrep` (which maintains `in/∕`) runs only after a source change. Invariant: a populated
  src hash always has its `in/` content (only `SrcPrep`/`SrcPrepEmpty` populate it; virtual units key on
  `EmptyDirHash`). Out-of-band Store manipulation under a live daemon is unsupported.
- **Unsafe dirs** (`unsafe = true`): `unsafeTTLHash` truncates `BuildStartTimeUTC` to `[unsafe].ttl` and salts
  `combined` with the bucket; TTL `0` ⇒ millisecond precision (one bucket per invocation).
- **Build flow per srcDir**: ⓪ local hit ⇒ return. ① `getBuildConfig` (miss ⇒ `readBuildConfig` = read + decode +
  `Hash=FilePrefix+HashBytes(raw)` + dep path resolution, then `SetBuildConfig`), then `shellFor` (FS-free). ②
  recurse into each of `effectiveBldDeps` (the config's list + the shell provider dir when provider-backed). ③
  `getSrcHash` each src dep, ④ own source. ⑤ `combined` = `BuildPrefix` + `CombineHashesInPlace` of the terms
  `config, bld.deps, srcPath, platform, root.config, src, src.deps, unsafe.ttl?`: `platform` = `GOOS/GOARCH` (a
  store shared across OSes never cross-serves); `root.config` = `RootConfig.GetHash()` — the marker's entire
  `[env]`, its `shell` default, and the `x.y` of `Bufa.Version` (a minor upgrade is a total rebuild: old `B` links
  go unreachable and
  `gc` sweeps them; a patch upgrade re-keys nothing); `srcPath` = `HashPath(ToSlash(srcDir))` — every path-derived
  value the script sees (`%CD%`, `BUFA_BUILD_DIR`, `BUFA_CACHE_DIR`) is a function of it, so a moved dir re-runs its
  script instead of serving output with a stale baked path (`Doc/Bugs/RenamedDirBakedPaths.md`); the output `D`
  still dedups across paths. ⑥ `daemon.GetBuildHash(combined, srcDir)`, falling through to `storeGetBuildHash` =
  `GetLinkTarget(OutRoot, combined)` + `daemon.SetBuildHash`. ⑦ hit ⇒ done. ⑧ miss ⇒ `realBuild`, then
  `storeSetBuildHash` = `Link(OutRoot, combined, buildHash)` + `daemon.SetBuildHash`. ⑨ iff not current ⇒
  `LinkPath(OutRoot, srcDir, combined)` + `SetPathBuildHash`. **Forced rebuild** (`ForceRebuildFor`,
  `--force`/`--force-all`): ①–⑤ unchanged; at ⑥ both result *getters* are swapped for `alwaysMiss` while the
  *setters* stay, so the wrapper chain records the fresh result itself; ⑨ always runs. Local caches ⓪ are untouched,
  so `--force-all` builds a diamond's shared dep once. A non-deterministic script may publish a different `D` under
  the same `B`; `Store.Link` re-points, and `storeSetBuildHash` prints a **non-fatal**
  `WARNING: Dir '<dir>' build is not reproducible …` from `Link`'s returned previous target. Dirty has no analog
  (its forced path skips the pre-build compare).
- **Cold path** (`realBuild`): fresh sandbox `bld/<srcDir>` mirroring the real tree from the root — own + deps
  restored at root-relative paths, **ancestors-first** (sorted by segment count; both `Build` entries
  `filepath.Clean` their srcDir) so `Restore`'s empty-destination invariant holds in both nesting directions. **Link
  staging** (`Doc/Specs/LinkStaging.md`): a bld dep declaring `largeOutput` is staged via `Store.RestoreLink` (one
  dir symlink to its `out/` tree) instead of copied; src deps and own source always copy. The **leaf contract** is
  checked over the sorted target set before any sandbox mutation: no staged target may lie strictly below a
  link-staged one (`Util.StrictlyBelow` pair scan) — restoring through the link would mutate store content. No
  cache-key term changes (the dep's output hash is unchanged). The sandbox wipe removes link objects without
  traversing; failure keeps the sandbox.
  Script: `tmp/<script>`, gone with `tmp/` on success, kept on failure; `cmd.Dir` = unit dir; interpreter = the
  dir's shell definition (**Shells**). **Env**: inherited verbatim for `unsafe` (`newEnv(os.Environ())`); for safe
  dirs `newFilteredEnv(…, safeInheritedEnv)` keeps only Windows `OS`/`SystemDrive`/`SystemRoot`/`windir`
  (`Doc/Specs/WindowsMinEnvVars.html`'s must-keep list minus what bufa sets itself), Unix **nothing**. Then
  `setBufaEnv` (one list shared with dirty): `BUFA_BUILD_ROOT`=`<bldRoot>/bld` + `BUFA_BUILD_DIR`=root-relative src
  dir (`%BUFA_BUILD_ROOT%\%BUFA_BUILD_DIR%` == cwd in both modes; relative so a script can bake it into a generated
  file and get byte-identical output on every machine), `BUFA_CACHE_ROOT`=`<bldRoot>/user` (pure path arithmetic),
  `BUFA_COPY_OR_MOVE`=the definition's `move` verb (dirty: `copy`; no pair ⇒ not exported). Then, when
  `deps.cacheDir = true`, `BUFA_CACHE_DIR` (`setCacheDirEnv`, `Doc/Specs/CacheDir.md`) = the bare
  `P<hash of srcDir>` entry name of the dir's persistent, never-wiped cache dir, ensured by
  `Store.EnsureCacheDir(srcDir)` just before the script (cold path only); identical value in every mode and across
  forced rebuilds; **no cache-key term** (a function of `srcPath`; the flag is BUFA bytes). Then the platform temp
  vars — Windows `TEMP`+`TMP` (and unless `unsafe` `PATHEXT`=`.COM;.EXE;.BAT;.CMD`, no script hosts, plus
  `ComSpec`), Unix `TMPDIR` — at
  `<bldRoot>/tmp`, recreated before every script so builds can't communicate through the ambient temp dir, even for
  `unsafe`; unless `unsafe`, `PATH` = `cleanSystemPath` (Windows System32 set, Unix `/usr/bin:/bin:/usr/sbin:/sbin`)
  and a safe Unix build gets `HOME`=`<bldRoot>/tmp`. Finally the root marker's `[env]`, the dep `BUFA.env` layers,
  then the dir's (`setAllEnvVars`, **Env vars**) — set **last** so `${NAME}` refs see every bufa var and an `[env]`
  namesake overrides any of them.
  Output: `ShowOutputFor(srcDir)` decides **before** the run — stream live (`Out` assigned to both streams, the same
  writer so os/exec serializes; an `*os.File` is handed to the child as the fd) with an unconditional pre-footer
  newline, or buffer and dump the identical frame on failure only. Frame: `----- Build Start: <dir> -----` …
  `-----   Build End: <dir> -----` / `----- Build FAILED (exit code N): <dir> -----` (the cause also rides the
  `Checkf` panic to the CLI, so the exit code prints twice by design).
  Publish: `MoveStore(OutRoot, bldDir, plan)` with a `PublishPlan` of `bldFilterForOrNil`, `PruneDirs`,
  `ExtPrune`, `KeepLinks`, and `AllowLinks: cfg.Unsafe`. `PruneDirs` = each dep restore target strictly inside the
  unit dir
  (`Util.RelStrictlyBelow`), ToSlash'd, **minus `deps.export`** (`Doc/Specs/ExportDeps.md`): an exported dep
  publishes with the output — staged content plus whatever the script wrote into it (the accumulate-into-a-dep
  pattern). `readBuildConfig`'s `resolveExportDeps` (after `normalizeExtDeps`) turns each `ExtDep.Export = true` into
  a list entry and clears the flag, then resolves every entry like a dep path: it must be a `deps.bld`/`deps.src`
  entry strictly below the dir or an `[[deps.ext]]` landing path, else hard error. `realBuild` rejects an exported
  dep that is link-staged (the script would write through the link into the store). Publish deletes the pruned trees
  plus dirs left empty; recorded before the script runs. No cache-key term (`export` is BUFA bytes; the content is
  already in the dep terms); dirty prunes nothing.
- **External deps** (`ExtDeps.go`, `Doc/Specs/ExternalDeps.md`): `[[deps.ext]]` artifacts fetched into the global
  `ArtifactCache`, gated by the pinned `F` hash — hermetic, network never on the rebuild path, **no cache-key term**
  (pins and flags are BUFA bytes; a url edit re-keys and costs one re-verifying download — the cache's url-link gate
  makes a version-bumped url with a forgotten hash edit fail loudly). `large` decides the staged *representation*
  (cache link vs writable copy), `export` output *membership* (folded into `deps.export` at config read).
  `normalizeExtDeps` (in `readBuildConfig`): `hash` a valid `F` key or `"?"`, `Name` default = url basename,
  cleaned, strictly-local, basename outside the `BUFA`/`*.BUFA` namespace, no case-folded dups.
  `requireExtDepSourceNamesAvailable` runs on the cold source-hash path (raw `Lstat`: files, empty dirs, dangling
  links all collide). **Clean staging** (`stageExtDeps`, after restore): overlap with a nested dep target
  (`Util.PathsOverlap` over `pruneDirs`) ⇒ conflict; an occupied sandbox path ⇒ hard error; else `ensureExtDep`,
  then `large` ⇒ `vfsx.XSymlink` into the cache, default ⇒ `Store.CopyFileIn`. **Publish** (`extDepPublishPlan`):
  non-exported names go into `ExtPrune` (removed kind-blind in MoveStore, plus emptied parents); exported `large`
  links into `KeepLinks` — the only symlinks a safe publish admits (`cfg.Unsafe` waives the policy for script-created
  links). **Script optional** (`cmd = false`) ⇒ stage+publish only: an exported-ext-only dir is a complete artifact
  **provider**. **Dirty** (`placeExtDeps`, before the script): `export` no-op; `large` ⇒ cache link, else writable
  copy (`vfsx.XCopyFile` + chmod) into the real dir. Repeat check: a link onto a **present** `cache/F<pin>` or a file
  re-hashing to the pin ⇒ current — but still gated by the cache's url link (`ensureExtDepURLBinding` for a link,
  full `ensureExtDep` for a copy), so an edited url under a stale pin re-verifies; a provably-bufa stale placement is
  re-placed; anything foreign (file, link, dir, script-mutated copy) ⇒ hard collision error. The artifact joins the
  dirty tree hash, so deletion self-heals; `placeExtDeps` reports whether it changed the tree (heal-skip, **Dirty
  mode**). Symlink rights: `large` placement in dirty mode, and every fetch in either mode.
- **Env vars** (`EnvVars.go`, `Doc/Specs/BufaEnvVars.md`, `Doc/Specs/EnvVarInterpolation.md`): `[env]` literals or
  file-sourced vars are `${VAR}`-interpolated into `[[deps.ext]]` **urls only** (braced-only; lone `$`, unterminated
  `${`, empty `${}` verbatim; `$${` is the one escape; an undefined ref, or a ref to a value itself holding `${`, is
  a hard error — urls are cache-key terms and must stay static) and exported into the script env in both modes in
  **document order**. A key naming an inherited or bufa-set var (`PATH` included) **overrides** it (last-wins `Set`,
  folded on Windows); only `BUFA_` is reserved, rejected at config read. **`${NAME}` expansion** (`Env.setExpanded`
  over `expandRefs`) happens at script-env assembly against the env so far — inherited, bufa vars, root entries,
  each dep's `BUFA.env`, then the dir's earlier entries — so a value sees only what precedes it. A ref to a name any
  **later** entry of the root↔dir pair (re)defines is a hard "(re)defined later" error (`definedLater` counts in the
  one pass; run for `unsafe` too — the ref would otherwise bake a value the script won't see). A self-reference is
  exempt: it resolves to the layer below (`PATH = '${PATH};…'` chains root→dir). Any other unknown name is
  "undefined". Expansion never happens at config read (the layers exist only per build), so `[env]` enters the cache
  key as written and a ref to an inherited var adds no key term. **`Env`** (`Env.go`) is the one script/session env
  value: `vars` (folded name → original name + value) + `order`; `newEnv` applies os/exec's leading-`=` rule; `Set`
  is last-wins **and moves the name to the end** (os/exec's dedup, so `Environ()` carries no duplicates);
  `newFilteredEnv(environ, keep)` never builds the full environ. `resolveEnvVars` runs in `readBuildConfig` after
  the platform fold and before `normalizeExtDeps` (the default `Name` derives from the FINAL url), **rewriting each
  entry to its literal** so cached configs are final and a daemon-served build exports FS-free. Files follow ext-name
  rules (shared `cleanLocalName`: strictly-local, non-reserved basename) — locality is what keeps the daemon cache
  honest; rejected in virtual configs. Names case-folded-unique (BuildConfig's `CheckEnvVarNames`), values trimmed
  to one line (`NormalizeEnvVarValue`) — shared with the root marker. **Zero new cache-key terms** (table + literals
  ⇒ `Config.Hash`; a var file is own source ⇒ src term). **Root marker `[env]`** (`RootConfig.Env`, literals only,
  validated + trimmed at marker read): `setAllEnvVars(env, srcDir, cfg, depEnvs)` first runs `checkRootDirEnvCase`
  (a dir key matching a root key only case-folded is an error; an exact match is a legal override) and
  `checkDepOverwrites` (**Dep env vars**), then sets root entries (labelled `.BUFA`), each dep layer
  (`<dep>/BUFA.env`), then the dir's. Root vars are read live from `b.RootConfig` at script time, never baked into
  the cached dir config, and are **not** interpolated into ext urls (a marker edit doesn't evict dir configs). Their
  one key term is `root.config` — name-sorted, so marker entry order is not a term.
- **Dep env vars** (`DepEnv.go`, `Doc/Specs/DepEnvVars.md`): a bld dep whose published output root holds `BUFA.env`
  exports its vars into every **direct** dependent's script env — bld deps only (the implied shell provider
  included), no transitivity, absent file ⇒ no-op. Loaded on the cold path from where the mode put the output (clean:
  `BldFS` under `bld/`, through the dir symlink when link-staged; dirty: `SrcFS` in place) via `loadDepEnv` over
  `localDepEnvCache`; malformed ⇒ hard error at the consumer's script step. `depEnvsFor(bldDeps)` collects layers in
  dep order (implied provider last) from the `effectiveBldDeps` list each mode's `build()` threads down to its
  script step (`bldDepsHashes` can't carry it — `CombineHashesInPlace` sorts in place). Each dep file is a **closed
  unit**: its own `definedLater`, refs chain on the layers below like a self-reference, its names never gate a root
  ref, and the root↔dir case check skips it. The one cross-layer rule, `checkDepOverwrites`: a later dep redefining
  an earlier dep's name (case-fold) must carry the identical value or chain on the name (`refersTo`); root and dir
  namesakes stay legal overrides. **Zero new cache-key terms**: the file rides the dep's output hash. `BUFA.env`
  sits outside `IsReservedName` like `BUFA.shell`, so it stages and publishes like any source file.
- `Build` self-recurses and panics on circular `deps.bld` (`circDepsCheck`) before scripts run. On success only (NOT
  deferred) it removes the whole `bld/` tree; a failed script or a shell session keeps `bld/<srcDir>/` and `tmp/`
  for inspection — the next build wipes them, `bufa gc` reclaims them. Per-input/combined/hit lines at `slog.Info`;
  Store + Hashing internals at `Debug`.
- **Dirty mode** (`DirtyBuild.go`, `Doc/Specs/DirtyBuildMode.md`): `DirtyBuilder` embeds `BuilderBase` and builds
  **in place in the source tree** — no staging, sandbox, publish, `in/`/`out/`, or safe/unsafe distinction.
  `Build(srcDir)` returns the dir's **dirty hash**: the content hash of its real directory under `[filters].dirty`
  (`dirtyFilterFor` delegates to `srcFilterFor`, so the same tiers apply; `allowLinks` always true — links hash
  through their referents, a dangling one is still an error), post-script when built (`computeDirtyHash`, the single
  definition), freshly computed when skipped. Outputs are hashed by default ⇒ skip hashes are self-correcting and
  tamper-detecting; excluding outputs via `[filters].dirty` opts into make-like skips. Skip = the stored
  `dirty/∕<srcDir>` skip hash EQUALS the fresh `BuildPrefix + CombineHashesInPlace(combinedBuildInputs(…))` — a
  *state* hash whose `src` term is the post-build own hash, compared for equality only. It is read **before** the
  tree is hashed, so an absent one skips the pre-build walk. Tiers: local maps (`localBuildCache` = post-build hash,
  `localConfigCache`, own `localHashCache` = current tree hash); the daemon (configs via `fetchBuildConfig`; tree
  hashes via `Wrap_Def` over `Get/SetDirtyHash` — both tree-hash tiers are write-updated together by
  `b.setDirtyHash` from `realDirtyBuild`, since every read happened pre-build); and the skip hash as **two stacked
  `Wrap_Def` layers** — outer `Daemon DirtySkipHash` (the `dirtySkipHashCache` shadow), inner `Store DirtySkipHash`
  (the file) — over the shared `checkSkipHash` (hit = stored EQUALS fresh, returning the tree hash; `""` a safe miss
  marker; an absent skip hash stays uncached). Stacking maintains the shadow through wrapper mechanics alone: a disk
  hit beneath a daemon miss primes the shadow, a build records disk then shadow, and a stale shadow self-corrects by
  falling through — mismatch direction only, so the one writer that moves disk skip hashes behind a live daemon (a
  daemon-disabled dirty build) stops the daemon first. The pre-build walk is **advisory** (under `contract.Rescue`):
  a tree that can't hash (e.g. a dangling ext cache link) builds instead of crashing — `placeExtDeps` re-points
  bufa's links and the post-build recompute surfaces what it didn't heal. **Heal-skip**: when placement changed the
  tree, `realDirtyBuild` recomputes the dirty hash and re-checks the disk skip hash — a match returns WITHOUT running
  the script (placement is bufa's bookkeeping, not user change). Script: cwd = the real dir, script at
  `<bldRoot>/tmp/` by absolute path (`runDirtyBuildScript`'s `MkdirAll(tmp/)` creates the build root; removed on
  success), env inherited verbatim + `setBufaEnv` with the mode's values — `BUFA_BUILD_ROOT`=**source root**,
  `BUFA_CACHE_ROOT`=`<bldRoot>/user`, `BUFA_COPY_OR_MOVE`=`copy` (deps are the real source tree) — then
  `BUFA_CACHE_DIR` (the identical `P…` name; outside the watched tree, so never in the dirty hash) and
  `setAllEnvVars`. **Forced rebuild**: both skip-hash getters → `alwaysMiss` (neither the stored hash nor the
  pre-build walk is read), setters stay, and the heal-skip is skipped too, so the script always runs. Virtual dirs
  are **materialized for real** (`SrcFS.MkdirAll`, never wiped); the config still records `VirtualDirMaterialized`,
  so the daemon prime makes the next clean build reject with "perhaps dirty-build output needs cleaning". No
  roundtrip caching, no early cutoff.
- **Shells** (`Shell.go`, `Doc/Specs/ConfigurableShells.md`): the interpreter is a `BuildConfig.ShellDef` from one of
  two sources — the embedded **presets** `cmd` (`cleanComSpec() /D /C|/K`, `/D` skips AutoRun; prompt
  `PROMPT`=`$P$G`; verbs `move`/`copy`; in the map on Windows **only**) and `bash` (bare `bash` on bufa's PATH;
  `--norc` / `--rcfile … -i`; `PS1`=`\s-\v\$ `; `mv`/`cp`; everywhere), or a **provider dir**'s published
  `BUFA.shell`. **Resolution** (`shellFor`, FS-free, step ①): dir `cfg.Shell` > marker `RootConfig.Shell` >
  `nativeShellName`. `resolveShell`: `IsShellPath` ⇒ provider dir = `resolveRelDir(base, value)` (base = the dir, or
  `.` for a marker value); else `RootConfig.Shells[value]`; else a preset; else "unknown shell". Resolved form: `""`
  no shell, `:<name>` preset, or the root-relative provider dir (`:` reserved). A scriptless dir with no session
  resolves no shell, so a definition-only or ext-only provider may carry any `shell` value. Guards: a provider
  resolving to **itself** is "depends on itself via shell" — **except** an alias shadowing a preset name
  (`[shells] bash = "/build/bash"`), whose self-reference resolves to the preset so the provider can build;
  indirect cycles reach `circDepsCheck`. **The dep is implied**: `effectiveBldDeps` = `cfg.Deps.Bld` + the provider dir
  (`slices.Concat`, never `append` onto the cached slice); listing it explicitly is an error. From there it is an
  ordinary bld dep: built first, staged, pruned when nested, **keyed** through `depsHashes`'s manifest (name = path,
  hash = output incl. the `BUFA.shell` bytes). **No new `combined` term**: a dir's `shell` is BUFA bytes; the
  marker's default joins `root.config`; a catalogue edit is already in the dep manifest. **Loading** (cold path
  only): `loadShellDef` over `localShellDefCache` (keys `:cmd`/`:bash` or the provider dir); a provider's
  `<fsRoot>/<dir>/BUFA.shell` is read from where this mode put the output and `DecodeShellDef`'d — absent or
  malformed ⇒ hard error at the **consumer's** script step. `resolveShellExe`: absolute as is; bare ⇒
  `exec.LookPath` in **bufa's** env; any other relative path (`./x`, `bin/x`) ⇒ joined onto the provider's VFS dir
  via `UnsafeIO.RealPath` — never the consumer's cwd. **Applying**: `scriptCommand`/`sessionCommand` over
  `shellCommand` — `exe` + argv with `${script}` replaced by the script's absolute path; `sessionCommand` picks
  `shell` or `postShell` and fails on a **nil** field at invocation; a prompt definition prepends `(bufa shell) ` to
  `prompt.env`. The environment is never logged. `Examples/` is a runnable mini project: a `.BUFA` with
  `shell = "nu"` over a `[shells]` catalogue, hermetic providers `build/nu`, `build/pwsh`, `build/elvish`, `build/shell`
  (pinned archive as a non-exported `large` ext — one per platform under `[[windows|linux|macos.deps.ext]]`, Linux
  and macOS sharing the `[unix]` script and differing only through `[linux.env]`/`[macos.env]` values — unpacked
  under the native shell, `largeOutput`), the ambient
  `build/powershell`, the `build/jdk.BUFA` toolchain provider (virtual; publishes a `BUFA.env` with `JAVA_HOME` +
  `PATH`) consumed by `java/`, and virtual consumers under `hello/`. `Examples_test.go` builds them against a scratch
  store, **skip-if-absent** (pinned artifact cached, ambient exe on PATH, platform command present) — so plain
  `go test` never touches the network, and CI's `examples` job is what un-skips it (it runs `buildall` first, which
  fills the cache). That job sets `$BUFA_TEST_EXAMPLES_REQUIRED`: `failIfSkipped` then fails any skip from a
  `t.Cleanup` (so a helper's own `t.Skip`, e.g. `skipIfNoSymlinks`, is caught too), except the one taken **before**
  it is armed — a unit with no command on this platform. Other tests use
  `cmd/test-shell` (built once per test binary in `Shell_test.go`'s `TestMain`) and a wrapper provider whose
  `BUFA.shell` is the native preset TOML-encoded from `shellPresets`, never a re-spelled literal.
- **Shell sessions** (`--shell`/`--post-shell` → `ModeShell`/`ModePostShell`, **target dir only**; both make
  `ForceRebuildFor` true so a session always reaches the cold path): the interactive shell is the definition's `exe`
  + `shell` argv (`cmd /D /K`, `bash --norc`) with the **identical** `cmd.Dir` and env the script got, plus
  `PROMPT`/`PS1` = `(bufa shell) ` + the env's own value; stdin = `rc.In`, stdout+stderr = `Out`; framed by
  `----- Shell Start: <dir> ('exit' to continue) -----` + `Dir:` + `Script:` (written to `tmp/` even when not run)
  and `-----   Shell End: <dir> -----`. The shell's exit status is ignored; Ctrl+C is **caught** (`signal.Notify`, not
  Ignore — an ignored disposition is inherited across exec) so the shell handles it. A `cmd = false` dir still opens
  its shell. **A session is never a build**: deps build, the target stages exactly like a build, then **nothing is
  published or recorded** — clean `realBuild` returns `""` without `MoveStore` and keeps the sandbox + `tmp/`; dirty
  returns `""` without `setDirtyHash`; the CLI prints no `Build result:`. `ModeShell` replaces the script;
  `ModePostShell` has the **shell process itself run the script** (`cmd /D /K <script>`,
  `bash --rcfile <script> -i`, nu `-n -e "source <script>"`) so the session keeps its state — a bare `exit` in the
  script ends the session, by design; no frame, no exit-status check.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Store](../Store/CLAUDE.md) — `Restore`'s empty-or-absent-destination invariant is what the ancestors-first sort
  relies on, and the `Util.StrictlyBelow` leaf scan exists because restoring below a `RestoreLink`ed dep would write
  *through* the dir symlink into store content — silent corruption, not an error. `PublishPlan`'s `PruneDirs`/
  `ExtPrune`/`KeepLinks` are filled here as bare paths — `PruneDirs` already **minus** `deps.export`, the only place
  an exported dep is exempted; Store never learns what an ext or exported dep is, so its kind-blind removal is the
  whole contract. `storeSetBuildHash` relies on `Store.Link` **re-pointing** a present `B<combined>` (a forced
  rebuild publishes under an already-linked key) and on its returned previous target for the "not reproducible"
  warning. `setCacheDirEnv` keys `Store.EnsureCacheDir` by the **same** cleaned root-relative `srcDir` every other
  `LinkPath` gets; another form silently makes a second entry or lets gc collect a live one. `ShellDefFileName`
  (`BUFA.shell`) and `EnvFileName` (`BUFA.env`) must stay **outside** `IsReservedName`: `srcFiltersOverride` and
  `cleanLocalName` consult it, and reserving either would silently drop a provider's definition or env exports from
  its own staged source (an absent `BUFA.env` is a legal no-op, so the loss is silent).
- [Watcher](../Watcher/CLAUDE.md) — the daemon evicts `buildConfigCache` per **owning build dir** on any file event,
  which is what makes `[env] file =` sources and `[[deps.ext]]` names strictly-local (`cleanLocalName`):
  `readBuildConfig` bakes the file's bytes and the resolved url into the cached config. Relaxing locality here, or
  narrowing that eviction there, serves stale configs. A `.BUFA` write evicts only the daemon's `rootConfig`, so this
  package keeps root `[env]`, the `shell` default, and `[shells]` **out** of the cached dir config — read live from
  `b.RootConfig` at script time, never interpolated into ext urls. A provider's `BUFA.shell` and a dep's `BUFA.env`
  reach consumers through the provider's **output hash**, read fresh from the staged/in-place output on every cold
  path, never through a daemon cache.
- [Hashing](../Hashing/CLAUDE.md) — `combined` works because `CombineHashes*` folds `NamedEntry` as **opaque**
  strings with no kind prefix or shape validation, which admits the non-hash `platform` and TTL-bucket terms and lets
  this package prepend `B`. The letter is always `Hashing.BuildPrefix` and a config's `Hash` is
  `FilePrefix + HashBytes`, so what this package writes is what `IsValidBuildHash`/`IsValidFileHash` (Store's
  gc/check classifiers, the ext-pin gate) accept by construction. `normalizeExtDeps` validates a pin with
  `IsValidFileHash` at config read.
- [Runtime](../Runtime/CLAUDE.md) — `$BUFA_BUILD_ROOT` is set into every script's env here **and** read by Runtime's
  `defaultBldRoot`, so it round-trips into any nested `bufa` a script invokes. `DirtyBuilder` writes the source tree
  through plain `SrcFS` ops, which works only because cmd/bufa passes `writableSrc=true` for dirty builds.
  `newBuilderBase` folds `rc.RootConfig` into `rootConfigHash` **at construction**, so `RootConfig` must already be
  resolved on the Config handed to `NewBuilder`/`NewDirtyBuilder` (a zero one keys every dir as if the marker had no
  `[env]`). A shell session reaches the cold path only because Runtime's `ForceRebuildFor` folds `ShellSessionFor` —
  the cache-bypass sites here never re-check `BuildMode`.
- [BuildConfig](../BuildConfig/CLAUDE.md) — root `[env]` values are exported and hashed as written, with no re-trim
  or emptiness guard here, on the strength of `RootConfig.Validate` having trimmed and checked every literal
  (pointer receiver). `setAllEnvVars` iterates both `EnvTable`s as-is — document order only because the decode
  gateways' `afterDecode` hook restores it; a table built any other way exports name-sorted, silently. `ShellDef` is
  applied **unchecked** (`Ext` glued onto `BUFA`, `Run` trusted to carry `${script}`, nil `Shell`/`PostShell` read
  as "mode absent") on the strength of `DecodeShellDef`'s `Validate` and the decoder keeping nil distinct from `[]`.
- [FilterFiles](../FilterFiles/CLAUDE.md) — `srcFiltersOverride`'s unanchored `-*.BUFA` depends on
  `normalizePattern`'s implicit `**/` prefix and on `*` matching an **empty** run; `bldFilterForOrNil` returns nil
  rather than `Compile(nil)` because a zero-rule filter excludes everything.
- [ArtifactCache](../ArtifactCache/CLAUDE.md) — the pin doubles as the cache-relative entry path and as link
  identity: dirty `placeExtDeps` recognizes bufa's own placement by comparing a link target to `EntryPath(dep.Hash)`.
  Re-shaping entry names turns every placed dirty artifact into a "foreign symlink" collision in the user's tree.
- [Cache](../Cache/CLAUDE.md) — wrappers apply **outermost-first**, so the newest tier is consulted first and every
  inner-tier hit primes the outer ones through their `Set`. Dirty mode's two stacked skip-hash layers maintain the
  daemon shadow through that mechanic alone; only the stale-shadow direction self-corrects.
- [cmd/bufa](../cmd/bufa/CLAUDE.md) — the build-root ownership allowlist `nuke` and `check` share admits exactly the
  daemon sock beside the six layout roots; the script lives in `tmp/`, so Build writing any file to the build root's
  **top level** makes a post-failure `nuke` refuse and `check` report it. `runInteractiveShell` reads stdin from
  `rc.In`, which only cmd/bufa's `buildMain` assigns — a nil `In` is a legal EOF stdin, so a path that forgets it
  gets a shell that opens and exits at once, with no error.
