# ArtifactCache

Package guidance. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md).

- Global content-addressed cache of `[[deps.ext]]` artifacts (`Doc/Specs/ExternalDeps.md`), per-user, outliving any
  build root — `bufa gc` never touches it, nothing evicts. Public API: `GetCacheDir()` (`$BUFA_GLOBAL_CACHE_DIR`;
  else `os.UserCacheDir()`'s `bufa` subdir), `New(dir)` (a `*BasePathFs` like the project store's — `RealPath` link
  targets), `Ensure(url, expectedHash, out) string` (abs OS entry path; a hit needs **both** the `F` entry and the
  url's link targeting it, and is then the proof: zero network, zero verification, zero output; anything less —
  including an entry missing its url link, a crashed run's publish window — fetches and re-verifies),
  `EnsureURLBinding(url, expectedHash, out)` (same minus the `F` presence probe — for a caller that already proved
  the entry, dirty's current `large` placement), `Contains(hash)`, `EntryPath(hash)`, `Dir()`, `Owns(target)` (abs
  OS path points into the cache — dirty mode telling a stale-pin cache link from a foreign one), `Fs()`,
  `QueryHashSentinel="?"`, `Nuke()` (`bufa nuke --global`/`--global-only`: the one sanctioned delete path —
  user-requested, **whole-cache**, never eviction; `Store.NukeDir` with the cache-owned predicate: regular files
  must be `IsValidFileHash` entries or `fetch-` staging leftovers, symlinks must be `url-` staging leftovers or url
  links — **any symlink targeting an `F` entry name**, the link's own name never consulted; anything else refuses;
  `NukeDir`'s `RemoveAll` clears the read-only attribute on Windows, so the insert-time chmod needs no separate
  pass). Entry names are their verified content hash, so hash walks fold a cache link's target basename instead of
  streaming the referent (Hashing's trusted-link shortcut).
- Layout: `F<hash>` verified files; `<encoded url>` → `F<hash>` **url links** (`urlLinkName`: the url text with
  Windows-forbidden filename chars swapped for look-alike Unicode — `/`→`∕`, `:`→`꞉`, `?`→`？`, …; an encoding
  exceeding `maxUrlNameLen` is trimmed at a rune boundary with `_U<hash(url)>` (`Hashing.HashUrl`) replacing the
  tail, so name + staging wrapper stay under every FS's 255-byte cap; on a case-insensitive FS two urls differing
  only by case share one name — benign only because a hit also requires the link to target the very entry both pin;
  relative sibling target = the entry name; several urls may link one entry); plus transient staging entries **at
  the cache root** (same dir ⇒ same volume ⇒ atomic rename; the `fetch-*` and `url-*` prefixes keep them outside the
  entry/url-link namespace), each download staged under a unique `TempFile` name (concurrent processes share the
  cache, no daemon, no locks; lost publish race ⇒ discard; entries chmod'd read-only at insert). A url link stages as
  `url-<link-name>-<pid>-<seq>` (seq = process-wide atomic counter) and renames **over** any old link (`os.Symlink`
  refuses to overwrite, `os.Rename` replaces — a re-pinned url re-points); every verified insert writes it, the
  bootstrap included. Url links are real OS symlinks, so **every fetch needs file-symlink rights**; a warm hit needs
  none.
- **Admission gate**: the only writers verify-at-insert (stream-hash via `Hashing.FileHasher` while downloading), and
  **every complete download is admitted under its *computed* key + url link** — the entry name is its true content
  hash, and nothing consumes it without pinning exactly that key. Mismatch ⇒ loud fail naming url + expected +
  **computed** hash *after* the admit, so the re-pin is free: pin the printed hash and the next build is a pure
  hit. Nothing ever enters under the *expected* pin. A wrong pin left unfixed re-downloads on **every** retry by
  design: a `url → F<X>` link under pin `Y` is indistinguishable from a re-pinned mutable url, so the url link is
  never a fail-fast gate; instead, when the link already targeted the computed hash before the fetch (the url
  re-downloaded unchanged), the mismatch error appends a fix-the-pin hint. `QueryHashSentinel` ⇒ the same
  admit-then-fail printing the hash — never auto-trust silently. A real download prints `Downloading <url>` to `out`
  regardless of log level.

## Cross-package contracts

Each binds this package to another; changing either side breaks the other with no compiler, `go vet`, or test error.

- [Build](../Build/CLAUDE.md) — the pin string doubles as the **cache-relative entry path**
  (`Store.CopyFileIn(cache.Fs(), dep.Hash, …)`) and as link **identity** evidence: dirty `placeExtDeps` tells bufa's
  own placement from a foreign one by comparing a symlink target to `EntryPath(dep.Hash)` and via
  `Owns`/`Contains`; a current `large` placement then calls `EnsureURLBinding`, a current copy the full `Ensure`, so
  an edited url can't bypass the url-link gate in dirty mode either. Sharding or renaming entries turns every
  previously placed dirty artifact into a hard "occupied by a foreign symlink" error in the user's **live source
  tree**.
- [Store](../Store/CLAUDE.md) — a `large` + `export` ext dep ships into `out/<D…>` as a symlink object pointing in
  here, so a published store tree depends on this cache being immutable, never evicting, and untouched by `bufa gc`.
  `Nuke` deliberately breaks that, user-requested and whole-cache: every project's published `large` links dangle
  until their next fetch + rebuild (a dangling link is a hard error in hash walks, so `bufa check` reports it rather
  than serving it). Partial eviction remains forbidden.
- [Hashing](../Hashing/CLAUDE.md) — url links are named by `urlLinkName`'s encoded url text, owned here; `HashUrl`
  supplies only the `_U<hash(url)>` tail of trimmed overlong names. Entry names are `F<verified content hash>`,
  exactly the invariant the trusted-link shortcut rests on: a name that could ever be untruthful would fold into the
  very key under verification.
