# FilterFiles

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Public API: `Compile(rules []string) *Filter`, `CompileNoPrepend(rules []string) *Filter`,
  `Compose(defaults, user, overrides []string) *Filter`, `Filter.Included(path string) bool`, `DefaultRules`. Parse
  errors panic (via `contract`).
- Rules: `+pattern` (include) / `-pattern` (exclude). `*` matches non-slash chars, `**` anything. Trailing `/` =
  directory descendants. Unanchored patterns match a basename at any depth; `/`-prefixed are root-anchored.
  Case-insensitive (Unicode fold) full-string match, last matching rule wins, no match ⇒ excluded. Input is a
  forward-slash relative path; a leading `/` is accepted but not required.
- Baseline (implicit `+**`): `Compile` prepends it when the first rule starts `-` (positional heuristic).
  `CompileNoPrepend` takes rules literally. `Compose` makes the baseline **explicit**: the **user** tier decides it
  (empty or leading `-` ⇒ prepend `+**`; leading `+` whitelist ⇒ none), placed at the floor beneath `defaults`, then
  compiled via `CompileNoPrepend` — so `defaults` stay fully general and never hijack the heuristic. Build composes
  its src filter this way (hidden-files default + forced `-/BUFA` override).
- `DefaultRules` = `["-**/.**", "-**/_**"]`. Not auto-applied — callers pass it explicitly (`cmd/filter-files` when
  no rules given).

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Store](../Store/CLAUDE.md) — `Filter.Included` is fed a path already rebased to the build unit's **own dir**, so a
  `/`-anchored user rule anchors at that srcDir, never at the source root. Store consults the filter on regular
  files and symlinks **only** — directories are decided solely by `skipNested` — so no exclude rule ever prunes a
  subtree walk; a trailing-`/` rule takes effect by matching the descendant file paths.
- [Build](../Build/CLAUDE.md) — Build's forced `srcFiltersOverride` (`-*.BUFA`) rests on `normalizePattern`'s
  implicit `**/` prefix for unanchored patterns and on `*` matching a possibly-**empty** non-slash run (so it covers
  `.BUFA` and `x.BUFA` at any depth but never bare `BUFA`). Tightening either would stage live `<suffix>.BUFA`
  configs into their owner's tree. A zero-rule `Filter` excludes **everything**, which is why `bldFilterForOrNil`
  returns a nil `*Filter` — Store's verbatim sentinel — rather than `Compile(nil)`.
- [internal/Util](../internal/Util/CLAUDE.md) — the repo folds path case two incompatible ways: `Util.NormalizePath`
  uses `strings.ToLower`, this package `cases.Fold()` (they disagree on e.g. Turkish dotted İ and the Kelvin sign).
  `Doc/BrainStorms/DirectoryCasing.md` records that FilterFiles is meant to match `NormalizePath` — unresolved, so
  don't "fix" either side in isolation.
