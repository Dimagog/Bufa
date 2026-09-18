# internal/logging

Package guidance. Repo-wide conventions: [CLAUDE.md](../../CLAUDE.md).

- `Configure(level, errOut)` installs a custom pretty `slog.Handler` as default; level from arg/`$LOG_LEVEL`, else
  `none` (above Error ⇒ silent). Source appended only when `$LOG_SOURCE` is truthy. Flat attrs only
  (WithAttrs/WithGroup implemented but unused).

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [cmd/bufa](../../cmd/bufa/CLAUDE.md) — `LevelNames` (the literal `"none, error, warn, info, debug"`) is
  interpolated verbatim into kong's `enum:"${log_levels}"` tag, so its spelling **and order** are the CLI's
  legal-values contract; cmd/bufa's side shows only `${log_levels}`. (`TestLevelNamesInSync` guards it against the
  `levels` map, not the CLI.)
- [internal/contract](../contract/CLAUDE.md) — contract calls `slog.Default().Handler().Handle` **directly** with a
  hand-computed PC and re-checks `Enabled` itself, bypassing any wrapping `slog.Logger`. `levelNone` at
  `slog.LevelError + 1` is what keeps failed checks silent at the default level; lowering it, or folding filtering
  into a `Logger`, turns every recovered contract failure into stderr noise.
