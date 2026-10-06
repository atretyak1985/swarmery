#!/usr/bin/env python3
"""Build the paste-ready HTML for one Substack article.

    python3 build-substack.py articles/substack/<slug> [--no-fetch]

Writes build/substack.html, build/manifest.json and a copy of the editor helper
into the article directory, and prints the manifest: what the draft Substack
saves must contain. Substack's editor drops what it cannot hold without an
error, so everything it is known to drop or refuse fails the build here instead.
"""

from __future__ import annotations

import argparse
import html
import json
import re
import shutil
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from html.parser import HTMLParser
from pathlib import Path

SUBTITLE_MAX = 255  # Above this Substack refuses to save the whole draft.
HELPER = Path(__file__).resolve().parent / "substack-editor.js"


@dataclass
class Scan:
    tags: set[str] = field(default_factory=set)
    links: set[str] = field(default_factory=set)
    images: list[tuple[str, str]] = field(default_factory=list)  # (src, alt)
    code_block_lines: list[int] = field(default_factory=list)
    code_links: int = 0


class Scanner(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.scan = Scan()
        self._pre: list[str] | None = None
        self._in_link = False

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attr = dict(attrs)
        self.scan.tags.add(tag)
        if tag == "a":
            self._in_link = True
            href = attr.get("href") or ""
            if re.match(r"https?:", href):
                self.scan.links.add(href)
        elif tag == "img":
            self.scan.images.append((attr.get("src") or "", attr.get("alt") or ""))
        elif tag == "pre":
            self._pre = []
        elif tag == "code" and self._in_link:
            self.scan.code_links += 1

    def handle_endtag(self, tag: str) -> None:
        if tag == "a":
            self._in_link = False
        elif tag == "pre" and self._pre is not None:
            text = "".join(self._pre).rstrip("\n")
            self.scan.code_block_lines.append(len(text.split("\n")))
            self._pre = None

    def handle_data(self, data: str) -> None:
        if self._pre is not None:
            self._pre.append(data)


def scan_html(body: str) -> Scan:
    scanner = Scanner()
    scanner.feed(body)
    scanner.close()
    return scanner.scan


def unquote_value(value: str) -> str:
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1].replace('\\"', '"')
    return value


def split_front_matter(text: str) -> tuple[dict[str, str], str]:
    match = re.match(r"---\n(.*?)\n---\n(.*)", text, re.S)
    if not match:
        return {}, text
    meta: dict[str, str] = {}
    for line in match.group(1).splitlines():
        key, sep, value = line.partition(":")
        if sep and re.fullmatch(r"[A-Za-z_]+", key.strip()):
            meta[key.strip()] = unquote_value(value.strip())
    return meta, match.group(2)


def to_html(markdown: str) -> str:
    base = ["pandoc", "--from=gfm", "--to=html", "--wrap=none"]
    stderr = ""
    # Highlighting wraps every code line in spans and anchors, which would be
    # pasted as text and counted as links. The flag was renamed in pandoc 3.8.
    for flag in ("--syntax-highlighting=none", "--no-highlight"):
        try:
            run = subprocess.run([*base, flag], input=markdown, capture_output=True, text=True)
        except FileNotFoundError:
            sys.exit("pandoc is not installed (brew install pandoc)")
        if run.returncode == 0:
            return run.stdout
        stderr = run.stderr.strip()
    sys.exit(f"pandoc failed: {stderr}")


def git(args: list[str], cwd: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)


class ImageHost:
    """Maps a committed file to the URL GitHub serves it at for this commit.

    The URL is pinned to the commit, so it cannot serve a stale copy of an
    image that was just replaced, which a branch URL does for several minutes.
    """

    def __init__(self, article_dir: Path) -> None:
        top = git(["rev-parse", "--show-toplevel"], article_dir)
        if top.returncode != 0:
            sys.exit(f"{article_dir} is not inside a git repository")
        self.root = Path(top.stdout.strip()).resolve()
        origin = git(["remote", "get-url", "origin"], self.root).stdout.strip()
        match = re.search(r"github\.com[:/]([^/]+)/(.+?)(?:\.git)?$", origin)
        if not match:
            sys.exit(f"origin is not a GitHub remote ({origin or 'none'}); Substack needs a public image URL")
        sha = git(["rev-parse", "HEAD"], self.root).stdout.strip()
        self.base = f"https://raw.githubusercontent.com/{match[1]}/{match[2]}/{sha}"

    def url_for(self, path: Path) -> str | None:
        """The public URL, or None when the file is not committed as it stands."""
        try:
            rel = path.relative_to(self.root).as_posix()
        except ValueError:
            return None
        tracked = git(["ls-files", "--error-unmatch", "--", rel], self.root).returncode == 0
        unchanged = git(["diff", "--quiet", "HEAD", "--", rel], self.root).returncode == 0
        return f"{self.base}/{urllib.parse.quote(rel)}" if tracked and unchanged else None


def fetch_failure(url: str) -> str | None:
    request = urllib.request.Request(url, method="HEAD", headers={"User-Agent": "Mozilla/5.0"})
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return None if response.status == 200 else f"HTTP {response.status}"
    except urllib.error.HTTPError as error:
        return f"HTTP {error.code}"
    except (urllib.error.URLError, TimeoutError) as error:
        return str(getattr(error, "reason", error))


def host_images(body: str, scan: Scan, article_dir: Path, fetch: bool, problems: list[str]) -> str:
    host: ImageHost | None = None
    for src, alt in scan.images:
        if not alt.strip():
            problems.append(f"image {src}: no alt text")
        url = src
        if not re.match(r"https?:", src):
            path = (article_dir / urllib.parse.unquote(src)).resolve()
            if not path.is_file():
                problems.append(f"image {src}: no such file")
                continue
            host = host or ImageHost(article_dir)
            hosted = host.url_for(path)
            if hosted is None:
                problems.append(f"image {src}: not committed as it stands; Substack fetches it from GitHub, so commit and push it")
                continue
            url = hosted
            body = body.replace(f'src="{html.escape(src)}"', f'src="{html.escape(url)}"')
        if fetch and (failure := fetch_failure(url)):
            problems.append(f"image {src}: {url} answered {failure}; push the commit (a private repository cannot serve it at all)")
    return body


def check_structure(scan: Scan, problems: list[str]) -> None:
    if "table" in scan.tags:
        problems.append("table: Substack's editor has no table element; use a list or an image of the table")
    if "h1" in scan.tags:
        problems.append("a `# ` heading in the body: the title is its own field, taken from front matter")
    if scan.tags & {"h4", "h5", "h6"}:
        problems.append("a heading below `###`: the editor helper turns h4 into h3, so the levels would collide")


def main() -> int:
    parser = argparse.ArgumentParser(description="Build the paste-ready HTML for one Substack article.")
    parser.add_argument("article_dir", type=Path, help="articles/substack/<slug>")
    parser.add_argument("--no-fetch", action="store_true", help="skip checking that each image URL answers 200")
    args = parser.parse_args()

    article_dir: Path = args.article_dir.resolve()
    source = article_dir / "article.md"
    if not source.is_file():
        sys.exit(f"{source}: no such file")
    build_dir = article_dir / "build"
    shutil.rmtree(build_dir, ignore_errors=True)  # A failed build must not leave an older one to paste.

    meta, markdown = split_front_matter(source.read_text(encoding="utf-8"))
    title, subtitle = meta.get("title", ""), meta.get("description", "")
    problems: list[str] = []
    if not title:
        problems.append("front matter: no title")
    if not subtitle:
        problems.append("front matter: no description (it becomes the subtitle)")
    elif len(subtitle) > SUBTITLE_MAX:
        problems.append(f"front matter: description is {len(subtitle)} characters; Substack saves at most {SUBTITLE_MAX}")

    body = to_html(markdown)
    scan = scan_html(body)
    check_structure(scan, problems)
    body = host_images(body, scan, article_dir, not args.no_fetch, problems)

    if problems:
        print(f"{source}: {len(problems)} problem(s), nothing built", file=sys.stderr)
        for problem in problems:
            print(f"  - {problem}", file=sys.stderr)
        return 1

    manifest = {
        "title": title,
        "subtitle": subtitle,
        "links": sorted(scan.links),
        "codeBlockLines": scan.code_block_lines,
        "images": len(scan.images),
        "codeLinks": scan.code_links,
    }
    build_dir.mkdir()
    page = f'<!doctype html>\n<html><head><meta charset="utf-8"><title>{html.escape(title)}</title></head>\n<body>\n{body}</body></html>\n'
    (build_dir / "substack.html").write_text(page, encoding="utf-8")
    (build_dir / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    shutil.copyfile(HELPER, build_dir / HELPER.name)
    print(json.dumps(manifest, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
