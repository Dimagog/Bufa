# BuildConfig

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- The parsed BUFA shape, shared so Build (TOML decode + script run) and Watcher (RPC cache value) don't cycle. Both
  `BufaConfig` and `RootConfig` double as TOML decode targets and gob-encoded Watcher cache values (a miss is signalled
  out-of-band by `…Reply.Found`, since the zero value is legitimate).
- **`BufaConfig`** = `Hash`, `VirtualDir`, `VirtualDirMaterialized`, an untagged `BaseConfig` embed (TOML keys
  flatten to the root table), and `Windows`, `Unix`, `Linux`, `MacOS BaseConfig`. The three `toml:"-"` fields are
  runtime-only, set
  by Build's `readBuildConfig` and carried in the daemon cache: `Hash` = `"F"+HashBytes` of the raw BUFA bytes (the
  config's cache-key term, so BUFA never rides in staged source); `VirtualDir` = declared by a `<suffix>.BUFA` in
  the parent ⇒ empty own source; `VirtualDirMaterialized` = the virtual dir existed **as a directory** at
  config-read time (dirty builds materialize them; a non-directory panics at read in both modes), so Build's
  `requireVirtualDirAbsent`
  can enforce the clean-mode must-not-exist contract even on a daemon hit.
- **`BaseConfig{Unsafe, LargeOutput, Shell, Cmd, Env, Deps, Filters}`** — every user-settable setting; it is both the
  root-table shape and, verbatim, the shape of every platform section.
  - `Unsafe bool` — Build runs the script with the inherited PATH and salts the key with a TTL bucket.
  - `LargeOutput bool` (`largeOutput`) — provider-declared: consumers stage this dir's output as a `deps.bld` dep by
    **link** (`Store.RestoreLink`) instead of copy; clean mode only, no cache-key term, consumers treat the tree
    read-only (`Doc/Specs/LinkStaging.md`).
  - `Cmd Cmd` — the `cmd` union (`Cmd.UnmarshalTOML` shape dispatch): a **string** is the script body (`Script`),
    **`false`** declares the dir scriptless (`Disabled` — stage+publish only); any other type fails at decode. A
    platform section's `cmd` overrides the root one as a whole value (presence fold — `[windows] cmd = false`
    silences a root script). `IsScriptOptional()` = `Cmd.Disabled` — the only opt-in; Build's `getPlatformScript`
    adds the must-have-a-script panic on the run path. The script **file** is `BUFA<ext>` from the shell definition.
  - `Shell string` — which shell runs `cmd`: a **bare name** (a marker `[shells]` alias, else a preset `cmd`/`bash`)
    or a **provider dir path** in dep grammar (`/build/nu` root-relative, `./tools/nu` dir-relative).
    `IsShellPath(value)` is the one grammar split — leading `.` or any `/` or `\` ⇒ path. Folds by presence; `""` ⇒
    inherit the marker's `shell`, else the platform's native preset. Build owns resolution, keying, and the implied
    provider dep (`Build/CLAUDE.md`, **Shells**); this package carries the value and the `BUFA.shell` schema.
  - `Deps{Src, Bld []string, Ext []ExtDep, Export []string, CacheDir bool}`. `deps.export`
    (`Doc/Specs/ExportDeps.md`): dep paths in the `src`/`bld` grammar, or an `[[deps.ext]]` `name`, whose staged
    tree publishes with the output instead of being pruned; a list, so it concats root-first. Build's
    `readBuildConfig` resolves and validates the entries and folds every `ExtDep.Export` shortcut in — the list is
    the one form Build consults. `deps.cacheDir` (`Doc/Specs/CacheDir.md`): the dir requests its persistent
    `BUFA_CACHE_DIR`; a scalar inside the `[deps]` table, so it folds by **presence**
    (`containsTomlKey(plat, "deps", "cacheDir")`), not by concat; its only cache-key effect is `Config.Hash`.
    `ExtDep{Url, Hash, Name, Large, Export}` (`Doc/Specs/ExternalDeps.md`): `Hash` the mandatory `F` pin or `"?"`
    bootstrap; `Name` the
    dir-relative landing path, default the url's file name; `Large` = place by reference (file symlink into the
    global cache) in both modes; `Export` = a **TOML shortcut only** — Build folds it into `Deps.Export` and clears
    it, so a resolved config never has it set.
  - `Env EnvTable[EnvVar]`, `EnvVar{Value, File}` (`Doc/Specs/BufaEnvVars.md`): a **literal** (`X = "4.13.2"`) or
    **file-sourced** (`X = { file = "antlr.ver" }`), exactly one set — `EnvVar.UnmarshalTOML` shape dispatch, which
    also rejects unknown/duplicate table keys and non-string or empty file values. Build's `readBuildConfig` resolves
    each entry to its literal, so cached configs carry final values.
  - `Filters{Src, Bld, Dirty []string}` — `[filters].src` rules the dir's own source before staging/hashing, `.bld`
    the output before publishing, `.dirty` the tree hash in dirty mode (where src/bld are ignored); empty ⇒ no
    filtering on that axis.
- **`EnvTable[V]`** (`EnvTable.go`, `V` ∈ `string | EnvVar`) is the one `[env]` shape: an embedded
  `Util.OrderedMap[string, V]` in **document order** (Build's script export and `${NAME}` visibility follow it —
  `Doc/Specs/EnvVarInterpolation.md`); `Get`/`Set`/`Len`/`All` are the whole API, `Set` keeping a known name's
  position. The embedding is what makes the table gob-encodable through the daemon RPC (`OrderedMap`'s own
  `GobEncode`/`GobDecode`), so `EnvTable` must stay field-less; no literal form or equality — build with `Set`s,
  compare via `All()`. Its `UnmarshalTOML` builds the table from the decoder's `map[string]any` (`decodeEnvValue`
  dispatches per `V`: `EnvVar.UnmarshalTOML`, or for `V = string` a "must be a literal string" error — what makes the
  root marker literals-only); that map has **no order**, so the owning config's `afterDecode` hook calls
  `orderByDocument(keys, tablePath...)`, which `Reorder`s the table to the metadata's keys under `tablePath`
  (`EqualFold` on the path, exact entry names) and `Require`s the counts to match — catching case-variant duplicate
  tables (`[env]` + `[ENV]`: the decoder EqualFold-folds both into one field, each call **resetting** the table, so
  the last would silently win). A bare `toml.Decode` leaves a sorted table. `override(section)` is the platform
  fold: `Set` per section entry, so an override keeps the top-level key's position and a new name follows; both
  tables are ordered before the fold. The name/value rules shared by a dir's `[env]` and the marker's live here:
  `CheckEnvVarNames[V]` (keys non-empty, case-fold-unique, no `BUFA_` prefix — the sole reserved namespace; a
  bufa-controlled name like `PATH` is a legal overriding key) and `NormalizeEnvVarValue` (`TrimSpace` to one
  non-empty line).
- **Platform sections** `[windows]`/`[unix]`/`[linux]`/`[macos]` — four **top-level** tables forming a fold tree
  (`root ← [windows]`; `root ← [unix] ← [linux] | [macos]`; `[unix.linux]` is an unknown key). `PlatformSections`
  is this host's chain, general to specific: `["windows"]`, `["unix", "linux"]`, `["unix", "macos"]`, or a bare
  `["unix"]` on any other GOOS; `PlatformSectionNames` renders it as `[unix]/[macos]` for error messages (Build's
  missing-script panic). `applyPlatformSettings(keys, sections)` walks the chain, and for each section **present** in
  the document orders its `[env]` by document order then `BaseConfig.applySection` merges it onto the root — so a
  `[linux]` setting beats `[unix]` exactly as `[unix]` beats the root, and `[linux]` needs no `[unix]` beside it. It
  runs **inside `DecodeConfigOrScript`** — the one place a `BufaConfig` is born, so no caller can observe the unfolded
  shape — and **zeroes all four sections** afterwards, so cached configs carry effective values only and nothing
  outside this package reads a section. The `sections` parameter exists so tests fold the `unix → linux` chain on
  any host; production passes `PlatformSections`. Merge rules, identical at every level:
  scalars (`unsafe`, `largeOutput`, `cmd`, `shell`, `deps.cacheDir`) override when **present** (via `defined`, not
  truthiness — an explicit `unsafe = false` beats a root `true`); `deps.src/bld/ext` + `filters.*` **concat**
  root-first (appended filter rules win under last-match; ext keeps the additive `[[windows.deps.ext]]` precedent);
  `[env]` merges per **exact** key via `EnvTable.override` (a case-fold-only collision keeps both keys and fails
  `CheckEnvVarNames`). With an empty root `filters.src`, platform rules become the user tier's head, so a platform
  whitelist suppresses Compose's implicit `+**`. The presence set (`tomlKeysSet`/`containsTomlKey`, a struct around
  a `Util.Set` in a **named** field so every lookup goes through `foldKey`) covers the document's keys **including
  every prefix** (the decoder matches fields `EqualFold`, the metadata records full paths in document spelling —
  `[Windows]` must read as defined, and `[windows.deps]` alone must mark the section present; `foldKey` joins
  NUL-separated since quoted key parts can contain dots).
- **Two TOML decode gateways**, both hard-failing on any unconsumed key via `checkUnknownKeys` (keys consumed by
  `EnvVar.UnmarshalTOML` count as decoded) and both ending in `finalizeDecode`, which fires the
  **`afterDecode(keys []toml.Key)` hook** on a target implementing `decodeFinalizer` — the one place a shape gets
  the document's key order: `RootConfig.afterDecode` orders its `[env]`; `BufaConfig.afterDecode` orders the
  root `[env]` then runs the platform fold (which orders each present section's `[env]` before folding it);
  `ShellDef` has none. `DecodeStrict(data, path, v)` is for shapes
  without platform sections — `ShellDef` and the `.BUFA` marker (a broken project config must fail, never run).
  `DecodeConfigOrScript(data, path, cfg)` is Build's BUFA decode: same strictness, plus a file that fails TOML
  **parsing** is a raw script — the whole content becomes the cross-platform `cmd`, `Unsafe` forced true (no way
  to declare deps, so the script needs the inherited PATH), every other setting default. A document that parses
  but holds unknown keys still hard-fails. **Parser-progress guard** (`startedAsToml`,
  `Doc/Specs/RawScriptDetection.md`): a parse failure is a broken config — hard fail with the
  `(if it's a raw script it must not start as parsable TOML, e.g. begin with a NAME=… line)` hint — when the
  `ParseError`'s `LastKey` is set (the parser's *current* key, `""` between statements) or when some whole-line
  prefix of the file (`SplitAfter("\n")` so CRLF reaches the parser intact) decodes to a non-empty map. So
  a raw script must not begin (after blank/`#` lines) with a `NAME=…` line or a `[table]` header. **Residual hole**:
  a *first* statement that breaks before its `=` (`largeOutput true`, `[env` on line 1) leaves no TOML evidence and
  runs as a script — accepted. The daemon config cache is keyed by BUFA bytes and never caches a hard fail.
- **`ShellDef`** — the `BUFA.shell` schema (`Doc/Specs/ConfigurableShells.md`), one shape for Build's embedded
  presets and a provider dir's published file. `Name` (`toml:"-"`, filled by Build: preset name or resolved provider
  dir), `Exe` (bare `x` ⇒ bufa's PATH, absolute as is, any other relative path inside the provider's output;
  `IsBareExe()` = `filepath.Base(exe) == exe` is the one definition of "bare"), `Ext` (script file = `BUFA<ext>`),
  `Run []string` (argv after exe for a build), `Shell`/`PostShell []string` (the `--shell`/`--post-shell` argv; **nil
  ⇒ the mode is absent** and that flag fails at invocation — `[]` is a present empty argv, and `DecodeStrict` keeps
  the distinction), `Prompt ShellPrompt{Env, Default}` (zero ⇒ no marker), `Move`/`Copy` (the `BUFA_COPY_OR_MOVE`
  spellings; both or neither). `${script}` (`ScriptPlaceholder`) is the **only** argv placeholder.
  `DecodeShellDef(data, path)` = `DecodeStrict` + `Validate`: `exe`, `ext` (`.`-prefixed, no separators), and `run`
  required; `run` and any present `postShell` must reference `${script}`; `prompt.default` needs `prompt.env`; verbs
  paired. Build caches loaded definitions in-process only — not a daemon gob contract.
- **`DecodeEnvFile(data, path) EnvTable[string]`** — the `BUFA.env` dotenv decode (`Doc/Specs/DepEnvVars.md`), not
  TOML: BOM stripped, each line `TrimSpace`d, full-line `#` comments and blanks skipped, `KEY=VALUE` split at the
  **first** `=` with the value verbatim (inner `=`/`#` and leading spaces are value bytes; `NormalizeEnvVarValue` is
  deliberately not applied). Hard errors with line numbers: a non-comment line without `=`, an empty value, a
  duplicate key (its own case-folded check, because `Set` would collapse an exact duplicate before
  `CheckEnvVarNames` could see it). Document order. Build layers it between the root and dir `[env]` tables.
- **`RootConfig`** = `Unsafe RootUnsafe{TTL}`, `Env EnvTable[string]`, `Shell string`, `Shells map[string]string`,
  `AllowBufaDir bool` — the `.BUFA` marker settings; no platform sections (`[windows]`/`[unix]` fail as unknown keys).
  `[unsafe].ttl` is a Go duration, default `15m` (`DefaultRootConfig()`; an empty marker is legal); `0` ⇒ bucket by
  `BuildStartTimeUTC` at millisecond precision. `[env]` is `EnvTable[string]`, NOT `EnvTable[EnvVar]`, so the type
  itself says **literals only** (the daemon's root-config cache is evicted by marker writes alone, so a file-sourced
  value could go stale). `allowBufaDir` — a `BUFA`-named *directory* is ordinary content (needed only to build bufa
  itself); Build copies it once into `Store.AllowBufaDir`. `Validate()` (pointer receiver — it **mutates**) requires
  `TTL >= 0`, runs `CheckEnvVarNames`, rewrites each literal to its `NormalizeEnvVarValue` form (so the daemon-cached
  root config carries final values), checks the `[shells]` **catalogue** (each alias a bare name, each value a
  `/`-prefixed source-root-absolute provider dir), and requires a non-empty `shell` to name an alias or be a
  `/`-prefixed provider path. `shell` and `[shells]` are carried as written — Build resolves them live per build
  (an alias may shadow `cmd`/`bash`; Build resolves the shadowed provider's own self-reference to the preset).
  `GetHash()` folds **every marker setting that must re-key every dir** into one hierarchical
  `CombineHashesInPlace`: `env` (the fold of its entries), `shell` (the raw default selector — deliberately re-keying
  every dir on a marker-default change), plus the non-marker `bufaVersion` = the `x.y` of `Bufa.Version` (bufa's own
  semantics are an every-dir input no config byte carries: a minor bump is a total rebuild, a patch bump restarts
  the daemon and re-keys nothing). Excluded on purpose: `[unsafe].ttl` (Build folds it as the unsafe-dirs-only
  `unsafe.ttl` term), `[shells]` (an alias resolves to a provider dir the consumer's dep manifest keys by path and
  output hash), `allowBufaDir` (without it the walk panics, so no cached output can differ). A new every-dir setting
  joins the top-level list here, nowhere else.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Watcher](../Watcher/CLAUDE.md) — both structs travel as **gob-encoded** RPC cache values, so the exported-field
  set is a wire contract: a shape change invalidates a running daemon's cached configs (flushed by the version
  bump's daemon restart), and a miss is signalled only via `…Reply.Found`. Cache value types live here so Build and
  Watcher don't cycle.
- [Runtime](../Runtime/CLAUDE.md) — `[unsafe].ttl = 0` means "bucket by `Config.BuildStartTimeUTC` truncated to a
  millisecond", which makes that timestamp a **cache-key input** that must stay fixed for the whole invocation.
- [Hashing](../Hashing/CLAUDE.md) — `ExtDep.Hash` is a user-authored `F…` key: the pin is literally `HashFile`
  output, so the key encoding is a committed config-file format, not an internal detail.
- [Build](../Build/CLAUDE.md) — Build exports `RootConfig.Env` values as-is (no re-trim, no emptiness check) and
  hashes them as written into `root.config`, on the strength of `RootConfig.Validate` having trimmed and checked
  each literal; dropping the rewrite (or making `Validate` a value receiver) silently exports untrimmed or empty
  values. Build iterates both `EnvTable`s as-is, trusting document order — which only the gateways' `afterDecode`
  hook guarantees; a table reaching Build any other way exports name-sorted with no error. Build applies a
  `ShellDef` without re-checking it — `Ext` glued onto `BUFA`, `Run` trusted to carry `${script}`, nil
  `Shell`/`PostShell` as "mode absent" — so `DecodeShellDef`'s `Validate` and the nil-vs-`[]` distinction are what
  stand between a malformed `BUFA.shell` and a silently script-less shell invocation.
- [Store](../Store/CLAUDE.md) — `Store.ShellDefFileName` (`BUFA.shell`) lies **outside** `IsReservedName`'s
  namespace on purpose: a provider's definition must stage as its own source and publish beside its binary.
  Reserving it would make `srcFiltersOverride` or `cleanLocalName` drop it and every provider publish without its
  definition.
