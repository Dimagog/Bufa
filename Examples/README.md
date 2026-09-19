# Examples — custom shells, JDK, Go, Clojure and Rust toolchains

## Custom Shells

A runnable mini project: `.BUFA` makes Nushell the project-wide shell (`shell = "nu"`, an alias from its
`[shells]` catalogue — `shell = "/build/nu"` would work too). `build/nu`, `build/pwsh`, `build/elvish`, and
`build/shell` (prefix-dev/shell, a bash-compatible shell that runs natively on Windows) are hermetic shell
**provider** dirs (pinned archive → unpacked binary + `BUFA.shell` definition); `build/powershell` is a
definition-only provider for OS-installed `powershell.exe`. `hello/` declares one virtual consumer unit per flavor:

| Shell              | Example            | `shell` setting        | What it shows                                    |
| ------------------ | ------------------ | ---------------------- | ------------------------------------------------ |
| Nushell            | `hello/default`    | inherited              | one cross-platform `cmd` under the default       |
| Nushell            | `hello/raw`        | inherited              | the file is the script; can't open with `NAME=…` |
| PowerShell 7       | `hello/pwsh-alias` | `"pwsh"` (alias)       | a dir naming its shell through the catalogue     |
| PowerShell 7       | `hello/pwsh-path`  | `"/build/pwsh"` (path) | a dir naming its provider dir directly           |
| Windows PowerShell | `hello/powershell` | `"powershell"` (alias) | OS-installed, downloads nothing; Windows only    |
| Elvish             | `hello/elvish`     | `"elvish"` (alias)     | `--post-shell` feeds the script as the rc file   |
| prefix-dev/shell   | `hello/shell`      | `"shell"` (alias)      | one bash-like body, native on every platform     |

## Hermetic Toolchains

`build/java.BUFA`, `build/clj/`, `build/go.BUFA`, and `build/rust.BUFA` are hermetic **toolchain** providers, the
same shape minus the `BUFA.shell`: a pinned archive unpacked once (`largeOutput = true`) plus a `BUFA.env` the script
writes. A consumer lists the provider in `deps.bld` and finds the tools on `PATH`, with nothing else to declare:

| Language | Example | Provider                        | What it shows                                                 |
| -------- | ------- | ------------------------------- | ------------------------------------------------------------- |
| Java     | `java/` | `build/java.BUFA`               | **virtual** provider: one file, no dir, only a pinned archive |
| Clojure  | `clj/`  | `build/clj/`, `build/java.BUFA` | two providers: `BUFA.env` reaches direct dependents only      |
| Go       | `go/`   | `build/go.BUFA`                 | `deps.cacheDir`: one persistent cache shared by all consumers |
| Rust     | `rust/` | `build/rust.BUFA`               | three pins merged into one prefix; `filters.bld` trims output |

* `build/clj/` also shows an `export = true` ext dep (a pinned file published as is) and a `[windows.filters]` rule
  dropping a Unix-only source file.
* `build/clj/`'s cache dir holds `~/.m2`: the first `clj/` build fetches Clojure from Maven Central.
* `build/rust` is the one provider with an ambient requirement: Rust links with the platform's own linker (Visual
  Studio Build Tools on Windows, `cc` on Linux and macOS). So `buildall` leaves `rust/` out unless
  `BUFA_TEST_EXAMPLES_REQUIRED` is set, as in CI; build it with `bufa rust`.
* `rust/.cargo/config.toml` passes `/Brepro` to MSVC's `link.exe` for a reproducible binary; a `filters.src` rule
  brings `.cargo/` in, since dot entries are excluded by default.
* The tool-specific settings are explained in each provider's own comments.

## Running the Examples

Run everything from `Examples/`. Build outputs land in the sibling `Examples.BUFA/` store (git-ignored).

### Build One Example at a Time

```shell
bufa hello/default     # downloads + unpacks nu once, then runs the cmd under it
bufa hello/pwsh-alias  # same with PowerShell 7
bufa hello/powershell  # uses OS-installed powershell.exe; downloads nothing
bufa hello/elvish      # same with Elvish
bufa hello/shell       # same with prefix-dev/shell (a conda package: zip → zstd tarball; see below)
bufa java              # downloads + unpacks the JDK once, then javac + java under nu
bufa go                # same with the Go distribution: go build, then the binary
bufa clj               # same with the Clojure CLI, on the same JDK; first run fetches Clojure from Maven
bufa rust              # same with rustc + cargo: cargo build, then the binary; needs a linker installed
```

### Build Everything with a `buildall` Script

```shell
buildall.cmd         # every hello/* unit plus java, go, and clj (Windows)
bash buildall.sh     # same on Linux and macOS
nu buildall.nu       # same on any platform, if you have Nushell installed
elvish buildall.elv  # same on any platform, if you have Elvish installed
```

### Open an Interactive Shell in a Build Environment

```shell
bufa --shell hello/default  # an interactive nu session in its build environment
bufa --shell java           # same, with the JDK on PATH
```

## How Providers Pin Archives per Platform

Every provider pins its archives per platform — Windows x64, Linux x64, macOS arm64 — as
`[[windows.deps.ext]]`/`[[linux.deps.ext]]`/`[[macos.deps.ext]]` entries, while Linux and macOS share one `[unix]`
script. Where the platforms differ, a `[linux.env]`/`[macos.env]` value carries the difference into that shared
script: the conda build string in `build/shell`, the macOS app-bundle `Contents/Home` in `build/java`.

`build/shell` shows a tool pinned only to unpack another: a conda package is a zip of zstd tarballs, which Windows'
native `tar` opens but no stock Linux or macOS tool does. So on Unix the dir pins a second artifact — the static
`micromamba` binary — and its script runs `micromamba package extract`. Neither artifact is published: only the
shell and its `BUFA.shell` are.
