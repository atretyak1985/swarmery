#!/bin/bash
# Behavioral tests for the site's locale layer (scripts/site/build.py + locales/).
#
# Framework-free and offline: every case builds the site into a throwaway dir
# under mktemp with the real generator (no --strict, so an untranslated language
# still builds) and inspects the output. Run locally with
# `bash scripts/tests/site-locale.test.sh`; CI picks it up through the
# scripts/tests/*.test.sh glob in .github/workflows/ci.yml.
#
# Cases:
#   en-additions-only       every English page equals a committed one once the
#                           i18n additions (hreflang links, og:locale, the language
#                           switch, data-i18n-* attributes) are taken out of both.
#                           Two modes:
#                             default             compare with HEAD:site/<page> —
#                                                 the committed site/ is what the
#                                                 generator builds (determinism); it
#                                                 means the same on a dev clone and
#                                                 on a depth-1 CI checkout
#                             SITE_BASE_REF=<ref> compare with <ref>:site/<page>
#                                                 instead — e.g. the commit before
#                                                 the locale layer, to prove the
#                                                 English output only gained the
#                                                 additions. The ref must resolve
#                                                 (fetch it first on a shallow
#                                                 clone), otherwise the case fails
#   uk-twin-exists          every English index.html has a uk/ twin with lang="uk"
#   hreflang-targets-exist  every <link rel="alternate" hreflang> points at a built page
#   strict-lists-missing    --strict on an empty uk overlay exits 1 and lists uk:<key>
#   lang-switch-roundtrip   the switch leads en -> uk twin and uk -> en twin
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUILD="${ROOT}/scripts/site/build.py"
BASE_URL="https://atretyak1985.github.io/swarmery/"

pass=0
fail=0
skipped=0

ok()   { pass=$((pass + 1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf '  FAIL  %s\n        %s\n' "$1" "$2"; }
skip() { skipped=$((skipped + 1)); printf '  skip  %s (%s)\n' "$1" "$2"; }

# eq <desc> <expected> <actual>
eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected [$2], got [$3]"; fi; }
# has <desc> <haystack> <needle>
has() { case "$2" in *"$3"*) ok "$1" ;; *) bad "$1" "missing [$3]" ;; esac; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# norm <file> — drop what the locale layer adds to an English page
norm() {
  sed -E -e 's/ data-i18n-[a-z]+="[^"]*"//g' \
         -e '/rel="alternate"|og:locale|class="lang"/d' -- "$1"
}

# resolve <page path relative to the site root> <relative href> — the target page
resolve() {
  python3 -I -c '
import os, sys
page, href = sys.argv[1], sys.argv[2]
t = os.path.normpath(os.path.join(os.path.dirname(page), href))
print("index.html" if t == "." else (t + "/index.html" if href.endswith("/") else t))
' "$1" "$2"
}

# switch_href <file> — the href of the language switch in a built page
switch_href() { sed -nE -e 's/.*<a class="lang" href="([^"]*)".*/\1/p' -- "$1" | head -n 1; }

# ── build ───────────────────────────────────────────────────────────────────
OUT="$TMP/out"
mkdir -p "$OUT"
# seed the counters apply-counts.sh last wrote, exactly as a rebuild of site/ keeps them
cp -- "${ROOT}/site/index.html" "$OUT/index.html"
if ! build_log="$(python3 "$BUILD" "$OUT" 2>&1)"; then
  bad "build" "build.py exited non-zero: $build_log"
  printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skipped"
  exit 1
fi

# English pages: every committed .html under site/ outside uk/
EN_PAGES=()
while IFS= read -r p; do EN_PAGES+=("$p"); done < <(
  cd "${ROOT}/site" && find . -name '*.html' -not -path './uk/*' | sed 's|^\./||' | sort
)

# ── en-additions-only ───────────────────────────────────────────────────────
echo "en-additions-only"
REF="${SITE_BASE_REF:-HEAD}"
if ! git -C "$ROOT" rev-parse --verify -q "${REF}^{commit}" >/dev/null; then
  bad "en-additions-only" "git ref [${REF}] does not resolve$([ -n "${SITE_BASE_REF:-}" ] && printf ' (SITE_BASE_REF)')"
else
  n=0
  for p in "${EN_PAGES[@]}"; do
    if ! git -C "$ROOT" cat-file -e "${REF}:site/${p}" 2>/dev/null; then
      bad "en-additions-only: $p" "not in ${REF}:site/"
      continue
    fi
    git -C "$ROOT" show "${REF}:site/${p}" > "$TMP/ref.html"
    if d="$(diff <(norm "$TMP/ref.html") <(norm "$OUT/$p"))"; then
      ok "en-additions-only: $p"
      n=$((n + 1))
    else
      bad "en-additions-only: $p" "$(printf '%s' "$d" | head -n 6)"
    fi
  done
  eq "en-additions-only: all ${#EN_PAGES[@]} en pages match ${REF}" "${#EN_PAGES[@]}" "$n"
fi

# ── uk-twin-exists ──────────────────────────────────────────────────────────
echo "uk-twin-exists"
n=0
total=0
for p in "${EN_PAGES[@]}"; do
  case "$p" in */index.html|index.html) ;; *) continue ;; esac
  total=$((total + 1))
  if [ -f "$OUT/uk/$p" ] && grep -q '<html lang="uk"' -- "$OUT/uk/$p"; then
    n=$((n + 1))
  else
    bad "uk-twin-exists: uk/$p" "missing, or not lang=\"uk\""
  fi
done
eq "uk-twin-exists: every en index.html has a lang=uk twin ($total pages)" "$total" "$n"

# ── hreflang-targets-exist ──────────────────────────────────────────────────
echo "hreflang-targets-exist"
n=0
missing=""
while IFS= read -r href; do
  n=$((n + 1))
  path="${href#"$BASE_URL"}"
  case "$path" in "" | */) path="${path}index.html" ;; esac
  [ -f "$OUT/$path" ] || missing="${missing} ${href}"
done < <(grep -rhoE '<link rel="alternate" hreflang="[^"]+" href="[^"]+"' -- "$OUT" | sed -E 's/.* href="([^"]+)"/\1/')
eq "hreflang-targets-exist: all $n alternate links resolve" "" "${missing# }"
# 12 en + 12 uk pages, three alternates each (404.html carries none)
eq "hreflang-targets-exist: alternate link count" "72" "$n"
eq "hreflang-targets-exist: one x-default on the en home page" "1" "$(grep -c 'hreflang="x-default"' -- "$OUT/index.html")"

# ── strict-lists-missing ────────────────────────────────────────────────────
echo "strict-lists-missing"
LOC="$TMP/locales"
cp -R -- "${ROOT}/scripts/site/locales" "$LOC"
# an empty Ukrainian overlay, whatever the real one carries
printf '\nUI = {}\nCONTENT = {}\n' >> "$LOC/uk.py"
strict_log="$(SITE_LOCALES_DIR="$LOC" python3 "$BUILD" --strict "$TMP/strict" 2>&1)"
rc=$?
eq "strict-lists-missing: --strict exits 1 on an empty uk overlay" "1" "$rc"
listed="$(printf '%s\n' "$strict_log" | grep -c '^uk:')"
if [ "$listed" -ge 1 ]; then ok "strict-lists-missing: lists $listed uk:<key> lines"; else bad "strict-lists-missing: lists uk:<key> lines" "none in: $(printf '%s' "$strict_log" | tail -n 3)"; fi
has "strict-lists-missing: names a UI key" "$strict_log" "uk:nav.features"
has "strict-lists-missing: names a content key" "$strict_log" "uk:content.features.planning.title"

# ── lang-switch-roundtrip ───────────────────────────────────────────────────
echo "lang-switch-roundtrip"
n=0
total=0
for p in "${EN_PAGES[@]}"; do
  case "$p" in */index.html|index.html) ;; *) continue ;; esac
  total=$((total + 1))
  there="$(resolve "$p" "$(switch_href "$OUT/$p")")"
  back="$(resolve "uk/$p" "$(switch_href "$OUT/uk/$p")")"
  if [ "$there" = "uk/$p" ] && [ "$back" = "$p" ]; then
    n=$((n + 1))
  else
    bad "lang-switch-roundtrip: $p" "en -> [$there], uk -> [$back]"
  fi
done
eq "lang-switch-roundtrip: en <-> uk on all $total pages" "$total" "$n"
eq "lang-switch-roundtrip: 404 leads to the uk home" "/swarmery/uk/" "$(switch_href "$OUT/404.html")"

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skipped"
[ "$fail" -eq 0 ]
