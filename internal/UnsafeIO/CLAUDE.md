# internal/UnsafeIO

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- The raw OS-path escape hatches: vfs→OS path translation plus probes on OS paths outside any vfs jail. An
  `UnsafeIO` call site is deliberately loud — it marks code that bypasses the injected fs, so every bypass stays
  greppable. Wrapping the pre-vfs probes in an unrestricted `OsFs` to fake vfs compliance is rejected: it jails
  nothing and hides the bypass. Only prod needs are exported.
- **Path translation (math only, no I/O)**: `GetRealPath` interface for afero wrappers exposing `RealPath`;
  `RealPath(fsys, path)` the **strict** resolver — panics when the fs does not expose it (vfsx's
  `XSymlink`/`XCopyFile` engines and `OSReadlink` resolve through it); `OSPath(fsys, path)` — `RealPath` when the fs
  exposes it, else `filepath.Abs` — so link ops work on rooted wrappers and plain `OsFs` alike (vfsx's
  `Readlink`/`TryLstat`/`Symlink` and Store's link-target absolutization ride it).
- `OSReadlink(fsys, path)` — Readlink through the strict `RealPath`, the loud flavor for Store's index-link reads,
  where `OSPath`'s Abs fallback would turn a mis-wrapped fs into silent absence and GC would collect everything.
- **OS-path probes** (paths outside any vfs — Runtime's srcRoot discovery walk, the minimum ops before any root
  exists to build a vfs on, and Watcher's event probes): `OSFileExists` (**silent** — a dir reads as "no file",
  never panics), `OSDirEntryExists` (Lstat-based any-occupant probe — a symlink counts even dangling),
  `OSTryStat`/`OSTryLstat` (nil on **any** error — Store's `OSConfigExists` and Runtime's split-roots tripwire build
  on them).

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Runtime](../../Runtime/CLAUDE.md) — `OSPath`'s `filepath.Abs` arm is a cwd-relative fallback: when an fs stops
  implementing `GetRealPath`, every symlink read and write silently resolves against the wrong root. Runtime's RO
  src wrapper re-exposing `RealPath` is what keeps that arm unused in production.
- [Watcher](../../Watcher/CLAUDE.md) — the daemon's probes must stay **silent** on squatting directories:
  `OSFileExists` reports a dir as "no file", never panics. Swapping in Store's `OSConfigExists` kills the event loop
  on a dir squatting on `BUFA`/`.BUFA`.
- [internal/vfsx](../vfsx/CLAUDE.md) — vfsx's `Readlink`/`TryLstat`/`Symlink` deliberately ride the Abs-fallback
  `OSPath`, while its `XSymlink`/`XCopyFile` engines ride the strict `RealPath`. Swapping flavors on either side
  silently trades a loud panic for wrong-root resolution.
