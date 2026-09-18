# internal/contract

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- Panic-based pre/post-conditions + error handling: `Require`/`Assert`/`Fail`/`Check`/`Checkf`/`Check2`/`Error`/
  `Errorf` (library funcs panic), `Catch`/`Rescue` (CLI `run()` does `defer Catch(&err)`), `Context("ctx", …)`
  (deferred panic-annotator prepending context as a panic unwinds), `PanicToError`. `With("ctx", …).Check2(v, err)`
  is the context-carrying Check2 (a generic method) — two calls because Go forbids extra args alongside a
  multi-value call. Every failing check logs an Error record before panicking, gated on the handler's level first —
  silent at the default `none`. **Bool validations use `Require`/`Assert`, never `if` + `Fail`**; `Fail` is only for
  unconditional dead-ends (e.g. default switch arms).

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [internal/runmain](../runmain/CLAUDE.md) — `Checkf`/`Errorf`/`Context` wrap with `"%s\n%w"`, so a surfaced
  contract error is **multi-line** (outermost breadcrumb first, leaf cause last) — which is why `Run` swaps the
  space after `ERROR:` for a newline. Changing the separator to `": %w"` flattens every CLI's error report.
  Conversely `Run` never recovers: every `run()` must open with `defer Catch(&err)` or these panics unwind into a Go
  trace and exit 2.
- [Store](../../Store/CLAUDE.md) — `Rescue`'s doc comment ("for tests") is **stale**: `Store.Check` wraps each entry
  re-hash in it to keep verifying past a corrupt entry, and Build's advisory pre-build dirty walk uses it to turn an
  unhashable tree into a cache miss.
- [internal/logging](../logging/CLAUDE.md) — failing checks call `slog.Default().Handler().Handle` **directly** with
  a hand-computed PC (fixed `runtime.Callers` skips) and re-check `Enabled` here; logging's
  `levelNone = slog.LevelError + 1` is what keeps them silent by default. Folding filtering into a wrapping
  `slog.Logger`, or lowering the `none` sentinel, turns every recovered contract failure into stderr noise — and
  the hand-computed PC breaks if the call depth changes.
- [cmd/bufa](../../cmd/bufa/CLAUDE.md) — `PanicToError` (and so `Catch`/`Rescue`) re-panics any recovered value that
  is not nil, an `error`, or a `string`. That is why cmd/bufa's `errCleanExit` must be an error **value**; a custom
  struct or int sentinel turns `--help` into an uncaught panic.
