#!/usr/bin/env bash
# test.sh — build the fixture and verify the output embeds a parseable payload.
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

bash "$SCRIPT_DIR/build.sh" \
  --json "$SCRIPT_DIR/../examples/fixture.json" \
  --out  "$tmp/map.html"

grep -q 'id="map-data"' "$tmp/map.html" || { echo "FAIL: map-data script missing"; exit 1; }
grep -q '{%%%MAP_JSON%%%}' "$tmp/map.html" && { echo "FAIL: sentinel not substituted"; exit 1; }

node - "$tmp/map.html" <<'EOF'
const { readFileSync } = require('node:fs');
const html = readFileSync(process.argv[2], 'utf8');
const m = html.match(/<script id="map-data" type="application\/json">([\s\S]*?)<\/script>/);
if (!m) { console.error('FAIL: cannot extract payload'); process.exit(1); }
const map = JSON.parse(m[1].replace(/<\\\//g, '</'));
if (map.modules.length !== 5 || map.flows.length !== 3) {
  console.error('FAIL: payload mismatch'); process.exit(1);
}
console.log('payload round-trip OK');
EOF
echo "PASS"
