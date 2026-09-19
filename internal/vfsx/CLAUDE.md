# internal/vfsx

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- Extends afero with generic fs helpers plus the os-level symlink, lstat, and copy ops vfs cannot express; all other
  prod file I/O goes through `vfs.Fs` methods (repo-wide rule in the root CLAUDE.md). Path translation and raw
  OS-path probes live in [internal/UnsafeIO](../UnsafeIO/CLAUDE.md). Out of scope by design: UDS socket lifecycle
  and process/env APIs. Only prod needs are exported — tests build fixtures with raw `os.*`.
- **vfs-path ops** (translate via `UnsafeIO.OSPath`, then `os.*`): `Readlink(fsys, path)` (raw target; error
  returned so callers pick panic vs best-effort), `TryLstat(fsys, path)` (error returned; fallback chain: afero's
  `Lstater`, then `os.Lstat` over `OSPath` for rooted wrappers exposing only `GetRealPath`, then plain `Stat` — a fs
  with neither, MemMapFs, cannot hold symlinks, so the two agree), `Lstat` (panic wrapper),
  `DirEntryExists(fsys, path)` (Lstat-based any-occupant probe — a symlink counts even dangling; Build's ext-dep
  occupancy checks,
  ArtifactCache's staging-leftover probe, nuke's pre-checks), `Symlink(fsys, linkPath, target)` (target text written
  **verbatim** — it is not a vfs path; `os.Symlink` picks file vs dir flavor from the referent and refuses an
  occupied linkPath). No write-op wrappers beyond links and copies: ordinary writes go through the fs value itself —
  dirty mode gets a **writable** `SrcFS` from Runtime, never a read-only escape hatch here.
- **Generic vfs conveniences** (plain `Stat`/`Open` wrappers; which bufa call site uses which is mapped in
  [Store/CLAUDE.md](../../Store/CLAUDE.md)): `Exists` (kind-agnostic, follows symlinks), `FileExists` (not a
  directory), `RegularFileExists`, `DirExistsFailOnFile` (a non-directory at path **panics**),
  `ReadSmallFileFast`/`TryReadSmallFileFast` (single open + one generous-buffer read + close — no size-probe Stat, no
  trailing EOF read, so a small config costs exactly 3 OS calls; the Try flavor returns nil on not-exist incl.
  `ENOTDIR`, a present empty file is non-nil, and a directory panics "Expected a file" — classified on the
  Read-error path so the happy path stays 3 calls), `ReadDirIfExists` (name-sorted infos; nil for a missing dir,
  any other error panics — Store's `listInfos` and ArtifactCache's `Check`; not for a caller that must tell missing
  from empty, like `NukeDir`), `RemoveDirIfEmpty` (one `Readdirnames(1)` probe; missing / non-dir / surviving
  entry ⇒ false untouched; vfs-based so the deletion stays jailed to the caller's rooted fs),
  `RemoveAllUnder` (`RemoveAll` of every child by name, symlinks as link objects, keeping the dir; Store's
  `EmptyCacheDirs`).
- **Cross-store ops (X-prefix, `(fs1, path1, fs2, path2)` shape)**:
  `XSymlink(targetFs, targetPath, linkFs, linkPath)` (link parents created; **strict** — both sides resolve via
  `UnsafeIO.RealPath`, no Abs fallback, so a link op on a fs without real paths fails loud; Build's `large` ext
  staging/placement and Store's same-fs `symlinkAt`), `XCopyFile(srcFs, srcPath, dstFs, dstPath, mode)` (one
  regular file; `CopyFileW` when `UseCopyFileOS`, hard-requiring `RealPath` on **both** stores, else a vfs-level
  `O_EXCL` stream copy that opens through the fs values — works on MemMapFs, refuses a read-only dst; Store's
  staging copy and dirty mode's artifact
  placement). `UseCopyFileOS` is not a const only for Unit Test's sake; default true on Windows
  (`CopyFile_windows.go`), false elsewhere (`CopyFile_fallback.go`).

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [internal/UnsafeIO](../UnsafeIO/CLAUDE.md) — `Readlink`/`TryLstat`/`Symlink` deliberately ride the Abs-fallback
  `UnsafeIO.OSPath` (so they work on plain `OsFs` too), while `XSymlink`/`XCopyFile` ride the strict
  `UnsafeIO.RealPath`. Swapping flavors on either side silently trades a loud panic for wrong-root resolution.
- [Store](../../Store/CLAUDE.md) — `XCopyFile`'s two arms serve different consumers: the stream arm opens through
  the fs values (Store's MemMapFs tests depend on it), the `CopyFileW` arm hard-requires `RealPath` on both stores.
- [Build](../../Build/CLAUDE.md) — dirty mode's source-tree writes are ordinary vfs ops on a **writable** `SrcFS`
  (Runtime's `writableSrc`), plus `XSymlink`/`XCopyFile` here for the cross-store artifact pathways. `XSymlink`
  resolves the link side through `UnsafeIO.RealPath`, so it silently bypasses a read-only wrapper that forwards
  `RealPath` — keep its call sites to genuinely writable stores.
