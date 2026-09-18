# cmd — CLI binaries

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- `cmd/bufa` — the one user-visible binary. See [bufa/CLAUDE.md](bufa/CLAUDE.md).
- `cmd/daemon` — dual-mode reference binary for the `Daemon` transport. See [../Daemon/CLAUDE.md](../Daemon/CLAUDE.md).
- `cmd/daemon-client <sockPath> {GetSrcHash <path> | SetSrcHash <path> <hash>}` — one Watcher RPC against an
  already-running daemon (no spawn; missing socket = error). Prints `GetSrcHash('<path>'): '<hash>'` /
  `SetSrcHash('<path>')='<hash>'`.
- `cmd/hash-files <path>` — prints `Hashing.Hash` of a file/dir.
- `cmd/filter-files (--rule R… | --rules FILE) (<root> | --stdin)` — walk or stdin filter (`DefaultRules` when no
  rules).
- `cmd/test-shell [-i] [<script>]` — a minimal test interpreter standing in for a custom shell (Build's
  shell-definition tests publish it from a provider dir beside a `BUFA.shell`): runs the script's
  one-command-per-line language (`echo`, `write`, `append`, `env`, `exit`; `${VAR}` expanded), then with `-i` reads
  more from stdin until EOF or `exit`, printing `$FAKE_PROMPT` before each line. `main` exits with `run`'s **int**
  code directly — a shell's exit status is its contract, which `runmain`'s error-only exit 1 cannot express — so it
  is the one binary outside the `runmain` seam; `run(args, in, out, errOut) int` stays buffer-driveable.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [internal/runmain](../internal/runmain/CLAUDE.md) — every binary here (`test-shell` excepted, above) keeps all
  work in `run(args, in, out, errOut) error`: results to `out`, diagnostics to `errOut`, never `os.Exit`, so tests
  drive it in-process with `bytes.Buffer`s. Reaching for `os.Stdout`/`os.Exit` inside `run` in a new binary
  silently removes in-process testability.
