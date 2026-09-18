# NameServer

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Single-host registry mapping any cwd to its project's srcRoot, so clients skip the source-root walk. **Pure
  cache** — the server never touches the filesystem; misses are the client's job (FS walk + back-fill). **Anchored
  entries only**: clients back-fill only anchored (`.BUFA` marker/`.git`) resolutions, so a hit is trusted as
  anchored without re-checking; the server cannot tell the difference, the gate lives in Runtime. Public API:
  `GetSrcRoot(cwd) (srcRoot, alive)` (alive=false ⇒ dial failed; alive=true + srcRoot="" ⇒ answered but uncached),
  `SetSrcRoot(dir, srcRoot)` (best-effort, async), `TryStart() func()` (nil on loss, cleanup func on win),
  `SockPath()`, `Stop(out)`.
- Global socket at `%TEMP%/bufa-ns.sock`. Each Watcher daemon calls `tryStartNS` at startup — listen-first /
  dial-probe (bind wins ⇒ also serve NS; live peer ⇒ plain per-project; stale socket ⇒ remove + retry once).
  **Client-driven takeover**: when a CLI finds NS dead (`alive==false`, via `Config.NSAlive`) AND its per-project
  daemon was already running, it RPCs `Watcher.TryStartNameServer` so that daemon retries the bind (no own-NS
  short-circuit — the dial-probe prevents a double claim, and a re-probe is what re-claims after an external
  `Stop`). Passive recovery, no polling. The hot path dials NS once per invocation.
- **External stop** (`bufa daemon stop/reset`): `Stop(out)` dials the global sock and calls the `Shutdown` RPC, which
  runs the cleanup (listener closed, sock removed) on a goroutine — fire-and-forget, so the sock disappears shortly
  **after** `Stop` returns; `daemon reset` still wins the re-claim in practice, and a lost race self-heals via
  takeover. Only the NS listener dies; the owning Watcher daemon keeps serving. The cleanup is `sync.Once`-guarded:
  reachable from both the Shutdown RPC and the owning daemon's exit (`closeNS`), and an unguarded second run could
  `os.Remove` a socket another daemon has since re-bound. Reporting mirrors `DaemonClient.Stop`: "Name server
  stopped" / "Name server was not running" (that path also removes a stale sock) / "Failed to stop name server: …"
  (RPC failed; the sock is left alone — it may be a live server's).
- RPC over in-memory `cwdCache: normalize(cwd) → srcRoot`. `GetSrcRoot` is a pure RLock lookup.
  `SetSrcRoot(SetSrcRootArgs{Dir, SrcRoot})` caches `Dir → SrcRoot` **plus every directory between them** by path
  arithmetic (no FS walk). `SetSrcRoot` is async (`go setSrcRootAt`); the unexported `setSrcRootAt` is synchronous
  so tests observe the write. Test seams `tryStartAt`/`getSrcRootAt`/`setSrcRootAt`/`stopAt` target a per-`TempDir`
  socket.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Watcher](../Watcher/CLAUDE.md) — entries are never revalidated against the filesystem, so the only invalidation
  paths are the owning daemon exiting and an explicit `Stop`. That is why Watcher self-shuts-down on a `.BUFA`
  marker Create|Remove|Rename rather than nil-ing its cached config; softening it strands this registry stale for
  the whole host. After an external `Stop` the owning watcher's held cleanup is **spent**, which is why `tryStartNS`
  re-probes unconditionally instead of short-circuiting on a non-nil `nsCleanup`.
- [cmd/bufa](../cmd/bufa/CLAUDE.md) — `daemon stop`/`reset` call `NameServer.Stop` **before** stopping or restarting
  the project daemon: stop's order keeps the Shutdown RPC from racing a dying NS-owning daemon, reset's frees the
  socket for the fresh daemon's claim.
- [Runtime](../Runtime/CLAUDE.md) — keys go through `Util.NormalizePath`, but the **value** is the caller's raw
  `SrcRoot`, stored and returned verbatim; Runtime uses it as `SrcRoot` and derives `BldRoot` from it without
  re-checking the FS, so the first client to `SetSrcRoot` fixes the spelling every later invocation inherits. The
  anchored-entries-only invariant is enforced entirely by Runtime's `SetSrcRoot` call sites.
