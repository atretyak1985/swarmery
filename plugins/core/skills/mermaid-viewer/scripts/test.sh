#!/usr/bin/env bash
# test.sh — smoke test for the build pipeline.
#
# Runs stats.sh + build.sh against examples/sample.mmd and diffs the generated
# HTML against examples/sample.html.  Exits 0 on match, 1 on drift.
#
# To refresh the golden sample after intentional template changes:
#   scripts/test.sh --update
#
# The fixture exercises the ER-diagram code path specifically (entity
# indexing, FK counts, search filter).  Non-ER templates don't need their
# own fixture — they share the same build pipeline.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
EXAMPLES="$SCRIPT_DIR/../examples"
SAMPLE_MMD="$EXAMPLES/sample.mmd"
GOLDEN="$EXAMPLES/sample.html"

update=0
if [ "${1:-}" = "--update" ]; then update=1; fi

[ -r "$SAMPLE_MMD" ] || { echo "error: missing fixture $SAMPLE_MMD" >&2; exit 1; }

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
generated="$tmpdir/sample.html"

echo "→ running stats.sh"
stats="$("$SCRIPT_DIR/stats.sh" "$SAMPLE_MMD")"
echo "  stats: $stats"

echo "→ running build.sh"
"$SCRIPT_DIR/build.sh" \
  --mmd "$SAMPLE_MMD" \
  --out "$generated" \
  --title "Sample Schema" \
  --subtitle "mermaid-viewer pipeline fixture" \
  --stats-json "$stats"

if [ "$update" -eq 1 ]; then
  cp "$generated" "$GOLDEN"
  echo "→ updated $GOLDEN"
  exit 0
fi

if [ ! -r "$GOLDEN" ]; then
  echo "warning: $GOLDEN missing — first run; storing golden"
  cp "$generated" "$GOLDEN"
  echo "PASS (initial)"
  exit 0
fi

if diff -u "$GOLDEN" "$generated" >"$tmpdir/diff.txt" 2>&1; then
  echo "PASS"
  exit 0
else
  echo "FAIL — output drifted from golden"
  echo "----- diff (head) -----"
  head -40 "$tmpdir/diff.txt"
  echo "-----------------------"
  echo "Inspect: diff -u $GOLDEN $generated"
  echo "Refresh: $SCRIPT_DIR/test.sh --update"
  exit 1
fi
