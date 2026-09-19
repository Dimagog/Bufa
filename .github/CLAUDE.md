# GitHub workflows

## Release notes

New release notes list each commit's first line prefixed with `* ` since the last published release, oldest first
(all history for the first release).
Releases already created in the UI keep their notes and only receive assets.

## Examples job

`examples` (ubuntu/windows/macos) builds bufa, runs `Examples/buildall.sh` (`buildall.cmd` on Windows), then
`go test ./Build -run '^TestExamples_'`, which no longer skips because `buildall` filled the Global Artifact Cache —
and cannot: that step sets `BUFA_TEST_EXAMPLES_REQUIRED`, which fails every skip but a unit with no command on the
platform (`hello/powershell` off Windows). The variable, not `$CI`, is the switch: the `test` job runs on CI too, with
a cold cache and no network, and must keep skipping.
It is the only pipeline that executes the platform sections of the Examples' configs — a new pin or a `[macos]`
edit is unverified until it runs.

* `actions/cache` holds the Global Artifact Cache (`$BUFA_GLOBAL_CACHE_DIR` under `$RUNNER_TEMP`), keyed by OS,
  arch, and the hash of every Examples `BUFA`/`*.BUFA`. Deliberately **no `restore-keys`**: any config edit starts
  from an empty cache and re-downloads everything once, so archives of dropped pins never accumulate, and the fetch +
  verify path still gets exercised on each pin change.
* A cache hit needs the entry **and** its url symlink ([ArtifactCache](../ArtifactCache/CLAUDE.md)), whose name holds
  look-alike Unicode. On Windows `actions/cache` packs with Git's MSYS tar, which deep-copies symlinks unless
  `MSYS=winsymlinks:nativestrict` — set on the cache step. A link lost anyway degrades to a re-download (a
  `Downloading <url>` line in a warm run's log is the tell), never a failure.
* **A release gate** (`release` needs it): it is the only end-to-end run of real shells and of the Linux/macOS
  platform sections, so a tag must not publish past a break there. It is also the only job depending on third-party
  hosts — on a cache miss (pin change, or GitHub's 7-day eviction) an upstream outage fails it; re-run the failed job
  and `release` proceeds, no re-tag needed.
* No tool-install steps: every example is hermetic on the runners' stock images (`build/shell` pins its own
  unpacker on Unix), and a new ambient requirement should be fixed in the example, not papered over here.
* `buildall.sh` runs as `bash buildall.sh`, so it needs no exec bit in the repo.
