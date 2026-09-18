# Site

https://bufa.build pipeline. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md). Not a Go package; root `BUFA` src
filter never matches it.

- **Content lives elsewhere**: pages are `README.md`, `Doc/Reference.md`, `Examples/README.md`, in place (github.com
  renders them too). `build.py`'s `PAGES` + `STATIC_DIRS` (`Doc/Images`, minus `*.mmd`) = whole published set.
  Output paths keep legacy Jekyll URLs (`/`, `/Doc/Reference.html`, `/Examples/`).
- **Renderer is GitHub**: `GET /repos/{repo}/contents/{path}?ref={sha}` with `Accept: application/vnd.github.html`
  — exact repo-page HTML. `POST /markdown` is **not** equivalent: `markdown` mode drops alerts, `gfm` turns soft
  wraps into `<br>` and links `@mentions`. So only **pushed** commits render: locally `HEAD` (warns on uncommitted
  page edits; unpushed ref = 404), CI `$GITHUB_SHA`.
- Post-processing = regex over real tags (text-node `<`/`"` arrive escaped, so code samples can't match): unwrap
  `<article>`; strip `user-content-` from ids **and** footnote hrefs (github.com maps `#slug` onto it in JS);
  restore `data-canonical-src` over camo `src`; rewrite relative links — `PAGES` source → its output,
  `STATIC_DIRS` file → published copy, other repo path → github.com `blob`/`tree` on `master`, leading `/` = repo
  root.
- **Link check is the build's test**: missing file, link leaving repo, or `#fragment` absent from target's ids
  fails the build, all errors listed. Runs on PRs (build only, no deploy).
- Stdlib-only + PEP 723 header: `uv run Site/build.py [outDir] [--ref REF]` (default `Site/_site`, git-ignored) and
  CI's bare `python3` both work. Token: `$GH_TOKEN`/`$GITHUB_TOKEN`, else `gh auth token`, else anonymous. A
  dependency ⇒ add to header, switch CI to `uv run`.
- Out dir overwritten in place, never cleaned — stale local files persist; CI starts empty.
- `github-markdown.css`: **pristine** vendored `github-markdown-css` v5.9.0 — bump by replacing, never edit.
- `site.css`: page frame (body background hexes repeated — vendored vars are scoped to `.markdown-body`);
  `.markdown-heading` permalink rules (GitHub emits the anchor as the heading's **sibling**, which vendored
  `h2:hover .anchor` never matches); and the **one deliberate departure** from github.com — orange Warning alert
  title + border (owner's call), scoped to `.markdown-alert-warning`. Light-mode `#c24e00` is tuned: brighter
  washes out, `#bc4c00` reads as rust.
- `template.html`: `{{root}}` = relative path to site root, so the site works under a sub-path or local server.
- `CNAME` **not published** (Pages `build_type: workflow` ignores it); it's only `build.py`'s canonical-URL source.
