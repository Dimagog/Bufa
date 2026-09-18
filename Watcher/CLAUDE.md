# Watcher

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Recursive watch (`github.com/rjeczalik/notify`) over the source tree, exposed as an on-demand UDS RPC daemon via
  `Daemon.Serve`. Public API: `Serve(sockPath, srcDir, idleTimeout)` + the exported RPC arg/reply types; cache value
  types live in BuildConfig. Only the RPC methods are exported (`Get`/`Set` × SrcHash, DirtyHash, DirtySkipHash,
  BuildConfig, BuildHash, RootConfig, plus `SetPathBuildHash`, `TryStartNameServer`, `Shutdown`). `rjeczalik/notify`
  over `fsnotify` because fsnotify's recursive backend is private (`enableRecurse=false`); its behavior is pinned by
  the gated `TestNotify_*` probes (`BUFA_NOTIFY_BEHAVIOR=1`). Tests start watchers via `settledWatcher`: a probe
  watch first waits for the fixture's setup writes to be committed, because FSEvents (macOS) assigns event ids
  asynchronously and otherwise replays them into the new stream as live Creates (a nested `BUFA` create clears
  every cache; a `.git` create shuts the daemon down).
- **Caches** (one `sync.RWMutex`; keys via `Util.NormalizePath`): `srcHashCache`, `dirtyHashCache`,
  `buildConfigCache` — build-dir-rel keyed, same lifetime, evicted together. `buildHashCache` combined-hash → build
  hash, content-addressed, never FS-invalidated. `pathBuildHashCache` (the combined hash `out/∕<dir>` last pointed at)
  and `dirtySkipHashCache` (`dirty/∕<dir>` skip hash) — FS-free **shadows** of disk state, written only by their Set
  RPCs, never event-invalidated, default-absent ⇒ the client reads disk (a stale absence costs a readback; a stale
  match would wrongly skip, so an out-of-band writer stops the daemon first). `rootConfig *RootConfig` (nil ⇒ not
  cached; the zero value is legitimate). `owningDirCache` changed-dir abs → owning build-dir-rel (`"."` when none).
- **Two watchpoints**, each on its own channel: recursive `srcDir/...` → `invalidatePathCaches`; non-recursive on the
  parent for Remove|Rename of srcDir itself → `checkBaseGone` (skipped at a filesystem root). **Caveat**: notify
  consolidates both into one OS watch at the parent and delivers a srcDir-self event to the subtree channel too, so
  `invalidatePathCaches` routes any `EqualFold`-srcDir path to `checkBaseGone` before taking `w.mu` — an out-of-tree
  path in the owner walk never terminates (`filepath.Dir` is a fixed point at the volume root) and would hang every
  RPC holding the lock. `resolveOwningDir` also clamps a non-`RelStrictlyBelow` path (e.g. an 8.3 short name) to
  `"."`.
- **Invalidation**: `filepath.Dir(p)` → `owningDirCache`; on miss walk up, probing each ancestor for `BUFA` and for a
  sibling `<base>.BUFA` (a dirty-materialized virtual dir has no `BUFA` of its own), memoize, then drop the owner's
  src/dirty/config entries. A `BUFA` create/remove/rename clears those three caches plus `owningDirCache` (graph
  change); a `BUFA` write is a plain content change. A `<suffix>.BUFA` event drops the virtual dir's own entries
  (`<parent-rel>/<suffix>`); on create/remove/rename it also `deleteSubtree`s `owningDirCache` under the declared dir
  and **falls through** to the owner invalidation, which is required: the declaration flips `skipNested` on the dir it
  names, so the owner's staged source gains or loses that subtree. A `Create` also exact-deletes `rel(p)`'s entries —
  a real dir at a cached **virtual** path must reach the cold path's must-not-exist check. `pruneMovedDirCaches`
  (Remove|Rename) prefix-prunes every path-keyed cache at or below the moved dir (`buildHashCache` untouched).
- **Self-shutdown** (`triggerShutdown`, idempotent, closes the `Daemon.Serve` listener): ① srcDir-self Remove|Rename —
  `checkBaseGone` fires on an `EqualFold` match **or** an existence probe finding srcDir gone (a rename can surface
  only under the new name); ② `.BUFA` marker Create|Remove|Rename — root identity changed; ③ `.git` Create|Remove|
  Rename anywhere (the entry itself; events inside `.git/**` have other basenames); ④ the root's **own** `BUFA`
  Remove|Rename (an unanchored root's identity dissolves). A root `BUFA` create or any nested `BUFA` event cannot
  change the topmost answer and takes the clear-cache path. A `.BUFA` marker **write** only nils `rootConfig`.
- RPC shapes: `GetBuildConfig` bundles config + staged-source hashes for the path and each `Deps.Src` (miss ⇒
  `Found==false`); `GetSrcHash` returns `""` on miss; `GetRootConfig` stamps `Bufa.Version` into `BufaVersion` — the
  daemon is passive in the version check, the client restarts. `Serve` = `newWatcher` + `tryStartNS`/`closeNS`
  (best-effort global NS claim) + `Daemon.Serve`. `tryStartNS` has **no** own-NS short-circuit: after an external
  `NameServer.Stop` the held `nsCleanup` is spent and a takeover must re-claim; `tryListen`'s dial-probe prevents a
  double claim (seam: `nsTryStart`). `Shutdown` closes the listener after 50ms so the reply flushes.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [DaemonClient](../DaemonClient/CLAUDE.md) — **untyped** contract: method-name string literals over gob-encoded
  structs (`cmd/daemon-client` hard-codes two of them). A rename or reshape compiles on both sides and degrades to a
  miss or a dropped write. The `BufaVersion` stamp is the guard: a mismatch (incl. `""` from an older daemon) makes
  the client Restart-cycle the daemon, so every `version.txt` bump retires stale gob shapes.
- [Store](../Store/CLAUDE.md) — the owning-dir walk uses the **silent** `UnsafeIO.OSFileExists` for both probes,
  never the panicking `OSConfigExists`/`VFSConfigExists`: a squatting directory must not kill the event loop. The
  `<dir>` ⇄ `<parent>/<base>.BUFA` bijection is Store's (`VirtualConfigPathForDir`/`VirtualSuffix`), so the event
  decode can't drift from Build's and gc's encode.
- [Build](../Build/CLAUDE.md) — evicting the whole owning dir's `buildConfigCache` on any file event is what keeps
  `[env] file =` sources and `[[deps.ext]]` names strictly local (`readBuildConfig` bakes the bytes and the resolved
  url); narrowing here or relaxing locality there serves stale configs. A marker write nils **only** `rootConfig`,
  which is why Build never bakes the marker's `[env]`, `shell` default, or `[shells]` into a dir's config — it reads
  them live. A provider's `BUFA.shell` is an ordinary file of its build dir; owning-dir eviction covers it.
- [Runtime](../Runtime/CLAUDE.md) — `BldRoot` is the sibling `<base>.BUFA`, so the store and this socket sit
  **outside** the watched tree; everything here assumes no store write is ever seen. `Serve` claims the NameServer
  **before** `Daemon.Serve` listens; reordering makes Runtime's post-`Connect` back-fill race the listener.
- [NameServer](../NameServer/CLAUDE.md) — entries are never revalidated, so the only invalidations are this daemon
  exiting and `NameServer.Stop`; that is why a marker Create|Remove|Rename shuts down rather than nil-ing
  `rootConfig`. An external `Stop` leaves `nsCleanup` non-nil but spent — why `tryStartNS` must not short-circuit.
- [Daemon](../Daemon/CLAUDE.md) — the `stop` func closes the listener only, not open connections; hence `Shutdown`'s
  50ms sleep.
- [internal/Util](../internal/Util/CLAUDE.md) — `NormalizePath` is not canonicalization (no `Clean`, no absolutize):
  keys must arrive as a `Rel` result, or two spellings of one dir miss each other.
- [BuildConfig](../BuildConfig/CLAUDE.md) — cache value types live there (no Build↔Watcher cycle); gob-encoded, so a
  shape change needs `bufa daemon reset`, and a miss is signalled by `…Reply.Found`.
- [cmd/bufa](../cmd/bufa/CLAUDE.md) — `Serve(sockPath, srcDir, idleTimeout)` is reached only through the positional
  self-spawn argv outside the kong grammar; the production **1h** idle default lives there (Daemon's reference binary:
  5s).
