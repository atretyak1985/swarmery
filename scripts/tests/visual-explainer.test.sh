#!/usr/bin/env bash
# Contract test for plugins/architecture-pack/skills/visual-explainer/ and /visualize.
#
# Never runs a model and makes no network call. The behaviour lives in the skill's own
# scripts/test.sh (checker, template resolution, the loopback one-page server, to-artifact,
# and probe.js run inside a real page when headless Chrome is present); this wrapper runs it
# in CI and adds the few structural contracts a script cannot check from inside the skill:
# every file SKILL.md names ships, the command stays a thin proxy with a distinct name, and
# nothing machine-specific or repo-specific is committed.
#
# Style matches scripts/tests/design-pack-contract.test.sh (set -euo pipefail,
# pass/fail counters, one line per check). Dependencies: bash, grep, node, curl.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SKILL="$ROOT/plugins/architecture-pack/skills/visual-explainer"
CMD="$ROOT/plugins/architecture-pack/commands/visualize.md"

pass=0
fail=0

ok()  { pass=$((pass + 1)); printf '  ok   - %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL - %s\n' "$1"; }

if ! command -v node >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
  echo "  skip - node and curl are required by the visual-explainer scripts"
  exit 0
fi

# ── 1. the skill's behavioural self-test ────────────────────────────────────
if out="$(bash "$SKILL/scripts/test.sh" 2>&1)"; then
  ok "skills/visual-explainer/scripts/test.sh passes ($(tail -1 <<<"$out"))"
else
  bad "skills/visual-explainer/scripts/test.sh failed:"
  printf '%s\n' "$out" | sed 's/^/        /'
fi

# ── 2. every file the SKILL.md points at ships with it ──────────────────────
for rel in resources/content-mapping.md resources/components.md resources/verification.md \
           templates/explainer-shell.html; do
  if [ -f "$SKILL/$rel" ] && grep -q "$rel" "$SKILL/SKILL.md"; then ok "SKILL.md names $rel and it exists"; else bad "SKILL.md / $rel out of sync"; fi
done
for script in new-page.mjs check.mjs serve.mjs probe.js to-artifact.mjs test.sh; do
  if [ -f "$SKILL/scripts/$script" ] && grep -q "$script" "$SKILL/SKILL.md"; then ok "SKILL.md names scripts/$script and it exists"; else bad "scripts/$script out of sync"; fi
done

# ── 3. the command is a thin proxy with its own name ─────────────────────────
if grep -q 'Thin entry point' "$CMD" && grep -q 'visual-explainer' "$CMD"; then ok "/visualize is a thin proxy to visual-explainer"; else bad "/visualize lost its thin-proxy description"; fi
if [ "$(basename "$CMD" .md)" != "$(basename "$SKILL")" ]; then ok "command and skill names differ (no registry collision)"; else bad "command and skill share a name"; fi

# ── 4. nothing machine- or repo-specific is committed ───────────────────────
if grep -rnE '/(Users|home)/[a-z]' "$SKILL" "$CMD" >/dev/null 2>&1; then bad "a machine-absolute path is committed"; else ok "no machine-absolute paths"; fi
if grep -rn 'swarm/' "$SKILL" "$CMD" >/dev/null 2>&1; then bad "this repo's own vocabulary leaked into the skill"; else ok "no repo-specific vocabulary"; fi

printf '\nvisual-explainer: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
