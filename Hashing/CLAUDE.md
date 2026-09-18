# Hashing

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Public API: `HashFile(fs, file)`, `HashBytes([]byte)` (the digest a file with those bytes would get — BUFA
  content), `HashUrl(url)` (`"U" + HashBytes` — the `_U<hash>` tail of ArtifactCache's trimmed overlong-url links; a
  binding prefix, never a content key), `HashPath(path)` (`"P" + HashBytes` — Store's `user/P…` cache-dir entry is
  `HashPath` of the slash-form build-dir path; a path-derived key, never a content key), `HashDir(fs, dir)`,
  `HashDirFiltered(fs, dir, skip)`, `HashLink(fs, path)`, `Hash(fs, path)` (dispatch file vs dir vs symlink — it
  Lstats its argument, so `bufa hash <dangling-link>` names the link and its raw target),
  `CombineHashes[T]`/`CombineHashesInPlace[T]` (fold `HashEntry` values into one digest — **no** kind prefix, the
  caller prepends; InPlace sorts the caller's slice), `EmptyDirHash` (the `"D…"` key of an empty tree, exported so
  Store can publish the shared empty-source entry for virtual units). `NamedEntry{Name, Hash}` is the stock
  `HashEntry`. `FileHasher` streams the `F…` digest for callers hashing while writing (ArtifactCache verifying a
  download). `IsValidFileHash`/`IsValidDirHash`/`IsValidBuildHash`/`IsValidUrlHash`/`IsValidPathHash` are exact
  full-shape checks — the **one** classifier: prod code never tests a key by `strings.HasPrefix` on its letter, so a
  prefixed non-key is a foreign name everywhere. The letters are the exported consts
  `FilePrefix`/`DirPrefix`/`BuildPrefix`/`UrlPrefix`/`PathPrefix`, the only place they are spelled. `SafeHashing` is
  the global bool disabling the trusted-link shortcut.
- BLAKE3 256-bit, lowercase RFC 4648 base32, no padding. Dir hash = `"D"` + BLAKE3 of the canonical manifest:
  children sorted by name, each `"<hash>\t<name>\n"` concatenated, built on `CombineHashesInPlace`.
- `HashDirFiltered`: `skip` is evaluated on **children only** (root never passed); `true` omits the child and its
  subtree. With `skip != nil`, a child dir whose filtered subtree is empty is omitted entirely (its hash equals
  `EmptyDirHash`) — paired with `Store.copyTree`'s on-demand dir creation so hash == staged tree. `skip == nil` keeps
  empty dirs. Store supplies the BUFA skip rule; Hashing stays BUFA-agnostic.
- **Symlinks — the leaf rule** (`Doc/Bugs/SymlinkHandling.md`): a symlink is a leaf named entry whose content is read
  through the referent — a file link hashes as the referent's bytes, a dir link as the whole referent subtree,
  recursed **verbatim** (`dirSkipFn` never descends into a referent: a linked subtree contributes whole or not at
  all) — so a linked tree keys identically to its materialized twin. A dangling link is a hard error naming link
  and target; a link cycling back to its own ancestor is a hard error via the `viaLinks` stack of Stat-through
  identities (`os.SameFile`) — a diamond stays legal, hashed twice. The empty-dir prune applies to real dirs only: an
  empty-referent link entry is kept (the staged link object survives the copy).
- **Trusted-link shortcut** (`hashLink` via `getHashFromSymlink`): a link whose raw target's basename is a content
  key of the referent's kind folds that basename as the entry's hash — zero content reads, byte-identical to the
  streamed key wherever the name is truthful (bufa's store and artifact cache entries are named by their verified
  hash), so a `large` cache link costs a Stat + Readlink per walk. The existence check is never elided. Global
  `SafeHashing` disables the fold (`--safe-hashing`); `checkMain` sets it and `Store.Check` asserts it — a lying
  hash-named link would fold to the very key under verification.
- **Parallel walk** (`hashDir`): per dir, one [internal/Util](../internal/Util/CLAUDE.md) `ForkJoin[NamedEntry]` —
  `Pool` a `RoutinePool` of `Util.DefaultParallelism` the public entry creates and threads down `hashDir`/`hashLink`
  as a parameter (never a package var); `Context` the level above's ctx; `Capacity` the entry count (`0` legal).
  Regular files go to `fj.Go`; sub-dir recursion and symlinks stay synchronous on the walker (depth-first) and enter
  via `fj.AddResult`; the name sort makes completion order irrelevant. The cap is per walk: an ancestor's in-flight
  files and a child's share the one pool's slots. Deadlock-free only because pool workers run nothing but
  `HashFile` — no slot holder ever waits for a slot. Failure: a file goroutine's contract panic is rescued by ForkJoin
  and recorded as the first error; the loop `c.Checkf(fj.Err(), …)`s before every entry (the one site adding dir
  context). Stop propagates **down the ctx tree**: each level is built on `fj.Context()` of the level above, and
  ForkJoin's error cell is that ctx, so a parent's failure — or an outside cancel — stops a child at its next entry;
  a child's failure reaches the parent only by unwinding. The public entries start from `context.Background()`. A
  walker-side failure unwinds every level; each level's `defer fj.Cleanup()` reaps its in-flight hashers and
  re-panics, so the caller sees one rescuable panic and no goroutine leaks. Guards:
  `TestHashDir_FileFailureIsRescuable`, `TestHashDirFiltered_LoopPanicReapsInFlightHashes`, `TestEmptyDirHash_Constant`,
  `TestHashDir_CancelReachesChildWalk`, `TestHashDir_OutsideCancel`, `TestHashDir_PoolCapsAcrossLevels`.
- Panic-based (contract), walks an injected `vfs.Fs`. CLI: `cmd/hash-files <path>`.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Store](../Store/CLAUDE.md) — a `D…` key **is** the on-disk directory name under `in/`/`out/`: `bufa check`
  re-hashes every content dir against its name, gc and check recognize entries by the full `IsValidDirHash`/
  `IsValidBuildHash` shape, and `EmptyDirHash` is written as a literal dir name. So lowercase base32 is a
  case-insensitive-filesystem requirement, and a manifest or alphabet change marks every existing store corrupt. A
  store entry's name must always be its **true** content hash — the trusted-link fold reads the name instead of the
  bytes — which is why `Check` asserts `SafeHashing` and why `StoreAsHash`/`SrcPrep`'s caller-supplied key is
  load-bearing. The filtered walk's empty-dir prune is paired with `Store.copyTree`'s on-demand dir creation, and
  the walk never applies `dirSkipFn` below a symlink referent. Store's `user/` root classifies entries with
  `IsValidPathHash` and names every cache dir `HashPath(<slash-form path>)` on disk — a prefix or encoding change
  silently orphans every entry.
- [Build](../Build/CLAUDE.md) — `CombineHashes*` folds `NamedEntry` as **opaque** strings: no kind prefix emitted, no
  shape validation, `\t`/`\n` separators. That is what lets Build fold non-hash terms (`platform`, the TTL bucket)
  into the primary key and prepend its own `B` letter; an `IsValid*` assertion here would break those terms. Build
  prepends the exported `BuildPrefix`, so the key it writes is what `IsValidBuildHash` accepts by construction.
  `normalizeExtDeps` validates a pin with `IsValidFileHash` at config read.
- [BuildConfig](../BuildConfig/CLAUDE.md) — the `F…` shape is a **user-authored config value**: a
  `[[deps.ext]] hash =` pin is literally `HashFile` output. Adjusting the encoding edits a file format users have
  committed.
- [internal/Util](../internal/Util/CLAUDE.md) — `hashDir` lives inside `ForkJoin`'s main-thread contract: `Go`,
  `AddResult`, `Results` only from the walker, `defer fj.Cleanup()` deferred **directly**, one instance per dir with
  `Capacity(len(dirEntries))` — so `0` must stay a legal capacity — and nested instances created from the walker
  while the parent's workers hold slots, all on one pool, so a Hashing worker must never call `hashDir`/`hashLink`
  (a slot holder waiting for a slot deadlocks at depth ≥ NumCPU). Each child is built on `fj.Context()` of its
  parent: that ctx **is** ForkJoin's error cell and the whole of Hashing's downward stop rule. A file-hash panic is
  safe only because ForkJoin rescues task panics.
- [ArtifactCache](../ArtifactCache/CLAUDE.md) — cache entries are named `F<verified content hash>`, the invariant
  the trusted-link shortcut rests on. `HashUrl` supplies only the `_U<hash(url)>` tail of trimmed overlong-url link
  names; a url-name format change orphans every recorded url binding (one re-verifying download each).
