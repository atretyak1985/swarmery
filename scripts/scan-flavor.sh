#!/bin/bash
# scan-flavor.sh — measure remaining project/domain flavor in plugins/** (P4 tracker).
# Target: zero. Run before every de-flavor commit. See docs/DE-FLAVOR.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Two rule sets (see docs/NEUTRALITY.md):
#   BRAND tokens (project/company identity + infra aliases) — forbidden EVERYWHERE in plugins/.
#   DOMAIN tokens — forbidden in core only; legitimate inside their own domain pack.
# Real patterns live in gitignored files (or env vars) so the public repo carries no
# internal names. Fallback below is a placeholder example — replace with your own.
if [ -n "${FLAVOR_BRAND:-}" ]; then BRAND="$FLAVOR_BRAND";
elif [ -f "${ROOT}/.flavor-tokens" ]; then BRAND="$(head -1 "${ROOT}/.flavor-tokens")";
else BRAND='mycompany|my-product|my-internal-env'; fi
if [ -n "${FLAVOR_DOMAIN:-}" ]; then DOMAIN="$FLAVOR_DOMAIN";
elif [ -f "${ROOT}/.flavor-tokens-domain" ]; then DOMAIN="$(head -1 "${ROOT}/.flavor-tokens-domain")";
else DOMAIN='my-domain-noun'; fi

echo "── Flavor scan: ${ROOT}/plugins ──"
files=$( { grep -rilE "$BRAND" "${ROOT}/plugins" 2>/dev/null; \
           grep -rilE "$DOMAIN" "${ROOT}/plugins/core" 2>/dev/null; } | sort -u || true)
# per-file count uses the pattern relevant to its location
PATTERN="$BRAND"   # default for packs; core files get BRAND|DOMAIN below
if [ -z "$files" ]; then
  echo "✓ clean — no project/domain tokens remain"
  exit 0
fi

# Per-file counts (sorted desc). Avoid a subshell so the total survives.
tmp=$(mktemp)
while IFS= read -r f; do
  [ -z "$f" ] && continue
  case "$f" in
    "${ROOT}/plugins/core/"*) pat="${BRAND}|${DOMAIN}";;
    *)                        pat="$BRAND";;
  esac
  n=$(grep -roiE "$pat" "$f" 2>/dev/null | wc -l | tr -d ' ')
  [ "$n" -eq 0 ] && continue
  printf '%4d  %s\n' "$n" "${f#$ROOT/}"
done <<< "$files" > "$tmp"
sort -rn "$tmp"
total=$(awk '{s+=$1} END{print s+0}' "$tmp")
rm -f "$tmp"

echo "────────────────────────────────"
echo "files: $(echo "$files" | grep -c .)   occurrences: $total   (target: 0)"
