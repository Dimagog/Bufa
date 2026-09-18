#!/usr/bin/env python3
# /// script
# requires-python = ">=3.9"
# dependencies = []
# ///
"""Build the bufa.build site from GitHub's own HTML rendering of the repo's Markdown. See Site/CLAUDE.md."""

import argparse
import html
import os
import posixpath
import re
import shutil
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

SITE = Path(__file__).resolve().parent
ROOT = SITE.parent

# repo-relative source -> site-relative output
PAGES = {
    "README.md": "index.html",
    "Doc/Reference.md": "Doc/Reference.html",
    "Examples/README.md": "Examples/index.html",
}
STATIC_DIRS = ["Doc/Images"]
SITE_FILES = ["github-markdown.css", "site.css"]

SITE_NAME = "Bufa"
DESCRIPTION = "BUFA - Build system For Adults"
BRANCH = "master"

TAG_RE = re.compile(r"<(a|img)\b[^>]*>")
URL_ATTR_RE = re.compile(r'\b(href|src)="([^"]*)"')
ID_RE = re.compile(r'<[^>]*?\bid="([^"]+)"')
ARTICLE_RE = re.compile(r"<article\b[^>]*>(.*)</article>", re.S)
H1_RE = re.compile(r"<h1\b[^>]*>(.*?)</h1>", re.S)
CAMO_RE = re.compile(r'\bsrc="[^"]*"([^>]*?)\s*\bdata-canonical-src="([^"]*)"')


def git(*args):
    return subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True, check=True).stdout.strip()


def find_token():
    for name in ("GH_TOKEN", "GITHUB_TOKEN"):
        if os.environ.get(name):
            return os.environ[name]
    try:
        return subprocess.run(["gh", "auth", "token"], capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


def fetch_rendered(repo, ref, src, token):
    url = f"https://api.github.com/repos/{repo}/contents/{urllib.parse.quote(src)}?ref={urllib.parse.quote(ref)}"
    req = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github.html",
        "X-GitHub-Api-Version": "2022-11-28",
        "User-Agent": "bufa-site-build",
    })
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req) as resp:
            return resp.read().decode("utf-8")
    except urllib.error.HTTPError as e:
        hint = " (GitHub renders pushed commits only: is this ref pushed?)" if e.code == 404 else ""
        sys.exit(f"build.py ERROR: GET {url}: HTTP {e.code}{hint}")


def clean(rendered, src):
    m = ARTICLE_RE.search(rendered)
    if not m:
        sys.exit(f"build.py ERROR: {src}: no <article> in GitHub's response")
    body = m.group(1)
    # github.com maps #slug onto these ids in JS; a static page needs the bare id
    body = body.replace('id="user-content-', 'id="').replace('href="#user-content-', 'href="#')
    return CAMO_RE.sub(lambda m: f'src="{m.group(2)}"{m.group(1)}', body)


def rel_url(from_out, to_out):
    rel = posixpath.relpath(to_out, posixpath.dirname(from_out) or ".")
    if posixpath.basename(to_out) == "index.html":
        rel = posixpath.dirname(rel)
        return rel + "/" if rel else "./"
    return rel


def rewrite_links(body, src, out, ids, repo, errors):
    def fix(url):
        if not url or url.startswith("//") or urllib.parse.urlsplit(url).scheme:
            return url
        path, _, frag = url.partition("#")
        path = path.partition("?")[0]
        if not path:
            if frag and frag not in ids[src]:
                errors.append(f"{src}: dangling anchor #{frag}")
            return url
        path = urllib.parse.unquote(path)
        # GitHub resolves a leading / against the repo root
        target = posixpath.normpath(path[1:] if path.startswith("/") else posixpath.join(posixpath.dirname(src), path))
        if target == "." or target.startswith(".."):
            errors.append(f"{src}: link leaves the repo: {url}")
            return url
        suffix = "#" + frag if frag else ""
        if target in PAGES:
            if frag and frag not in ids[target]:
                errors.append(f"{src}: dangling anchor {url}")
            return rel_url(out, PAGES[target]) + suffix
        if any(target == d or target.startswith(d + "/") for d in STATIC_DIRS):
            if not (ROOT / target).is_file():
                errors.append(f"{src}: missing file {url}")
            return rel_url(out, target) + suffix
        if not (ROOT / target).exists():
            errors.append(f"{src}: missing file {url}")
            return url
        kind = "tree" if (ROOT / target).is_dir() else "blob"
        return f"https://github.com/{repo}/{kind}/{BRANCH}/{urllib.parse.quote(target)}{suffix}"

    def fix_tag(m):
        return URL_ATTR_RE.sub(lambda a: f'{a.group(1)}="{html.escape(fix(html.unescape(a.group(2))))}"', m.group(0))

    return TAG_RE.sub(fix_tag, body)


def page_title(body, src):
    m = H1_RE.search(body)
    if not m:
        sys.exit(f"build.py ERROR: {src}: no <h1> to title the page with")
    return html.unescape(re.sub(r"<[^>]+>", "", m.group(1))).strip()


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("out", nargs="?", default=str(SITE / "_site"), help="output dir (default: Site/_site)")
    ap.add_argument("--ref", default=os.environ.get("GITHUB_SHA"), help="pushed git ref to render (default: HEAD)")
    ap.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", "Dimagog/Bufa"))
    args = ap.parse_args()

    ref = args.ref or git("rev-parse", "HEAD")
    if not args.ref and git("status", "--porcelain", "--", *PAGES):
        print("build.py WARNING: uncommitted page edits are not rendered: GitHub renders pushed commits only",
              file=sys.stderr)

    token = find_token()
    domain = (SITE / "CNAME").read_text(encoding="utf-8").strip()
    template = (SITE / "template.html").read_text(encoding="utf-8")

    bodies = {src: clean(fetch_rendered(args.repo, ref, src, token), src) for src in PAGES}
    ids = {src: set(ID_RE.findall(body)) for src, body in bodies.items()}

    errors = []
    out_root = Path(args.out)
    for src, out in PAGES.items():
        body = rewrite_links(bodies[src], src, out, ids, args.repo, errors)
        fields = {
            "title": html.escape(f"{page_title(body, src)} | {SITE_NAME}"),
            "site_name": html.escape(SITE_NAME),
            "description": html.escape(DESCRIPTION),
            "canonical": f"https://{domain}/" + ("" if rel_url("x", out) == "./" else rel_url("x", out)),
            "root": posixpath.relpath(".", posixpath.dirname(out) or ".") + "/",
            "content": body,
        }
        page = re.sub(r"\{\{(\w+)\}\}", lambda m: fields[m.group(1)], template)
        dest = out_root / out
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(page, encoding="utf-8", newline="\n")
        print(f"{src} -> {out}")

    if errors:
        sys.exit("build.py ERROR: broken links:\n  " + "\n  ".join(errors))

    for d in STATIC_DIRS:
        shutil.copytree(ROOT / d, out_root / d, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.mmd"))
    for f in SITE_FILES:
        shutil.copyfile(SITE / f, out_root / f)


if __name__ == "__main__":
    main()
