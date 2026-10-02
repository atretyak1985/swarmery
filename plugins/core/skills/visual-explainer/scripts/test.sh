#!/usr/bin/env bash
# test.sh — self-test for the visual-explainer skill: the shell passes the static check,
# the checker catches the defects it exists for, and to-artifact produces a body-level page.
# Usage: bash scripts/test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
pass=0; fail=0
ok()  { pass=$((pass + 1)); printf '  ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL %s\n' "$1"; }

command -v node >/dev/null || { echo "node is required"; exit 1; }

# 1. The shell itself is clean.
if node "$here/scripts/check.mjs" "$here/templates/shell.html" >/dev/null; then ok "shell passes check.mjs"; else bad "shell fails check.mjs"; fi

# 2. probe.js is a single function expression that parses.
if node -e "new Function('return (' + require('fs').readFileSync(process.argv[1], 'utf8') + ')')()" "$here/scripts/probe.js" 2>/dev/null; then
  ok "probe.js parses as a function expression"
else
  bad "probe.js does not parse"
fi

# 3. The checker catches seeded defects.
sed -e 's/<main id="main">/<main id="main"><svg id="top" viewBox="0 0 200 100"><text>x<\/text><\/svg>/' \
    -e 's/data-t="sample"/data-t="nope"/' \
    -e 's/data-d="sample"/data-d="ghost"/' \
    "$here/templates/shell.html" > "$tmp/broken.html"
out="$(node "$here/scripts/check.mjs" "$tmp/broken.html" || true)"
for code in dup-id glossary-key drawer-missing; do
  if grep -q "ERROR $code" <<<"$out"; then ok "checker flags $code"; else bad "checker misses $code"; fi
done
if node "$here/scripts/check.mjs" "$tmp/broken.html" >/dev/null 2>&1; then bad "checker exits 0 on a broken page"; else ok "checker exits non-zero on a broken page"; fi

# 4. to-artifact strips the document wrapper and keeps the title first.
node "$here/scripts/to-artifact.mjs" "$here/templates/shell.html" "$tmp/artifact.html" >/dev/null
if head -c 200 "$tmp/artifact.html" | grep -q '^<title>'; then ok "to-artifact puts <title> first"; else bad "to-artifact title placement"; fi
if grep -qiE '<!doctype|<html|<head>|<body' "$tmp/artifact.html"; then bad "to-artifact left a document wrapper"; else ok "to-artifact removes the wrapper"; fi
if grep -q 'id="drawer"' "$tmp/artifact.html" && grep -q 'id="glossary"' "$tmp/artifact.html"; then ok "to-artifact keeps body content"; else bad "to-artifact lost body content"; fi

printf '%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
