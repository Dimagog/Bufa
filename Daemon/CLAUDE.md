# Daemon

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- On-demand single-instance UDS RPC daemon; client + daemon share one binary. Public API:
  `Connect(sockPath, beforeSpawn func(), daemonArgs ...string) (net.Conn, bool)` (the bool is `alreadyRunning` —
  true only when the first Dial hit a live daemon), `Serve(sockPath, idleTimeout, register)`,
  `PollUntil(timeout, cond) bool`
  (deadline loop at `pollInterval`=20ms — shared by `Connect`'s spawn-wait and `DaemonClient.Restart`'s
  cleanup-wait). Stdlib +
  `internal/contract` only, no platform code.
- `Connect`: Dial (hit ⇒ `conn, true`) → on miss `beforeSpawn()` if non-nil (the caller creates the sock dir there —
  never on a hit, so a connected hot path stays FS-free), then `exec.Command` of `os.Executable()` with
  `--daemon <sockPath> <daemonArgs...>` + `cmd.Process.Release` to detach, then poll-Dial up to `connectTimeout`=5s;
  returns `conn, false` (nil conn if it never listened). `register` receives the RPC server **and** a `stop func()`
  that closes the listener so a handler can trigger orderly shutdown.
- `Serve`: does **not** create the sock dir — the spawner's `beforeSpawn` did, so a missing dir fails loudly (Listen
  error, after one misleading stale-socket probe) instead of quietly minting an untagged build root. Then
  Listen-first / Dial-probe — Listen wins ⇒ own; Listen fails + Dial succeeds ⇒ live daemon, return; both fail ⇒
  stale, Remove + Listen once. `idleTimeout` via per-Accept `SetDeadline` (RPC on existing conns does not reset). A
  residual bind/listen race may leave one orphan listener that exits at its idle deadline — tolerated.
- `cmd/daemon` is the dual-mode reference binary (`<sockPath> [<idle>]`, `--daemon` selects daemon mode; default
  idle **5s**). Client mode's `beforeSpawn` `MkdirAll`s the sock dir; daemon mode writes `daemon-<pid>.pid` into it
  so tests count alive daemons portably.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [cmd/bufa](../cmd/bufa/CLAUDE.md) — the self-spawn vector is assembled across **three** packages:
  `DaemonClient.Connect` passes srcRoot as the sole extra arg, `Connect` here prepends `--daemon <sockPath>` and
  execs `os.Executable()`, and cmd/bufa dispatches on raw `args[0] == "--daemon"` **outside** the main kong grammar.
  From `Connect(sockPath, beforeSpawn, daemonArgs...)` alone there is no sign that another package pins the token,
  its position, and the argument count.
- [Watcher](../Watcher/CLAUDE.md) — the `stop` func handed to `register` closes the listener **only**, not open
  connections, which is why `Watcher.Shutdown` sleeps 50ms before calling it so the RPC reply flushes.
- [DaemonClient](../DaemonClient/CLAUDE.md) — `Serve`'s `defer os.Remove(sockPath)` is the only observable signal
  that a **live** daemon has exited; `Restart` polls for that file to disappear before the next `Connect`. Dropping
  the unlink turns every `bufa daemon reset` into a timeout plus a colliding spawn. A killed daemon's leftover sock
  (unlink defer skipped) is removed on the client side by `stop`'s not-running path. `beforeSpawn` is the other half
  of the sock-dir contract: DaemonClient passes `Store.EnsureBuildRootAt`, which is what creates the (tagged) build
  root in daemon mode — `Serve` relies on it and never `MkdirAll`s.
