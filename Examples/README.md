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
bufa hello/shell            # same with prefix-dev/shell (a conda package: zip → zstd tarball, native tar reads both)
bufa java                   # downloads + unpacks the JDK once, then javac + java under nu
buildall.cmd                # every hello/* unit plus java (Windows)
bufa --shell hello/default  # an interactive nu session in its build environment
bufa --shell java           # same, with the JDK on PATH
```

Build outputs land in the sibling `Examples.BUFA/` store (git-ignored).
