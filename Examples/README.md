# Examples — custom shells and a JDK toolchain

A runnable mini project: `.BUFA` makes Nushell the project-wide shell (`shell = "nu"`, an alias from its
`[shells]` catalogue — `shell = "/build/nu"` would work too). `build/nu`, `build/pwsh`, `build/elvish`, and
`build/shell` (prefix-dev/shell, a bash-compatible shell that runs natively on Windows) are hermetic shell
**provider** dirs (pinned archive → unpacked binary + `BUFA.shell` definition); `build/powershell` is a
definition-only provider for OS-installed `powershell.exe`. `hello/` declares one virtual consumer unit per flavor:

| unit                | shell                          | what it shows                                                                  |
| ------------------- | ------------------------------ | ------------------------------------------------------------------------------ |
| `hello/default`     | inherited (`nu`)               | one cross-platform `cmd` under the default                                     |
| `hello/raw`         | inherited (`nu`)               | the whole file is the script — a raw-script BUFA (must not open with `NAME=…`) |
| `hello/pwsh-alias`  | `shell = "pwsh"` (alias)       | a dir naming its shell through the catalogue                                   |
| `hello/pwsh-path`   | `shell = "/build/pwsh"` (path) | a dir naming its provider dir directly                                         |
| `hello/powershell`  | `shell = "powershell"` (alias) | OS-installed Windows PowerShell (Windows only)                                 |
| `hello/elvish`      | `shell = "elvish"` (alias)     | Elvish; `--post-shell` feeds the script as the rc file                         |
| `hello/shell`       | `shell = "shell"` (alias)      | prefix-dev/shell: one bash-like body, native on every platform                 |

`build/jdk.BUFA` is a **toolchain** provider, the same shape minus the `BUFA.shell`, and a **virtual** build dir: the
one file declares `build/jdk` without a directory, since everything it needs comes from its pinned archive. A Temurin
JDK, unpacked once, `largeOutput = true`, and a `BUFA.env` its script writes with `JAVA_HOME` and a `PATH` that chains
`jdk/bin` on. `java/` is its consumer: one `deps.bld = ["/build/jdk"]` line, and its script has `javac` and `java` on
`PATH`.

```shell
cd Examples
bufa hello/default          # downloads + unpacks nu once, then runs the cmd under it
bufa hello/pwsh-alias       # same with PowerShell 7
bufa hello/powershell       # uses OS-installed powershell.exe; downloads nothing
bufa hello/elvish           # same with Elvish
bufa hello/shell            # same with prefix-dev/shell (a conda package: zip → zstd tarball; see below)
bufa java                   # downloads + unpacks the JDK once, then javac + java under nu
buildall.cmd                # every hello/* unit plus java (Windows)
bash buildall.sh            # same on Linux and macOS
nu buildall.nu              # same on any platform, if you have Nushell installed
elvish buildall.elv         # same on any platform, if you have Elvish installed
bufa --shell hello/default  # an interactive nu session in its build environment
bufa --shell java           # same, with the JDK on PATH
```

Build outputs land in the sibling `Examples.BUFA/` store (git-ignored).

Every provider pins one archive per platform — Windows x64, Linux x64, macOS arm64 — as
`[[windows.deps.ext]]`/`[[linux.deps.ext]]`/`[[macos.deps.ext]]` entries, while Linux and macOS share one `[unix]`
script. Where the platforms differ, a `[linux.env]`/`[macos.env]` value carries the difference into that shared
script: the conda build string in `build/shell`, the macOS app-bundle `Contents/Home` in `build/jdk`.

`build/shell` shows a tool pinned only to unpack another: a conda package is a zip of zstd tarballs, which Windows'
native `tar` opens but no stock Linux or macOS tool does. So on Unix the dir pins a second artifact — the static
`micromamba` binary — and its script runs `micromamba package extract`. Neither artifact is published: only the
shell and its `BUFA.shell` are.
