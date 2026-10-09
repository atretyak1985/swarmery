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
#   strict-build-clean      --strict on the real locales exits 0: nothing in uk
#                           falls back to English (reports the missing count)
#   uk-no-english-leak      no uk page carries a run of 4+ English words outside
#                           <pre>/<code>/<kbd>, tags, attributes and the ALLOWLIST
#                           below; skipped while strict-build-clean reports keys
#                           that are still untranslated (every page is English then)
#   uk-asset-paths          every src/data-src/data-play/data-poster/data-zoom/
#                           poster/href on a uk page that names assets/, video/ or
#                           favicon.svg resolves from that page to a file of the
#                           site (or, for video/, of docs/video/)
#
# SITE_LOCALES_DIR is passed through to every build except strict-lists-missing,
# which always starts from a copy of the real locales.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUILD="${ROOT}/scripts/site/build.py"
BASE_URL="https://atretyak1985.github.io/swarmery/"

# English that a Ukrainian page may legitimately carry: product and project names,
# licences, the dashboard page names (D6). Matched as whole words, case-sensitive.
# Also allowed, read from the sources rather than listed here: every data.py POSTS
# title and subtitle (the cards lead to English articles, D6) and every
# marketplace.json plugin description (manifests are not localised, D7).
ALLOWLIST=(
  "Claude Code" "Swarmery" "GitHub" "Substack" "Serena" "Graphify" "TrailMap"
  "Ledgerly API" "PolyForm Noncommercial" "Apache-2.0" "Anthropic"
  "Go" "React" "SQLite"
  "Today" "Inbox" "Sessions" "Needs you" "Plans" "Health" "Learning" "Knowledge" "System"
)

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
  # the 404 page's inline link to the Ukrainian home is the fifth allowed addition
  sed -E -e 's/ data-i18n-[a-z]+="[^"]*"//g' \
         -e 's#<p class="muted"><a href="/swarmery/uk/"[^<]*</a></p>##g' \
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
if [ -f "${ROOT}/site/uk/index.html" ]; then
  mkdir -p "$OUT/uk"
  cp -- "${ROOT}/site/uk/index.html" "$OUT/uk/index.html"
fi
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
# every en page with a uk twin, and the twin, carry three alternates each (en, uk,
# x-default); a page without a twin (404.html) carries none
twins=0
for p in "${EN_PAGES[@]}"; do [ -f "$OUT/uk/$p" ] && twins=$((twins + 1)); done
eq "hreflang-targets-exist: alternate link count ($twins en pages with a uk twin)" "$((twins * 2 * 3))" "$n"
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

# ── strict-build-clean ──────────────────────────────────────────────────────
echo "strict-build-clean"
clean_log="$(python3 "$BUILD" --strict "$TMP/strict-clean" 2>&1)"
rc=$?
# "build.py: --strict: <N> untranslated or unknown key(s) in uk"
missing_n="$(printf '%s\n' "$clean_log" | sed -nE 's/.*--strict: ([0-9]+) untranslated.*/\1/p' | head -n 1)"
if [ "$rc" -eq 0 ]; then
  missing_n=0
  ok "strict-build-clean: --strict exits 0, no uk string falls back to English"
else
  bad "strict-build-clean: --strict exits 0, no uk string falls back to English" \
    "exit $rc, ${missing_n:-?} untranslated or unknown key(s), first: $(printf '%s\n' "$clean_log" | grep -m 1 '^uk:')"
fi

# ── uk-no-english-leak ──────────────────────────────────────────────────────
echo "uk-no-english-leak"
# Prints "<page>: <run>" for every run of 4+ consecutive English words on a uk page.
# Removed first: <script>/<style>/<svg>/<pre>/<code>/<kbd> with their content,
# comments and every tag (so every attribute); inline tags join their text, other
# tags end a line. An ALLOWLIST phrase breaks a run, as does any non-word token.
LEAK_PY='
import html, json, os, re, sys
out, root, allow = sys.argv[1], sys.argv[2], sys.argv[3:]
sys.path.insert(0, os.path.join(root, "scripts", "site"))
import data
allow += [p[1] for p in data.POSTS] + [p[2] for p in data.POSTS if p[2]]
# series cards render the part after "Part N: " as the title (build.py page_blog)
allow += [t.split(": ", 1)[1] for t in (p[1] for p in data.POSTS) if ": " in t]
with open(os.path.join(root, ".claude-plugin", "marketplace.json"), encoding="utf-8") as fh:
    allow += [p["description"] for p in json.load(fh)["plugins"]]
allowed = [re.compile(r"(?<![A-Za-z])" + re.escape(a) + r"(?![A-Za-z])") for a in sorted(set(allow), key=len, reverse=True)]
# two letters or more: "j, k, e, x" (keyboard keys) is not an English run
word = re.compile(r"[A-Za-z]{2,}(?:[-\x27" + chr(0x2019) + r"][A-Za-z]+)*")
# straight and typographic quotes, brackets, punctuation and the ellipsis around a word
edge = "\"\x27()[]{}.,:;!?" + "".join(map(chr, (0xAB, 0xBB, 0x201C, 0x201D, 0x2018, 0x2019, 0x2026)))
for dp, _, files in sorted(os.walk(os.path.join(out, "uk"))):
    for f in sorted(files):
        if not f.endswith(".html"): continue
        path = os.path.join(dp, f)
        with open(path, encoding="utf-8") as fh: s = fh.read()
        s = re.sub(r"(?is)<(script|style|svg|pre|code|kbd)\b.*?</\1\s*>", "\n", s)
        s = re.sub(r"(?s)<!--.*?-->", "\n", s)
        s = re.sub(r"(?i)</?(?:a|em|b|strong|i|u|small|abbr|mark)\b[^>]*>", " ", s)
        s = html.unescape(re.sub(r"<[^>]*>", "\n", s))
        for a in allowed: s = a.sub("\n", s)
        for line in s.split("\n"):
            run = []
            for tok in line.split() + [""]:
                if tok and word.fullmatch(tok.strip(edge)):
                    run.append(tok)
                    continue
                if len(run) >= 4: print(os.path.relpath(path, out) + ": " + " ".join(run))
                run = []
'
if [ "${missing_n:-1}" -ne 0 ]; then
  skip "uk-no-english-leak" "strict-build-clean reports ${missing_n:-?} untranslated key(s); the English fallback would be the leak"
else
  leaks="$(python3 -I -c "$LEAK_PY" "$OUT" "$ROOT" "${ALLOWLIST[@]}" 2>&1)"
  eq "uk-no-english-leak: no run of 4+ English words on a uk page" "" "$leaks"
fi

# ── uk-asset-paths ──────────────────────────────────────────────────────────
echo "uk-asset-paths"
# Prints "<page>: <value> -> <resolved>" for every asset reference that resolves to
# nothing, and "checked <N>" last. Relative values only: the uk pages carry no
# root-absolute asset path, and an absolute URL is not a file of this build.
ASSETS_PY='
import os, re, sys
out, site, video = sys.argv[1:4]
attr = re.compile(r"\s(?:src|data-src|data-play|data-poster|data-zoom|poster|href)=\"([^\"]*)\"")
named = re.compile(r"(?:^|/)(?:assets/|video/|favicon\.svg$)")
n = 0
for dp, _, files in sorted(os.walk(os.path.join(out, "uk"))):
    for f in sorted(files):
        if not f.endswith(".html"): continue
        page = os.path.relpath(os.path.join(dp, f), out)
        with open(os.path.join(dp, f), encoding="utf-8") as fh: s = fh.read()
        for v in attr.findall(s):
            v = v.split("#")[0].split("?")[0]
            if "://" in v or v.startswith(("/", "data:", "mailto:")) or not named.search(v): continue
            n += 1
            t = os.path.normpath(os.path.join(os.path.dirname(page), v))
            if t.startswith("video/"): found = os.path.isfile(os.path.join(video, t[len("video/"):]))
            elif t.startswith(".."): found = False
            else: found = os.path.isfile(os.path.join(site, t)) or os.path.isfile(os.path.join(out, t))
            if not found: print(page + ": " + v + " -> " + t)
print("checked %d" % n)
'
asset_log="$(python3 -I -c "$ASSETS_PY" "$OUT" "${ROOT}/site" "${ROOT}/docs/video" 2>&1)"
checked="$(printf '%s\n' "$asset_log" | sed -nE 's/^checked ([0-9]+)$/\1/p')"
if [ "${checked:-0}" -gt 0 ]; then ok "uk-asset-paths: $checked asset references on uk pages"; else bad "uk-asset-paths: found asset references to check" "$asset_log"; fi
eq "uk-asset-paths: every asset reference resolves" "" "$(printf '%s\n' "$asset_log" | grep -v '^checked ')"

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skipped"
[ "$fail" -eq 0 ]
