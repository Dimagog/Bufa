# Site

The https://bufa.build pipeline. Repo-wide conventions: [CLAUDE.md](../CLAUDE.md). Not a Go package: nothing here
is seen by `go build`/`vet`/`test`, and the root `BUFA` src filter never matches it.

- **Content does not live here.** The pages are `README.md`, `Doc/Reference.md`, and `Examples/README.md`, in place,
  because github.com renders them there too. `Site/` is machinery only. `build.py`'s `PAGES` is the whole published
  set (plus `STATIC_DIRS` = `Doc/Images`, minus `*.mmd`); nothing else in the repo reaches the site. Output paths keep
  the legacy Jekyll URLs (`/`, `/Doc/Reference.html`, `/Examples/`).
- **The renderer is GitHub itself**: `GET /repos/{repo}/contents/{path}?ref={sha}` with
  `Accept: application/vnd.github.html` — the exact HTML of the repo page (alerts, heading permalinks with GitHub's
  own slugs, soft line breaks kept). `POST /markdown` is **not** equivalent in either mode: `markdown` renders no
  alerts, `gfm` renders them but turns every soft wrap into `<br>` and links `@mentions`. Consequence: only **pushed**
  commits render. Locally the script renders `HEAD` and warns when page sources have uncommitted edits; an unpushed
  ref is a 404. CI passes `$GITHUB_SHA` (on a PR, the merge commit).
- `build.py` post-processing, all of it regex over real tags (text-node `<`/`"` arrive escaped, so a tag-scoped
  regex cannot match inside code samples): unwrap the `<article>`; strip the `user-content-` id prefix (github.com
  maps `#slug` onto it in JS; a static page needs the bare id — footnote hrefs carry the prefix too, so both sides
  are stripped); restore `data-canonical-src` over the camo proxy `src`; rewrite relative links — a `PAGES` source →
  its output (an `index.html` target → its directory), a `STATIC_DIRS` file → the published copy, any other existing
  repo path → a github.com `blob`/`tree` URL on `master`, a leading `/` resolved against the repo root as GitHub does.
- **Link check is the build's test**: a relative link to a missing file, a link leaving the repo, or a `#fragment`
  absent from the target page's ids fails the build, all errors listed. It runs on PRs (build job only, no deploy).
- Stdlib-only with a PEP 723 header, so `uv run Site/build.py [outDir] [--ref REF]` (default out `Site/_site`,
  git-ignored) and the runner's bare `python3` both work with no setup step. Token: `$GH_TOKEN`/`$GITHUB_TOKEN`, else
  `gh auth token`, else anonymous. A dependency, if ever needed, goes in the header and CI switches to `uv run`.
- The out dir is overwritten in place, never cleaned — a stale local file can outlive its source; CI always starts
  empty.
- `github-markdown.css` is a **pristine** vendored `github-markdown-css` (v5.9.0, MIT, sindresorhus): bump by
  replacing the file, never edit it. `site.css` holds the page frame (980px column, body background per color scheme
  — the vendored vars are scoped to `.markdown-body`, so the two hex values are repeated) and the `.markdown-heading`
  permalink rules the vendored file lacks: GitHub now emits the anchor as the heading's **sibling**, which the
  vendored `h2:hover .anchor` rules never match.
- `template.html` — `{{title}}` (`<h1 text> | Bufa`), `{{description}}`, `{{site_name}}`, `{{canonical}}`,
  `{{root}}` (relative path to the site root, so the site also works under a sub-path or a local server),
  `{{content}}`.
- `CNAME` is copied to the site root and is the source of the canonical-URL domain. Pages is in
  `build_type: workflow` mode ([site.yml](../.github/workflows/site.yml): build on push/PR touching the page sources
  or `Site/**`, deploy from `master` only); the custom domain itself lives in the repo's Pages settings.
