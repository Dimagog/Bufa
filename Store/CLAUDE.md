# Store

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Content-addressed directory store over one `vfs.Fs` (Build's `BldFS`): `Store.go`, the `bufa gc` collector
  `GC.go`, the `bufa check` verifier `Check.go`, `Nuke.go`. Public API: `NewStore(fs)`, `Contains(root, hash)`,
  `GetLinkTarget(root, name)` (`""` = absent/not-symlink), `Link(root, name, hash) prev` (creates, reads-then-skips
  when current, **re-points** when the target differs — a `B` key follows its latest publish — and returns the
  previous target so Build can report a re-point), `LinkPath(root, path, hash)` (`Link` on the encoded `∕<path>`
  GC-root name), `Restore(root, hash, dstDir)`, `RestoreLink(root, hash, dstPath)` (one real dir symlink at dstPath
  onto the content tree; dstPath must not exist — `os.Symlink` refuses, self-enforcing the empty-destination
  invariant; link staging for `largeOutput` deps), `HashSrc(srcFs, dir, filter, allowLinks)`,
  `Store(root, srcFs, srcDir, pruneNested, filter, allowLinks)`, `StoreAsHash(…, hash)` (publish under a known
  hash, no re-hash), `MoveStore(root, srcDir, plan PublishPlan)` (destructive same-FS publish, bld→out),
  `SrcPrep(srcFs, srcDir, filter, allowLinks)` (`HashSrc` + `Contains(InRoot)` short-circuit + `StoreAsHash` on
  miss, then refresh `in/∕<srcDir>`), `SrcPrepEmpty(srcDir)` (SrcPrep for a virtual unit: `MkdirAll` the shared
  `in/<EmptyDirHash>` directly — nothing partial to hide, so no tmp-stage — then refresh `in/∕<srcDir>`; srcDir
  need not exist), `GC`,
  `Check`, `EnsureCacheDir(path) name` / `EmptyCacheDirs(out) n` (**Cache dirs**), `VFSConfigExists(fs, path)` /
  `OSConfigExists(path)` (file-exists that **panics on a directory** — `BUFA`/`.BUFA` are reserved, so a dir there
  is a contract violation; used by `skipNested`, `BuildDirPresent`, Runtime's walk — never inside the daemon, which
  uses the silent `UnsafeIO.OSFileExists`), `BuildDirPresent(srcFS, path)` (gc's liveness predicate: real
  `<path>/BUFA` file, or virtual `<parent>/<base>.BUFA` file via the silent `vfsx.FileExists` — a same-named dir is
  a legitimate build root there), `IsReservedName(name)` (bare `BUFA` or `*.BUFA`; the `*.BUFA` half is
  `bufaSuffixed`, skipNested's prune rule), `ShellDefFileName` = `BUFA.shell` and `EnvFileName` = `BUFA.env`
  (deliberately **not** reserved: each must stage and publish like any source file), `Nuke(bldRoot, allowedFiles…)`
  (`RemoveAll` the whole build root only after `ownedTopLevel(allowedFiles)` admits every entry — the six
  `layoutRoots` as directories plus the case-folded `allowedFiles` as non-directories (cmd/bufa passes the daemon
  sock name; a failed build's script lives inside `tmp/`); anything else refuses naming the offenders, dirs with a
  trailing `/`; missing root ⇒ no-op; the caller stops the daemon **and waits** first),
  `NukeDir(friendlyName, dir, owned)` (the verify-then-delete engine on an OS path, I/O through a throwaway
  `BasePathFs`; ArtifactCache's `Nuke` reuses it), `CopyFileIn(srcFs, srcPath, dstPath)` (one regular file in,
  parents created, `Chmod 0644` clearing the read-only marking `CopyFileW` propagates from a cache entry). Which
  vfsx probe each bufa call site uses: `vfsx.Exists` test-only; `vfsx.FileExists` the silent `<suffix>.BUFA` check;
  `vfsx.RegularFileExists` ArtifactCache's hit probe; `vfsx.DirExistsFailOnFile` Build's virtual-dir
  materialization probes and `Contains` (a file squatting on a key is corruption); `vfsx.TryReadSmallFileFast` the
  3-OS-call config read (Build's real-vs-virtual detection; panics on a dir); `vfsx.RemoveDirIfEmpty` MoveStore's
  ext-prune sweep.
- **Filtering, three axes**, all via `filteredSkip(root, pruneNested, filter, allowLinks)` — the one skip closure
  shared by `copyTree` and `HashSrc`, so a staged tree and its hash never diverge: `pruneNested` applies `skipNested`
  to **directories**; `filter` applies `[filters]` to regular files **and symlinks** (a link is a leaf entry filtered
  by its own path; never prunes dirs); `allowLinks` is the symlink policy (`Doc/Bugs/SymlinkHandling.md`) — false
  hard-fails on any symlink child with the declare-via-`[[deps.ext]]`-or-`unsafe` hint, true treats links as
  leaves. The filter is consulted **before** the policy, so only a link that would actually stage can fail a safe
  build. `filter == nil` ⇒ verbatim (empty dirs kept, links preserved), legal only with `pruneNested=false` +
  `allowLinks=true`. Usage: src ⇒ `pruneNested=true` + composed `[filters].src` + `allowLinks=cfg.Unsafe`; bld
  output ⇒ `pruneNested=false` + `[filters].bld` (nil when empty); `Restore` ⇒ verbatim.
- `skipNested` prunes a subdir that is a nested build unit (`BUFA`), a nested source-root marker (`.BUFA`), or a dir
  declared virtual by a sibling `<name>.BUFA` (matters only for a dirty-materialized dir). **Any** `*.BUFA`-suffixed
  *directory* (bare `.BUFA` included) is a **hard error** with the leftover-build-root hint — after a source root
  relocates upward, the old `<base>.BUFA` store sits inside the new tree, and silent pruning would hide the poison;
  only directories are reserved. A directory named `BUFA` panics (`VFSConfigExists`) unless the package var
  `AllowBufaDir` is set (Build sets it once from the marker's `allowBufaDir`; gc never does).
- Exported layout constants: `InRoot="in"`, `OutRoot="out"`, `BldSandboxRoot="bld"`, `DirtyRoot="dirty"`,
  `UserRoot="user"`, `TmpRoot="tmp"`, `BuildConfigName="BUFA"`, `BuildRootDirSuffix=".BUFA"` (`C:\Proj` →
  `C:\Proj.BUFA`), `SrcRootFileName=".BUFA"` (the marker: the bare, empty-suffix member of the `*.BUFA` namespace,
  so no dir can declare a virtual config colliding with it), `VirtualConfigSuffix=".BUFA"` (`clj.BUFA` → virtual dir
  `clj`), `GitRootMarker=".git"` (dir **or** file), `PathLinkPrefix="∕"` (U+2215). `BuildRootDirSuffix`,
  `VirtualConfigSuffix`, and `SrcRootFileName` are distinct naming choices whose values coincide — don't collapse
  them.
- **Dirty skip hashes** (`dirty/`): `GetDirtySkipHash(path)` / `SetDirtySkipHash(path, skipHash)` — one regular file
  per build dir named `∕<path>`, holding the skip hash of the last successful dirty build. Pure skip cache: no store
  content, no symlinks; a failed script leaves the old value.
- **Cache dirs** (`user/`, `Doc/Specs/CacheDir.md`): the persistent, script-written cache dir of every build dir
  with `deps.cacheDir = true`. One entry is a **pair** derived from the slash-form root-relative path: the dir
  `user/P<HashPath(path)>` (`cacheDirName` — plain ASCII, the value scripts see; `P` never collides with `∕`;
  classified by full key shape `Hashing.IsValidPathHash`) plus the `user/∕<path>` symlink onto it (attribution to
  its build dir, like the other roots' GC links). `EnsureCacheDir(path)` returns the bare `P…` name: one `TryLstat`
  — a squatting non-directory (file or symlink) is a hard error — absent ⇒ `MkdirAll`, then
  `LinkPath(UserRoot, path, P…)` (re-points a mispointed `∕`). Content is **never** hashed, staged, published,
  inspected, or wiped by bufa. `EmptyCacheDirs(out)` (`nuke --cache-only`): each `P…` dir gets
  `vfsx.RemoveAllUnder` (the dir and its `∕` link stay), `∕` links are skipped, foreign names are reported
  (`UNEXPECTED ENTRY: 'user/<name>' left untouched (run 'bufa check' to inspect)`) and left — no refusal, unlike
  `Nuke`. Missing `user/` ⇒ 0.
- Virtual-config bijection, owned here: `VirtualConfigPathForDir(srcDir)` (`""` only at the source root) and
  `VirtualSuffix(name)` (`""` for a name without the suffix — bare `BUFA` is shorter — and for bare `.BUFA`; a real
  suffix is never empty). Shared by Build's `locateBuildConfig`, gc, and Watcher so encode and decode can't drift.
- **Layout**: `in/<D…>/` & `out/<D…>/` content trees (key = `HashDir`); index symlinks `out/B<combined>` → `D`;
  mutable GC roots `in/∕<path>` → `D` directly, `out/∕<path>` → `B<combined>` → `D` (the two-hop chain lets GC
  reclaim a dir's superseded `B` links even when the `D` survives via another dir), `user/∕<path>` → `P…`. A
  `∕<path>` name is the ToSlash'd path with each `/`→`∕`, **casing preserved** (GC stats the decoded path; a folded
  name would miss a real dir on a case-sensitive FS and collect its live root; root `.`→bare `∕`);
  `pathToLinkName`/`pathFromLinkName`. gc/check/`EmptyCacheDirs` classify `D`/`B`/`P` names by **full key shape**
  (`IsValidDirHash`/`IsValidBuildHash`/`IsValidPathHash`; a prefixed non-key is foreign), only `∕` by its leading
  char. `tmp/` is the single on-demand scratch dir shared by Store staging and Build's temp redirect — every user
  recreates it before use.
- **Real OS directory symlinks**: all symlink ops use the **strict** flavors (`vfsx.XSymlink`, `UnsafeIO.OSReadlink`),
  which hard-require a `RealPath`-resolving `s.fs` — a mis-wrapped fs fails loud instead of reading links as absent.
  `Link` writes through `linkTo` (both sides absolute OS paths, so Windows makes a dir symlink; target always
  published before linking); creating links needs symlink rights, reading/removing does not. `GetLinkTarget`
  `filepath.Base`s the readback to the bare key. `Link` re-points via `removeLink` (a plain `Remove` of the link
  object) + re-create, since `os.Symlink` won't overwrite; unchanged ⇒ one readback, no write.
- `Store`/`StoreAsHash` share `stage` (`RemoveAll(tmp)` → `copyTree`) and `publishTmp` (`Contains`? discard : atomic
  same-fs `Rename`; lost race ⇒ discard). `MoveStore` never stages: it filters `srcDir` **in place**
  (`filterInPlace`) in a **read-only scan** (`filterScan.scanDir` over `vfs.ReadDir`, collecting the delete set) and
  an `apply` pass that mutates only after the whole scan passed — every scan-time error (symlink policy, kept-link
  validation) fires before any deletion, so a failed publish leaves the kept-for-debug sandbox as the script left
  it. Scan: excluded files/links → `Remove`; a path in `PruneDirs` (Build's nested staged-dep targets minus
  `deps.export`) or `ExtPrune` (non-exported `[[deps.ext]]` names) — both srcDir-relative slash-path sets,
  **kind-blind**: a directory → `RemoveAll`, a symlink → `Remove`d as a link object by set membership (never
  followed, so a link-staged dep's store tree survives), a file → `Remove`d; unmatched keys are harmless. Dirs left
  empty drop only in a deleting pass (filter or `PruneDirs`); `ExtPrune` instead sweeps each pruned name's parent
  chain via `vfsx.RemoveDirIfEmpty`, so a verbatim publish keeps unrelated empty dirs. Symlink policy: a publishing
  link must be in `KeepLinks` (Build's `large && export` ext names) or waived by `AllowLinks` (unsafe), else panic;
  every kept link is validated at scan time (`checkKeptLink`): dangling ⇒ hard error; a target inside the moving
  tree or the bld sandbox — literal against the literal root, `EvalSymlinks`-resolved against the resolved root
  (so a root behind a symlink or an 8.3 short name still matches; roots resolve lazily, on the first kept link) — is
  rejected (it would dangle after the rename/wipe). A kept link with a relative target resolving **outside** the tree
  is re-pointed absolute before hashing; an intra-tree relative target survives verbatim. Then `HashDir` the pruned
  tree and rename the whole dir into `root/hash`. The scan is **unconditional** (even a verbatim unsafe publish can
  hold a script link needing validation); with a nil filter and no prunes it deletes nothing. `PublishPlan` (`Filter`,
  `PruneDirs`, `ExtPrune`, `KeepLinks`, `AllowLinks`) is the whole publish policy as one value — a new axis is a
  field, not a signature sweep; zero value = verbatim safe publish.
- `copyTree` recurses through `copyDir`, which reads each directory once via `vfs.ReadDir` and **reuses that
  FileInfo** (like `HashDirFiltered`) — not `afero.Walk`, which re-`Lstat`s every entry. Copy delegates to
  `vfsx.XCopyFile` (`CopyFileW` under `vfsx.UseCopyFileOS`, both stores exposing `RealPath`; else vfs-level `O_EXCL`
  stream copy). A non-nil skip predicate creates dirs **on-demand** (only when an included file or kept link lands in
  one, matching `HashDirFiltered`'s empty-dir prune); nil copies verbatim. A symlink is **never followed**: a kept
  link (even dangling — the hash walk is where danglers fail) is cloned as a link object by `copyLink` with its
  target rewritten absolute (a relative target would re-resolve against the destination), via `resolveLinkTarget` —
  shared with `checkKeptLink` so the publish scan validates exactly what the copy writes. Other non-regular entries
  are skipped.
- **GC** (`GC(srcPresent, reportFreedSpace) GCStats`): `gcContentRoot` over `in/`, `out/` (with the ∕→B→D tier), then
  `user/`, each on one `listNames` snapshot: decode each `∕<path>`, `removeLink` the stale ones (`!srcPresent(path)`,
  `StalePathLinks`), collect survivors' targets; in `out/` walk the `B` entries, remove any not live
  (`OrphanBuildLinks`), resolve the rest to `D`; then `RemoveAll` every content entry not live (`OrphanContent` for
  in/+out/, `OrphanCacheDirs` for user/ — the one moment bufa deletes cache-dir content, and only because its build
  dir is gone; a live entry is never opened, and attribution is by dir presence — a dir that dropped `deps.cacheDir`
  keeps its entry). `srcPresent` is injected (Store stays src-FS-decoupled); the CLI passes `BuildDirPresent`.
  `dirty/` (`gcDirtyRoot`): every skip hash whose path fails `srcPresent` is removed (`StaleDirtySkipHashes`). Last,
  the **scratch roots** `bld/` and `tmp/`: a successful build removes both, so either present is a failed/interrupted
  build's or a shell session's leftover — `RemoveAll`ed whole (`LeftoverScratchDirs`; a directory only — a non-dir
  squatter is check's business; `RemoveAll` never follows the link-staged deps inside). Missing roots are no-ops.
  **Freed bytes** (`reportFreedSpace`): whole-tree removals go through the `removeTree` closure — the measured
  variant runs `treeSize` first, a recursive walk summing **regular files only** (a symlink counts 0); link removals
  and skip hashes are never measured. Anything wrongly collected is re-staged/re-built next build.
- **Check** (`Check(out, fix, allowedFiles…) CheckStats`): read-only unless `fix`; one `checkContext` (store, fix,
  out, stats) whose methods carry mode, writer, and counters. `checkTopLevel` judges the build root's own listing by
  `ownedTopLevel(allowedFiles)` — the very predicate `Nuke` refuses on (cmd/bufa passes the same sock name) — so a
  stray `target/` beside `in/` is `UnexpectedEntries`; the layout roots' contents are the later passes' (`bld/`,
  `tmp/`, `dirty/` never entered). `checkContentRoot` over `in/` then `out/`: content by full `IsValidDirHash` shape,
  `out/` build links by `IsValidBuildHash` (exactly gc's classification — a prefixed non-key is `UnexpectedEntries`,
  never corrupt content). Pass 1 resolves every `∕` (and out/'s `B`) link against the enumeration, no extra FS ops
  (wrong kind / dangling ⇒ `BadLinks`; a non-symlink on a link name ⇒ `UnexpectedEntries`); out/'s `B` tier resolves
  before the `∕` tier so the ∕→B→D correlation never depends on enumeration order. Pass 2 re-hashes every content dir
  against its name (`SafeHashing` contract-asserted at entry; the re-hash runs under `contract.Rescue`, so a dangling
  or cycling symlink reports "cannot be hashed" and verification continues) — `CorruptContent`, correlated back to
  the source dirs whose ∕ links reference it. `checkUserRoot` checks the **shape** of `user/`, never content: a `P…`
  directory, and a `∕` symlink whose target is `cacheDirName(pathFromLinkName(name))` and present as a dir; anything
  else is `BadLinks`/`UnexpectedEntries` (an unlinked `P…` is gc's job). Problems print one line each to `out`;
  returns `CheckStats{CheckedContent, CheckedLinks, CorruptContent, BadLinks, UnexpectedEntries, RemovedEntries}`
  with `Ok()`. `fix` deletes each corrupt entry plus every index link referencing it (`fixCorrupt`, links first so a
  mid-fix crash leaves an orphan for gc), **every bad link** (a dangling one would serve a phantom hit —
  `GetLinkTarget` never stats the target), **and every unexpected entry** (`RemoveAll`): one policy for every root —
  check reports whatever isn't a well-formed bufa entry, fix removes all of it, so `checkMain` passes iff
  `fix || Ok()`. A deleted `B` makes its ∕ referrers dangling, caught in the same run via the `removed` set. The
  caller must stop the daemon first. `dirty/` is not checked. Missing roots are no-ops.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Hashing](../Hashing/CLAUDE.md) — a `D…` key **is** an entry's on-disk name, so the key encoding is a filesystem
  naming constraint (case-insensitive volumes), and `Check`'s re-hash-against-the-name pass is meaningful only while
  every entry name is its **true** content hash — which `StoreAsHash`/`SrcPrep`'s caller-supplied key can violate.
  Hash walks fold a hash-named link's target basename instead of reading through it, so a wrong key silently becomes
  the hash of every tree linking to it; hence `Check` asserts `SafeHashing`. `filteredSkip` is shared by `copyTree`
  and `HashSrc`, and the filtered walk's empty-dir prune is paired with `copyTree`'s on-demand dir creation, so a
  staged tree and its hash cannot diverge.
- [Build](../Build/CLAUDE.md) — `Restore` requires an empty-or-absent destination, and a `RestoreLink`ed
  `largeOutput` dep must be a staging **leaf**: restoring below it writes *through* the dir symlink into store
  content. Build's ancestors-first sort and `Util.StrictlyBelow` scan exist only for that; relaxing it here removes
  the constraint they enforce with silent corruption as the failure mode. `PublishPlan`'s
  `PruneDirs`/`ExtPrune`/`KeepLinks` arrive as bare paths — `PruneDirs` already minus `deps.export`; Store stays
  ignorant of what an ext or exported dep is. Build's `storeSetBuildHash` counts on `Link` **re-pointing** a present
  `B<combined>` and on its returned previous target for the "not reproducible" warning; reverting either breaks only
  Build's tests. `EnsureCacheDir(path)` must be keyed by the **same** root-relative srcDir Build hands `LinkPath` —
  the `user/∕` link decodes through gc's shared `srcPresent`, and the exported `P…` value is a pure function of that
  path. `ShellDefFileName` and `EnvFileName` must stay **outside** `IsReservedName`: Build's `srcFiltersOverride` and
  `cleanLocalName` lean on it, so reserving either would silently drop a provider's definition or env exports from
  its own staged source (an absent `BUFA.env` is a legal no-op).
- [Watcher](../Watcher/CLAUDE.md) — inside the daemon, config existence probes must be the **silent**
  `UnsafeIO.OSFileExists`, never the panicking `OSConfigExists`/`VFSConfigExists`: a directory squatting on
  `BUFA`/`.BUFA` would kill the event loop instead of being reported by the build side. `out/∕<dir>` and
  `dirty/∕<dir>` have FS-free daemon shadows (`pathBuildHashCache`, `dirtySkipHashCache`) of disk state the watcher
  can never observe, so an out-of-band writer must stop the daemon first — a stale shadow that **matches** wrongly
  skips a build, a stale absence merely costs a readback.
- [ArtifactCache](../ArtifactCache/CLAUDE.md) — a `large` + `export` ext dep ships into `out/<D…>` as a symlink
  **object** whose target lives outside every root Store manages. Such a tree stays valid only because the global
  cache is immutable, never evicts, and `bufa gc` never touches it — GC, Check, and the publish policy treat those
  targets as durable.
- [Runtime](../Runtime/CLAUDE.md) — every symlink op hard-requires `s.fs` to resolve `RealPath`, and vfsx's
  `CopyFileW` engine requires the source fs to forward it. Runtime builds the FS pair, so wrapping `BldFS` or the src
  half in any afero decorator that hides `RealPath` panics inside Store on every link and OS copy.
- [FilterFiles](../FilterFiles/CLAUDE.md) — `filteredSkip` rebases each child to the build unit's own dir
  (`Rel(root, path)` + `ToSlash`) before `Filter.Included`, so a `/`-anchored rule anchors **there**, not at the
  source root. The filter is consulted on regular files and symlinks only; directories are `skipNested`'s call
  alone. A nil `*Filter` is the verbatim sentinel — a zero-rule compiled filter would exclude everything.
- [internal/Util](../internal/Util/CLAUDE.md) — `AtOrBelow`/`StrictlyBelow` **panic** on unrelatable paths
  (different Windows volumes); `checkKeptLink` must use `AtOrBelowIgnoreUnrelated`, or a legal cross-drive symlink
  target crashes.
- [internal/contract](../internal/contract/CLAUDE.md) — `Rescue` is a **production** panic-downgrade primitive here
  despite its doc comment saying "for tests": `Check` wraps each entry re-hash in it so verification continues past
  a dangling or cycling symlink.
- [internal/vfsx](../internal/vfsx/CLAUDE.md) — `XCopyFile`'s stream arm opens through the fs values (this package's
  MemMapFs tests depend on it), while its `CopyFileW` arm hard-requires `RealPath` on both stores; re-pathing either
  arm breaks the other consumer silently.
