#!/usr/bin/env bash
# build.sh — render architecture-map.json into the self-contained HTML viewer.
#
# Usage: build.sh --json <path> --out <path>
#
# Substitutes the {%%%MAP_JSON%%%} sentinel in templates/map.html.tpl with the
# JSON payload (awk index/substr — literal, regex-safe; the mermaid-viewer
# idiom). "</" is escaped to "<\/" so the payload can never terminate the
# <script> element early ("\/" is a legal JSON escape for "/").

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
TEMPLATE="$SCRIPT_DIR/../templates/map.html.tpl"

json="" out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --json) json="$2"; shift 2 ;;
    --out)  out="$2";  shift 2 ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *) echo "error: unknown flag: $1" >&2; exit 2 ;;
  esac
done

[ -z "$json" ] && { echo "error: --json is required" >&2; exit 2; }
[ -z "$out"  ] && { echo "error: --out is required"  >&2; exit 2; }
[ -r "$json" ] || { echo "error: cannot read $json" >&2; exit 1; }
[ -r "$TEMPLATE" ] || { echo "error: template not found at $TEMPLATE" >&2; exit 1; }

node -e "JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'))" "$json" \
  || { echo "error: $json is not valid JSON" >&2; exit 1; }

escaped="$(mktemp)"
trap 'rm -f "$escaped"' EXIT
sed 's|</|<\\/|g' "$json" > "$escaped"

awk -v sentinel='{%%%MAP_JSON%%%}' '
  NR == FNR { payload = payload sep $0; sep = "\n"; next }
  {
    p = index($0, sentinel)
    if (p) print substr($0, 1, p - 1) payload substr($0, p + length(sentinel))
    else   print
  }
' "$escaped" "$TEMPLATE" > "$out"

echo "wrote $out"
