# Bufa Reference

This document describes every setting of a `BUFA` build config and of the `.BUFA` project root config, every environment
variable Bufa reads or sets, and the two files a build dir can publish for its dependents: `BUFA.env` and `BUFA.shell`.
The [README](../README.md) explains the concepts; this file is the complete list.

Contents:

* [Conventions](#conventions)
* [`BUFA` — build dir settings](#bufa--build-dir-settings)
* [`.BUFA` — project-wide settings](#bufa--project-wide-settings)
* [Environment variables](#environment-variables)
* [`BUFA.env` — dependency-published env variables](#bufaenv--dependency-published-variables)
* [`BUFA.shell` — shell definitions](#bufashell--shell-definitions)

## Conventions

**Files.** A directory becomes a build dir when it holds a file named `BUFA`, even a completely empty one: an empty
`BUFA` has nothing to build, but it excludes the directory from its parent's source and makes it a valid `deps.src` target.
A `<name>.BUFA` file declares a *virtual* build dir `<name>` inside the directory holding the file (same settings, no
source of its own). A `.BUFA` file marks the project root. The whole `*.BUFA` suffix is reserved: no source file or
`[[deps.ext]]` artifact may be named `BUFA` or `*.BUFA` (compared case-insensitively). `BUFA.env` and `BUFA.shell`
are deliberately outside that rule.

**Paths.** Everywhere a setting names a project path (`deps.*`, `shell`, `[shells]`), a `/`-prefixed path is relative
to the project root and a plain path is relative to the build dir (`/build/antlr`, `../Grammar`, `tools/nu`).
Relative paths in a virtual config resolve against the virtual dir, not the file's parent.

**Typos fail.** `.BUFA` is TOML or empty; `BUFA` is TOML, empty, or a raw script (below). In either TOML file an
unknown key is an error, never silently ignored.

**Two forms of `BUFA`.** A `BUFA` file that does not parse as TOML is a *raw script*: the whole file becomes the
cross-platform `cmd`, `unsafe` is implied (a raw script cannot declare deps, so it gets your real `PATH`), and every
other setting is at its default. A raw script must not start with anything that parses as
TOML, such as a `NAME=…` line or a `[table]` header. A file that parses but holds unknown keys is a broken config, not
a script.

## `BUFA` — build dir settings

All keys, with their defaults:

| Key                  | What                     | Type                | Default        | Platform fold |
| -------------------- | ------------------------ | ------------------- | -------------- | ------------- |
| `cmd`                | the build script         | string or `false`   | absent         | replace       |
| `shell`              | shell running `cmd`      | string              | `""` (inherit) | replace       |
| `unsafe`             | non-hermetic build       | bool                | `false`        | replace       |
| `largeOutput`        | consumers link, not copy | bool                | `false`        | replace       |
| `deps.src`           | source deps              | list of paths       | `[]`           | append        |
| `deps.bld`           | built deps               | list of paths       | `[]`           | append        |
| `[[deps.ext]]`       | pinned downloads         | list of tables      | `[]`           | append        |
| `deps.export`        | deps shipped in output   | list of paths/names | `[]`           | append        |
| `deps.cacheDir`      | persistent cache dir     | bool                | `false`        | replace       |
| `[env]`              | script env vars          | table               | empty          | merge per key |
| `filters.src`        | what is source           | list of rules       | `["-.**"]`     | append        |
| `filters.bld`        | what is output           | list of rules       | `[]`           | append        |
| `filters.dirty`      | what dirty mode hashes   | list of rules       | `["-.**"]`     | append        |
| `[windows]`/`[unix]` | per-platform overrides   | table               | empty          | —             |
| `[linux]`/`[macos]`  | per-OS overrides on Unix | table               | empty          | —             |

### `cmd`

The build script, or `false`.

* **A string** is the script body. It is written to a file named `BUFA<ext>` (`ext` from the shell definition) under
  the store's `tmp/` dir and run by the dir's shell with its cwd in the build dir: the sandbox copy in a clean build,
  the real dir in a dirty build. A non-zero exit fails the build.
* **`false`** declares the dir deliberately scriptless: it only stages its deps and publishes them. This is the one
  way to make a build dir without a script; combined with an exported `[[deps.ext]]` it is a complete artifact
  provider. `cmd = true` or any other type is a decode error.
* **Absent** is legal to parse but not to build: `bufa` on such a dir fails with report that it has nothing to build. An
  empty `BUFA` is still useful, since it excludes the directory from its parent's source and makes it a valid
  `deps.src`.

A root-table `cmd` is cross-platform. A platform section's `cmd` replaces it as a whole value on that platform: with
a root `cmd = "./build.sh"` and `[windows] cmd = "build.cmd"`, Windows runs `build.cmd` and every other platform runs
`./build.sh`. The replacement is total, so `[windows] cmd = false` silences a root script on Windows only. On Linux
and macOS a `[linux]`/`[macos]` `cmd` in turn replaces the `[unix]` one, see
[Platform Sections](#platform-sections-windows--unix--linux--macos).

### `shell`

Which shell runs `cmd`. One of:

* A **bare name**: an alias from the `.BUFA` `[shells]` catalogue, else a built-in preset — `cmd` (Windows only) or
  `bash` (every platform, resolved from Bufa's own `PATH`).
* A **provider dir path** (anything with a leading `.` or containing `/` or `\`): a build dir whose output holds a
  `BUFA.shell` definition, `/build/nu` root-relative or `./tools/nu` dir-relative.
* `""` (absent): inherit the `.BUFA`'s `shell`, else the platform's native preset.

Precedence: platform section > root table > `.BUFA` root > native preset (`cmd` on Windows, `bash` elsewhere).

A provider-backed shell is an **implied `deps.bld`**: the provider builds first and is staged like any dep. Listing
the provider in your own `deps.bld` is an error, and a provider that resolves to itself (it forgot to name its own
native shell under a provider-backed default) is an error too. Exception: an alias that shadows a preset name
(`[shells] bash = "/build/bash"`) lets that provider build under the real preset.

A scriptless dir (`cmd = false`) resolves no shell unless `--shell`/`--post-shell` targets it. See
['BUFA.shell'](#bufashell--shell-definitions) for what a definition contains and how the presets behave.

### `unsafe`

`unsafe = true` makes the build non-hermetic:

* The script inherits Bufa's **whole environment**, including `PATH`, `HOME`, `USERPROFILE`, etc.
  Only `TEMP`/`TMP`/`TMPDIR` are still redirected to the store's `tmp/` dir, so builds cannot accidentally leak state to
  each other through a shared temp dir.
* The result is cached, but under a **time bucket**: the cache key includes the build start time truncated to
  `.BUFA`'s `[unsafe].ttl`, so once a later invocation starts in a new time bucket the dir rebuilds. Bufa cannot
  hash the inherited tools and variables, so it rebuilds periodically instead: a toolchain change is noticed within
  one TTL rather than never.
* **Symlinks** in the dir's source are staged as link objects instead of being rejected, and symlinks the script
  creates in its output are published as links.

A raw-script `BUFA` is implicitly `unsafe`.

`[[deps.ext]]` downloads don't need `unsafe=true`: a pinned hash makes the fetch
deterministic, so the build stays hermetic.

### `largeOutput`

`largeOutput = true` declares that this dir's output is large and that consumers only read it. Every dir that lists it
in `deps.bld` stages the output as **one directory symlink** into the store instead of copying it. A copy-staged dep is
a disposable copy the consumer's script may edit, move, or delete; a linked tree is the store itself, so the script must
treat it read-only, run and read it in place, and may not name it in `deps.export`. Clean mode only; `deps.src` staging
and dirty builds are unaffected. No cache-key effect on consumers.

> [!CAUTION]
> This is one of the "For Adults" aspects of Bufa: it's a great optimization, essential for making Shells and
> Toolchains regular build dependencies, but nothing stops a consumer's script from writing through the link into
> the store, which corrupts that output for every consumer.

Such corruptions could be detected with `bufa check` after the fact.\
And `bufa check --fix` removes it from the store, so the next build republishes a clean copy.

### `[deps]`

There are 3 dependency kinds plus 2 modifiers. Every dep is staged at its project-relative path, so the sandbox mirrors
the source tree and scripts reach a dep by the same relative path they would use in the project.

#### `deps.src`

A list of build dirs whose **filtered source** is staged into your sandbox. Each must be a build dir (an empty `BUFA`
will do), is not built (by this reference), is filtered by *its own* `[filters].src`, and its content hash is part of
your cache key. In dirty mode the dep is hashed but nothing is staged.

#### `deps.bld`

A list of build dirs whose **build output** is staged into your sandbox. They build first, recursively (each dir
at most once per run, circular chains rejected before any script runs). Only the dep's output enters your sandbox
and only its output hash enters your cache key, so a dep change that leaves its output byte-identical does not
rebuild you. Nothing is transitive. In dirty mode the deps build dirty, in place, before you. A dep's published
`BUFA.env` is exported into your script's environment (see ['BUFA.env'](#bufaenv--dependency-published-variables)).

#### `[[deps.ext]]`

A list of pinned downloads, fetched by Bufa, verified once, and kept in the global per-user artifact cache. The
network is never on the rebuild path, and since the pinned hash makes the fetch deterministic, a dir using them stays
hermetic: no `unsafe = true` needed.

One TOML table per artifact:

* **`url`** (required) — the download source, any scheme. `${NAME}` references expand from this dir's `[env]`, not
  from `.BUFA`'s.
* **`hash`** (required) — the Bufa `F…` content hash of the file, or `"?"` to bootstrap: Bufa downloads, prints the
  computed hash, and fails the build. Paste the printed hash into `BUFA` as the new `hash` value and build again: the
  download is already in the the global cache, so the second build is a pure cache hit with no re-download.
* **`name`** — the landing path relative to the build dir. Default: the url's file name. May point into a subdir
  (`libs/antlr.jar`), which Bufa creates. Must be strictly local (no `..`, not absolute) with a basename other than
  `BUFA`/`*.BUFA`.
* **`large`** — `true` stages a symlink to the global cache instead of a writable copy. Treat it read-only and never
  relocate it. Default `false`.
* **`export`** — `true` ships the artifact in the build output: the bytes for a copy, the link object for `large`. A
  shortcut for listing `name` in `deps.export`. Default `false`: the artifact is an input, pruned at publish.

A `hash` mismatch fails loudly naming the computed hash. Editing `url` with an unchanged pin costs one re-verifying
download, use "?".\
Two entries landing at the same `name` (after the platform sections are merged in) are an error, and so is a `name`
already taken by a real source file.

 `[[windows.deps.ext]]`/`[[unix.deps.ext]]`/`[[linux.deps.ext]]`/`[[macos.deps.ext]]` entries are added to the shared
 ones, so a download that differs per OS is one entry per section.

 In dirty mode the artifact is placed into the real dir at `name`; `export` is a no-op.

> [!CAUTION]
> `large` is the same "For Adults" trade-off as `largeOutput`, with a **wider blast radius**: the link points
> into the global artifact cache shared by every project on the machine. Entries are marked read-only at insert, so a
> plain accidental write fails loudly, but a script dedicated to misbehave, that corrupts or replaces the file corrupts
> the entry for every project, and since a cache hit is trusted by name with no re-verification, no later build notices.

`bufa check` detects a corrupted entry that an exported `large` artifact points at.

`bufa nuke --global-only` (or `--global`) deletes the whole cache; the next build re-downloads and re-verifies every
artifact.

#### `deps.export`

A list of deps whose staged tree **ships with your build output** instead of being pruned at publish. Entries use the
dep-path grammar and must name a `deps.bld`/`deps.src` entry lying strictly below the build dir, or a `[[deps.ext]]`
artifact by its `name`. Anything else — a sibling or ancestor dep, the dir itself, a name that is no dep — is a config
error. An exported dep publishes as its staged content plus whatever the script wrote into it (the accumulate
pattern).

A `largeOutput` dep cannot be exported. Deps nested inside an exported dep are still pruned unless exported
themselves. `[filters].bld` still applies inside an exported tree.

A no-op in dirty mode, still validated.

#### `deps.cacheDir`

`deps.cacheDir = true` gives the dir a **persistent, writable directory** of its own that survives unchanges across
buids, and exports its name as `BUFA_CACHE_DIR`. Bufa creates it before the script runs, never wipes it (not on failure,
not on `--force`, not on `bufa gc` while the dir exists), and never looks inside. Point `GOCACHE`, `CARGO_HOME`, or a
Maven repo at `%BUFA_CACHE_ROOT%\%BUFA_CACHE_DIR%`. Legal in a virtual config.

> [!CAUTION]
> The cache dir is another "For Adults" feature: its content is history that Bufa never hashes, so a build's output
> may silently depend on what earlier builds left there, and the same cache key can yield different bytes on a machine
> whose cache is colder or warmer. Requesting it asserts that the tool's cache is transparent, as Go's or Cargo's is. A
> `--force` rebuild that prints the non-reproducibility warning is the first sign that it is not.

`bufa nuke --cache-only` empties every such dir;

`bufa gc` removes the entry of a build dir that no longer exists, but does no other cleanup.

Written as `deps.cacheDir = true` at the top level or `cacheDir = true` inside `[deps]`.

### `[env]`

Variables exported into the build script's environment, in both modes. Each value is a **literal** or **read from a
file** in the build dir:

```toml
[env]
JAVA_VER  = "21"
ANTLR_VER = { file = "antlr.ver" }
PATH      = '${BUFA_BUILD_ROOT}\tools;${PATH}'
```

* A value must trim to one non-empty line. A file path is relative to the build dir, strictly local, and its basename
  must not be `BUFA`/`*.BUFA`. A virtual config may not use `{ file = … }` (it has no source), literals are fine.
* Keys must be non-empty, unique case-insensitively, and must not start with `BUFA_` (the only reserved prefix). Any
  other name, `PATH` included, is legal and **overrides** the inherited or Bufa-set variable of that name.
* Values may reference earlier variables as `${NAME}`; `$${` is the one escape and writes a literal `${`. Expansion
  happens at script start against the environment assembled so far, in **document order**, so a value sees the
  inherited environment, Bufa's `BUFA_*` and platform variables, `.BUFA`'s `[env]`, the deps' `BUFA.env` layers, and
  its own table's earlier entries. A self-reference (`PATH = '${PATH};tools'`) chains on the value below. A reference
  to a name that a *later* entry of the `.BUFA`/`BUFA` pair defines or redefines is an error, and so is an
  undefined name. Names are looked up the way the OS does it (case-insensitively on Windows).
* `${NAME}` also expands inside `[[deps.ext]]` `url`s, at config read, from this dir's `[env]` only; a value that
  itself contains `${` may not be referenced from a url.
* `[windows.env]`/`[unix.env]`/`[linux.env]`/`[macos.env]` merge per exact key: a platform value replaces the shared
  one in its position, new keys follow.
* Unused variables are still resolved, so a missing file fails the build even if nothing reads the value.

A dir's `[env]` layers on top of `.BUFA`'s: an exact-key match overrides the root value, a match that differs only in
case is an error.

### `[filters]`

Each filter is a list of rules, applied to paths relative to the build dir.

#### Rules language

* Each rule is `+pattern` (include) or `-pattern` (exclude), matched case-insensitively.
* `*` matches within one path segment, `**` across segments; `?`, `[abc]`, and `{a,b}` work as usual.
  * `-src/*.c` excludes `src/main.c` but not `src/sub/deep.c`; `-src/**.c` excludes both.
  * `-test?.log` excludes `test1.log` but not `test10.log`.
  * `-*.[ch]` excludes `.c` and `.h` files; `-*.{png,jpg}` excludes both image types with one rule.
* A trailing `/` names a directory and everything below it (`dir/` is rewritten as `dir/**`).
  * `-build/` excludes `build/out.exe` and `build/obj/x.o`; `-build` excludes only a *file* named `build`.
* A pattern without a leading `/` matches at any depth (it is rewritten as `**/pattern`); a `/`-prefixed one is
  anchored at the build dir — *this* dir, never the project root, unlike a `/`-prefixed dep path.
  * `-tmp/` (becomes `-**/tmp/**`) excludes `tmp/` and `src/tmp/` alike; `-/tmp/` (becomes `-/tmp/**`) excludes only the
    top-level `tmp/`.
  * `-*.log` (becomes `-**/*.log`) excludes `a.log` and `logs/b.log`; `-/*.log` excludes only `a.log`.
* **The last matching rule wins.**
  * `["-build/", "+build/keep.txt"]` drops `build/` except `build/keep.txt`. Append `"-*.txt"` and `keep.txt` is
    dropped again: position matters, specificity does not.
* The first rule decides what happens to an unmatched path: a list opening with `-ruls` is a blacklist (unmatched ⇒
  included), list opening with `+rule` is a whitelist (unmatched ⇒ excluded).
  * `["-*.tmp"]` takes everything except `.tmp` files; `["+*.c", "+*.h"]` takes `.c` and `.h` files and nothing else.
  * `["+src/", "-src/gen/"]` takes only `src/`, minus `src/gen/`.
* Rules act on files and symlinks, never on directories: a directory is included when something in it is.
* A nested build dir is never part of this dir's source, whatever the rules say.

`[windows.filters]`/`[unix.filters]`/`[linux.filters]`/`[macos.filters]` lists are appended after the shared ones, so
a platform rule overrides a shared one for the paths it names.

#### `filters.src`

A list of rules deciding what counts as the dir's source, applied before hashing and staging. Also what a consumer
listing this dir in `deps.src` receives.\
Default: dot-prefixed names excluded at any depth (`-.**`). `BUFA` and `*.BUFA` are always excluded; no rule can add
them back.

#### `filters.bld`

A list of rules deciding what counts as build output, applied to the sandbox after the script ran (and succeeded):
excluded files are deleted, the rest is the output.\
Default: nothing excluded, the dir publishes verbatim, hidden files and empty dirs included.

`["-**"]` publishes nothing: right for a test runner or a "build everything" aggregator, where a successful (and cached)
run is the whole point and there is no output worth keeping.

#### `filters.dirty`

A list of rules deciding what the dirty-mode content hash sees, the only filter dirty mode uses. Same defaults as
`src`. Outputs written into the dir are hashed by default, which is what makes a deleted or hand-edited output
rebuild.

> [!CAUTION]
> A dirty build can overwrite or delete your source files: the script runs right in the real source tree, with no
> sandbox to protect it. That is no different from a plain `Makefile` or `build.sh`/`build.cmd` script.

### Platform Sections `[windows]` / `[unix]` / `[linux]` / `[macos]`

Each section has exactly the shape of the root table and is folded onto it for the running platform.
The sections form a tree, folded general to specific:

```
root
 ├─ [windows]
 └─ [unix]
     ├─ [linux]
     └─ [macos]
```

Windows folds `[windows]`; Linux folds `[unix]` then `[linux]`; macOS folds `[unix]` then `[macos]`; any other Unix
folds `[unix]` alone. All four are top-level tables (`[macos]`, not `[unix.macos]`). Each fold applies the same
rules, so a `[linux]` setting beats the `[unix]` one exactly as `[unix]` beats the root:

* **Scalars** (`cmd`, `shell`, `unsafe`, `largeOutput`, `deps.cacheDir`) replace the value so far when **present** in
  the section, whatever their value: an explicit `[windows] unsafe = false` beats a root `unsafe = true`.
* **Lists** (`deps.src`, `deps.bld`, `deps.export`, `[[deps.ext]]`, `filters.*`) are appended: root's, then
  `[unix]`'s, then the leaf's.
* **`[env]`** merges per exact key, the later section's value winning in the earlier key's position.

A section written only through subtables (`[windows.deps]`, `[[windows.deps.ext]]`) counts as present; `[linux]`
needs no `[unix]` beside it. There are no platform sections in `.BUFA` or `BUFA.shell`.

## `.BUFA` — project-wide settings

The `.BUFA` file at the project root is optional and may be empty: its presence alone anchors the project root and beats
every other marker. Without one, a `.git` or the topmost `BUFA` determine project's root; the full walk, its precedence
and what it remembers between runs are explained in the README's [Project Root](../README.md#project-root) section.

When `.BUFA` has content, it is TOML with these keys and nothing else (no platform sections):

| Key            | What                     | Type                | Default | Rebuilds every dir |
| -------------- | ------------------------ | ------------------- | ------- | ------------------- |
| `shell`        | project default shell    | string              | `""`    | yes                 |
| `[shells]`     | shell aliases            | table of alias→path | empty   | no                  |
| `[env]`        | project-wide env vars    | table of literals   | empty   | yes                 |
| `[unsafe].ttl` | unsafe rebuild interval  | duration            | `"15m"` | unsafe dirs only    |
| `allowBufaDir` | allow a `BUFA` directory | bool                | `false` | no                  |

The last column says which edits rebuild the whole project: Bufa's own version is also part of every dir's cache
key, so upgrading Bufa is a full rebuild too.

### `shell`

The default shell for every dir whose own `shell` is not specified. Stricter than a dir's: a non-empty value must be an
alias from `[shells]` or a `/`-prefixed provider dir path (no relative pahts). Preset names are not accepted here; leave
it empty for the native preset. Changing it rebuilds every dir.

### `[shells]`

Aliases for shell provider dirs. Keys are bare names, values are `/`-prefixed project-root-relative provider dirs;
relative values are rejected. An alias may shadow `cmd` or `bash`, which lets a project pin a versioned Bash while the
provider itself still builds under the real preset. Re-pointing an alias rebuilds exactly the dirs using it.

```toml
[shells]
nu   = "/build/nu"
pwsh = "/build/pwsh"
```

### `[env]`

Variables every build script in the project sees, in both modes. **Literals only**: a `{ file = … }` entry is a parse
error. Same name and value rules as a dir's `[env]` (one non-empty line, case-insensitively unique keys, no `BUFA_`
prefix), validated whenever Bufa reads the marker. Exported before the dir's `[env]`, so a dir may override a root
key exactly or chain on it with a self-reference. Root values are **not** expanded into `[[deps.ext]]` urls. The
whole table is part of every dir's cache key, so any edit rebuilds the project.

### `[unsafe].ttl`

The Time-To-Live of an `unsafe = true` build results: how long it may be trusted, as a Go duration string (`"24h"`,
`"90m"`, `"0"`). The cache key of every unsafe dir carries the build start time rounded down to this precision, so a new
invocation with different rounding result rebuilds. `0` means one bucket per invocation (millisecond precision, same-invocation hits
kept). Negative values are rejected. Changing it rebuilds unsafe dirs only.

### `allowBufaDir`

_A source *directory* named `BUFA` (any case) is normally reported as a reserved-name error when a walk meets it.
`allowBufaDir = true` treats such a directory as ordinary content. Needed only to build the Bufa itself, whose `cmd/bufa`
matches the name case-insensitively._

## Environment variables

### Read by Bufa

Set these in the environment `bufa` runs in:

* **`BUFA_BUILD_ROOT`** — puts the build store at `$BUFA_BUILD_ROOT/<project>.BUFA` instead of beside the project.
  Allows hosting build store farther away from source, or on faster storage.
  Required (must be set) when the project root is a filesystem root, where no sibling can exist (the store is then
  `$BUFA_BUILD_ROOT/.BUFA`).
* **`BUFA_NO_DAEMON`** — any non-empty value: never start or use the watcher daemon, like `--no-daemon` on every
  command. `bufa nuke` still stops a running daemon before deleting the store.
* **`BUFA_GLOBAL_CACHE_DIR`** — the location of the global `[[deps.ext]]` artifact cache. Default: the `bufa` subdir
  of the user cache dir — `%LOCALAPPDATA%\bufa` on Windows, `$XDG_CACHE_HOME/bufa` or `~/.cache/bufa` on Linux,
  `~/Library/Caches/bufa` on macOS.
* **`LOG_LEVEL`** — the log level when `--log-level` is not given: `none` (default, silent), `error`, `warn`, `info`,
  `debug`. Also read by the daemon process.
* **`LOG_SOURCE`** — `true` or `1`: append the `file:line` of each log record.

A `BUFA_BUILD_ROOT` seen by Bufa and the `BUFA_BUILD_ROOT` a script receives (below) share a name but not a job. The
name round-trips on purpose: a nested `bufa` invoked from a build script resolves its own store under the sandbox.

### Set for build scripts

Every script, in every mode, gets these:

* **`BUFA_BUILD_ROOT`** — absolute. In a clean build the sandbox root, `<store>/bld`; in a dirty build the project
  root.
* **`BUFA_BUILD_DIR`** — the build dir's project-relative path with OS separators, `.` for the root dir. Same in both
  modes.
* **`BUFA_CACHE_ROOT`** — `<store>/user`, absolute. Same in both modes, set whether or not the dir uses it.
* **`BUFA_CACHE_DIR`** — only for a dir with `deps.cacheDir = true`: the name of its persistent cache dir under
  `BUFA_CACHE_ROOT`, unique per requesting build dir. Same value in both modes.
* **`BUFA_COPY_OR_MOVE`** — the shell definition's `move` verb in a clean build, its `copy` verb in a dirty one.

`BUFA_BUILD_ROOT` joined with `BUFA_BUILD_DIR` is the script's cwd, and `BUFA_CACHE_ROOT` joined with `BUFA_CACHE_DIR`
is its cache dir.\
The root/relative split is deliberate: a script that bakes only the relative half into a generated
file (`%%BUFA_BUILD_ROOT%%\%BUFA_BUILD_DIR%`) produces byte-identical output on every machine.

`BUFA_COPY_OR_MOVE` is `move`/`copy` under `cmd`, `mv`/`cp` under `bash` and most custom shells, and absent when the
shell definition declares no verbs: use it to relocate dep files inside sandbox, since deps staged deps in a sandbox are
disposable copies but dirty-mode deps are the real source tree. **EXCEPT** the deps staged by link.

**What else the script sees** depends on the mode.

A clean, safe build (the default) starts from a scrubbed environment:

* **Inherited:** on Windows only `OS`, `SystemDrive`, `SystemRoot`, and `windir`; on Unix nothing at all.
* **`PATH`:** on Windows `%SystemRoot%\System32`, `%SystemRoot%`, `%SystemRoot%\System32\Wbem`, and
  `%SystemRoot%\System32\WindowsPowerShell\v1.0`; on Unix `/usr/bin:/bin:/usr/sbin:/sbin`.
* **`TEMP` and `TMP`** (Windows) or **`TMPDIR`** (Unix): the store's `tmp/` dir, recreated empty before every script,
  removed after a successful one and kept after a failure together with the sandbox.
* **`HOME`** (Unix): the same `tmp/` dir, a synthetic home wiped with it. Windows has no `HOME`.
* **`PATHEXT`** (Windows): `.COM;.EXE;.BAT;.CMD`, executables and batch files only.
* **`ComSpec`** (Windows): `%SystemRoot%\System32\cmd.exe`, regardless of system default.

A clean `unsafe = true` build inherits everything (`PATH`, `HOME`, `PATHEXT`, `ComSpec` included); only the temp
variables are still redirected to `tmp/`. A dirty build inherits everything verbatim and redirects nothing.

**Layering order.** The script environment is assembled in this order, each later layer overriding an earlier
namesake and each `${NAME}` reference seeing only what precedes it:

1. Inherited env variables (filtered as above).
2. The `BUFA_*` variables and the platform variables from the tables above.
3. `.BUFA`'s `[env]`, in document order.
4. Each direct `deps.bld` dep's published `BUFA.env`, in `deps.bld` order, the implied shell provider last.
5. The dir's `[env]` (platform-merged), in document order.

**Shell sessions.** `--shell` and `--post-shell` open the dir's shell with the identical cwd and environment plus one
prompt marker: the definition's `prompt.env` variable (`PROMPT` for `cmd`, `PS1` for `bash`) is set to `(bufa shell) `
followed by its previous value or the definition's `prompt.default`.

## `BUFA.env` — dependency-published variables

A build dir whose **published output build dir** contains a `BUFA.env` file exports its variables into the script
environment of every dir that lists it in `deps.bld` — the implied shell provider included, `deps.src` deps
excluded. Direct dependents only, no transitivity, and no way to opt out. The file may be static source in the
provider or written by its build script. An absent file is a silent no-op.

```shell
# BUFA.env published by /build/go
GOROOT=${BUFA_BUILD_ROOT}/build/go
PATH=${GOROOT}/bin;${PATH}
```

**Format.** Dotenv-style, UTF-8 (a BOM is stripped):

* `KEY=VALUE` per line, split at the **first** `=`; the value is everything after it, verbatim. No quoting, no
  escaping, no inline comments: a `#` inside a value is part of the value.
* Lines are trimmed, so CRLF and trailing spaces from `echo KEY=VAL >> BUFA.env` are harmless.
* Blank lines and lines starting with `#` (comments) are skipped.
* Hard errors, reported with line numbers: a line without `=`, an empty value, a duplicate key (case-insensitive), a
  key that is empty or starts with `BUFA_`.

**Semantics.**

* Variables are exported in file order, between `.BUFA`'s `[env]` and the consumer's `[env]`, deps in `deps.bld`
  order. Last wins, so a dep may override a root value and the consumer's `[env]` may override a dep's.
* Each file is a **closed unit**: `${NAME}` references resolve against everything below it (inherited, Bufa, root
  `[env]`, earlier deps) plus the file's own earlier lines, and a self-reference chains (`PATH=…;${PATH}`). A
  reference to a name the same file defines later is an error; the consumer's redefinitions never affect a dep's
  references.
* A later dep may redefine an earlier dep's variable only with the **identical value** or by **chaining** on it (its
  value must contain `${NAME}`). A plain overwrite is a hard error, since it would silently hide the earlier
  provider's tools.
* Values should address the provider's own published tree via `${BUFA_BUILD_ROOT}/<provider's project path>`, never
  `${BUFA_BUILD_DIR}`, which expands to the consumer's dir.
* The file is part of the provider's output, so editing it rebuilds every consumer. No other cache-key effect.
* A virtual `cmd = false` provider cannot publish one: it has no source and no script.

> Note: Windows PowerShell's `>>` writes UTF-16LE; generate the file with `Out-File -Encoding utf8` or from `cmd`/`bash`.

## `BUFA.shell` — shell definitions

Bufa runs a *definition*, not a shell: the executable, the script file extension, and the argv shapes for a build
and for the two interactive sessions. Two definitions are built in (`cmd`, `bash`); any other shell is a **provider
dir** that publishes a `BUFA.shell` in its output build dir, usually beside the unpacked binary.

```toml
exe       = "./nu"                                    # required
ext       = ".nu"                                     # required
run       = ["-n", "${script}"]                       # required
shell     = ["-n"]                                    # optional: --shell argv
postShell = ["-n", "-e", "source '${script}'"]        # optional: --post-shell argv
prompt    = { env = "PROMPT_COMMAND", default = "" }  # optional
move      = "mv"                                      # optional pair
copy      = "cp"
```

* **`exe`** (required) — the interpreter. `./x` or `bin/x` is relative to the provider's output dir. A bare `x` is
  looked up on **Bufa's** `PATH` and handed to the child as an absolute path (an ambient, unpinned shell). An
  absolute path is used as is. Windows needs no `.exe`.
* **`ext`** (required) — the script file's extension, `.`-prefixed, no separators. The script is written as
  `BUFA<ext>`; PowerShell requires `.ps1`.
* **`run`** (required) — the argv after `exe` for a build. Must contain `${script}`.
* **`shell`** — the argv for `--shell`. Absent ⇒ `--shell` on a dir using this shell fails at invocation. `[]` is a
  present, empty argv.
* **`postShell`** — the argv for `--post-shell`: the shell process itself runs the script and stays open. Must contain
  `${script}`. Absent ⇒ `--post-shell` fails at invocation.
* **`prompt`** — `{ env = "…", default = "…" }`: the session prompt variable and its fallback value. `default`
  needs `env`. Absent ⇒ no prompt marker.
* **`move`**, **`copy`** — the `BUFA_COPY_OR_MOVE` spellings for clean and dirty mode. Both or neither; neither ⇒
  the variable is not exported.

`${script}` is the script's absolute path and the only placeholder; it is substituted in every argv. Unknown keys are
errors. No platform sections: a shell that differs per OS is two provider dirs plus a per-dir `[windows]`/`[unix]`
`shell` override. Hermeticity flags (`-n`, `-NoProfile`, `--norc`) belong in the definition, not in Bufa.

**Presets.** Built in, with the flags Bufa always used:

| Name   | `ext`   | `run`             | `shell`  | `postShell`             | `prompt`          | verbs         |
| ------ | ------- | ----------------- | -------- | ----------------------- | ----------------- | ------------- |
| `cmd`  | `.cmd`  | `/D /C ${script}` | `/D /K`  | `/D /K ${script}`       | `PROMPT`, `$P$G`  | `move`/`copy` |
| `bash` | `.bash` | `${script}`       | `--norc` | `--rcfile ${script} -i` | `PS1`, `\s-\v\$ ` | `mv`/`cp`     |

`cmd` preset exists on Windows only; `bash` on every platform (but must be installed on Windows).

`cmd` is `%SystemRoot%\System32\cmd.exe` by absolute path; `/D` skips the AutoRun registry commands. `bash` is
whatever `bash` Bufa's own `PATH` finds (Git Bash or WSL on Windows).

**Provider dirs.** A provider is an ordinary build dir: typically a pinned archive as a non-exported `large`
`[[deps.ext]]`, a script that unpacks it under the *native* shell named explicitly (`[windows] shell = "cmd"`,
`[unix] shell = "bash"`), `BUFA.shell` as own source so it publishes beside the binary, and `largeOutput = true` so
consumers link it in. A definition-only provider (`cmd = false` plus a `BUFA.shell` with a bare `exe`) wraps a shell
installed on the machine. The definition is read from the provider's output at script time; an output without one, or
with a malformed one, fails at the consumer's script step. Both the binary and the definition are part of the
provider's output hash, so editing either rebuilds every consumer. The [Examples](../Examples/README.md) directory
holds working providers for Nushell, PowerShell 7, Windows PowerShell, Elvish, and prefix-dev/shell.
