# DaemonClient

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Type `Client` — the build-side wrapper over the Watcher RPC daemon (distinct from the `Daemon` transport). Holds a
  `*rpc.Client` that is **nil when the daemon is disabled/unreachable**; every method nil-checks it and returns a
  miss/no-op, so callers never nil-check. Imports `BuildConfig`+`Daemon`+`Watcher`+`Util` but **not `Runtime`** —
  the lifecycle funcs take Config primitives (build root, source root, disabled/nsAlive), keeping
  `Runtime`↔`DaemonClient` acyclic.
- Lifecycle: `Connect(bldRoot, srcRoot, disabled, nsAlive, out)` derives the sock `<bldRoot>/bufa-d.sock` (exported
  `SockName`; `""` when disabled), thin over `Daemon.Connect`; when that reports `alreadyRunning && !nsAlive`, it
  fires `Watcher.TryStartNameServer` (NS takeover). It also stashes a `restartDaemon(msg)` closure (print `msg`,
  then stop/wait/re-`Connect`) for the version-mismatch path. `Stop(bldRoot, disabled, out)` dials + sends
  `Watcher.Shutdown` and returns **immediately** (the not-running path still removes a killed daemon's leftover
  sock), reporting "Daemon stopped" / "Daemon was not running" / disabled / failed — used by `gc`, `check`,
  `daemon stop`, and daemon-disabled dirty builds (which pass `disabled=false` so Stop reaches the sock). `Stop`
  and `Restart` share the unexported `stop(…) (sockPath, stopped)`, which never prints the success line.
  `Restart(bldRoot, disabled, out)` = `stop` + `waitSockGone` (`Daemon.PollUntil` for the sock to disappear, so the
  following `Connect` doesn't collide with the dying daemon), reporting "Daemon restarted". `StopWait` = `stop` +
  the same wait, reporting "Daemon stopped" — for `bufa nuke`, which deletes the sock's dir next.
- Cache methods: `Get`/`SetSrcHash`, `Get`/`SetDirtyHash`, `Get`/`SetDirtySkipHash`, `GetBuildConfig(srcDir)`
  (returns config + src-dep hash map + found), `SetBuildConfig`, `GetBuildHash(combined, srcDir)` (returns build
  hash + `PathBuildHashCurrent`), `SetBuildHash`, `SetPathBuildHash`, `Get`/`SetRootConfig`. `Get*` use synchronous
  `Call`; `Set*` are fire-and-forget via `Go` + `logRpcError` (drains `call.Done` so async write errors stay
  visible). `GetRootConfig` also checks the reply's version stamp: on
  mismatch it restarts the daemon in place — `restartDaemon` reruns `Restart` + `Connect` and swaps the fresh handle
  into the **same** `Client` (`*c = *Connect(...)`) — then returns a miss, so the caller's disk fallback runs and its
  `SetRootConfig` primes the fresh daemon through the swapped handle. Only the version *comparison* restarts; an RPC
  error keeps the plain fallback.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Watcher](../Watcher/CLAUDE.md) — the client↔daemon contract is **untyped**: RPC method names are string literals
  over gob-encoded structs. A renamed method or reshaped struct on either side compiles and degrades to a logged
  miss or a dropped write — never a build failure — and nothing in the repo gates it (`cmd/daemon-client` hard-codes
  two of the same strings). The `GetRootConfigReply.BufaVersion` stamp is the guard: the daemon stays passive, this
  side compares — and gob leaves the field `""` on an older daemon's reply, which reads as a mismatch.
- [Runtime](../Runtime/CLAUDE.md) — the methods nil-check the **inner rpc handle**, not the receiver: `Config.Daemon`
  is nil on the `PrepareConfigWithNoDaemon` paths, where a cache call panics instead of degrading. The mismatch
  restart swaps the `Client`'s innards rather than returning a new one because `getRootConfig`'s `Cache.Wrap0_Bool`
  binds method values to the `*Client`; a replacement would leave the wrapper writing to the dead daemon.
- [Daemon](../Daemon/CLAUDE.md) — `Serve`'s `defer os.Remove(sockPath)` is the only observable signal that a
  **live** daemon has exited, and `Restart` polls for it before the next `Connect`; dropping the unlink turns every
  `bufa daemon reset` into a timeout plus a colliding spawn. The killed-daemon leftover is `stop`'s job: its
  not-running path removes it (its own dial just failed, so no live-probe needed).
- [Cache](../Cache/CLAUDE.md) — returning `""` on a miss (and when disabled) is what lets these getters stack under
  `Cache.Wrap_Def`, whose miss marker is the zero value. A new hash RPC whose empty string is a legitimate value
  silently turns every hit into a miss.
