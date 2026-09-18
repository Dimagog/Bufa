# internal/runmain

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- `Run(name, run)` CLI bootstrap: feeds `os.Args[1:]`/streams, prints `"<name> ERROR: <err>"` to stderr (a
  multi-line err starts on its own line after `ERROR:`), exits 1. On bad input the bufa `run()` prints the offending
  command's usage summary then panics the parse error, so the reason prints after the help.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [internal/contract](../contract/CLAUDE.md) — `Run` never recovers; it exits 1 only on a **returned** error, so
  every `run()` must open with `defer contract.Catch(&err)` or the repo's panic-based checks unwind into a Go trace
  and exit 2. The multi-line branch after `ERROR:` is not a nicety: contract wraps with `"%s\n%w"`, so a surfaced
  contract error is genuinely multi-line.
- [cmd](../../cmd/CLAUDE.md) — the `run(args, in, out, errOut) error` shape **is** the testability seam: all work
  lives in `run`, results go to `out`, diagnostics to `errOut`, nothing calls `os.Exit`. A new binary reaching for
  `os.Stdout`/`os.Exit` inside `run` silently removes that.
