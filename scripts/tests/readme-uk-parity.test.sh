#!/bin/bash
# readme-uk-parity — README.uk.md must stay structurally in step with README.md.
#
# README.uk.md is a hand-written translation, so nothing regenerates it when the
# English README grows a section. This suite is the tripwire: the same number of
# `## ` and `### ` headings and fenced blocks, the docgen-managed pack table in
# both files (same rows, so the same packs), the language switch in both, and
# every in-page anchor in the Ukrainian file pointing at a heading or an
# explicit `<a id>` that really exists under GitHub's slug rule.
#
# Offline and framework-free. Run with `bash scripts/tests/readme-uk-parity.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EN="${ROOT}/README.md"
UK="${ROOT}/README.uk.md"

pass=0
fail=0

ok()  { pass=$((pass + 1)); printf '  ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL  %s\n        %s\n' "$1" "$2"; }

# eq <desc> <expected> <actual>
eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected [$2], got [$3]"; fi; }
# has <desc> <haystack> <needle>
has() { case "$2" in *"$3"*) ok "$1" ;; *) bad "$1" "missing [$3]" ;; esac; }

# count <file> <regex> — number of matching lines (0 when none, never an error).
count() { grep -c -- "$2" "$1" || true; }

# region_rows <file> — table rows strictly between the generated:packs markers.
region_rows() {
  sed -n '/<!-- BEGIN generated:packs -->/,/<!-- END generated:packs -->/p' "$1" |
    grep -c '^|' || true
}

echo "readme-uk-parity"

if [ ! -f "$UK" ]; then
  bad "README.uk.md exists" "no file at ${UK}"
  printf 'readme-uk-parity: %d passed, %d failed\n' "$pass" "$fail"
  exit 1
fi
ok "README.uk.md exists"

# ── structure ───────────────────────────────────────────────────────────────
eq "same number of '## ' headings"  "$(count "$EN" '^## ')"  "$(count "$UK" '^## ')"
eq "same number of '### ' headings" "$(count "$EN" '^### ')" "$(count "$UK" '^### ')"
eq "same number of fence lines"     "$(count "$EN" '^```')"  "$(count "$UK" '^```')"

# ── the generated pack table ────────────────────────────────────────────────
for f in "$EN" "$UK"; do
  name="$(basename "$f")"
  eq "${name}: one BEGIN generated:packs marker" "1" "$(count "$f" '<!-- BEGIN generated:packs -->')"
  eq "${name}: one END generated:packs marker"   "1" "$(count "$f" '<!-- END generated:packs -->')"
done
eq "README.uk.md: Ukrainian table header" "1" "$(count "$UK" '^| Плагін | Що всередині |$')"
eq "both tables carry the same number of rows" "$(region_rows "$EN")" "$(region_rows "$UK")"

# ── the language switch ─────────────────────────────────────────────────────
eq "README.md links README.uk.md exactly once"  "1" "$(count "$EN" 'README.uk.md')"
eq "README.uk.md links README.md exactly once"  "1" "$(count "$UK" '](README.md)')"
has "README.md switch line" "$(cat "$EN")" '**English** · [Українська](README.uk.md)'
has "README.uk.md switch line" "$(cat "$UK")" '[English](README.md) · **Українська**'

# ── in-page anchors resolve ─────────────────────────────────────────────────
# GitHub's slug rule: lowercase, drop everything that is not a letter, digit,
# space, hyphen or underscore (Unicode-aware, so Cyrillic is kept), then each
# space becomes a hyphen. Headings inside fences are not headings.
# shellcheck disable=SC2016  # a node program, not shell: nothing should expand
dangling="$(node -e '
  const fs = require("fs");
  const text = fs.readFileSync(process.argv[1], "utf8");
  const ids = new Set();
  let inFence = false;
  for (const line of text.split("\n")) {
    if (/^```/.test(line)) { inFence = !inFence; continue; }
    if (inFence) continue;
    const h = /^#{1,6}\s+(.*?)\s*#*\s*$/.exec(line);
    if (h) {
      const plain = h[1].replace(/\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/[`*]/g, "");
      ids.add(plain.toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, "").replace(/ /g, "-"));
    }
    for (const m of line.matchAll(/<a id="([^"]+)"/g)) ids.add(m[1]);
  }
  const missing = [];
  for (const m of text.matchAll(/\]\(#([^)]+)\)/g)) {
    if (!ids.has(decodeURIComponent(m[1]))) missing.push("#" + m[1]);
  }
  process.stdout.write(missing.join(" "));
' "$UK")" || dangling="node exited $?"
eq "README.uk.md: every in-page anchor resolves" "" "$dangling"

printf 'readme-uk-parity: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
