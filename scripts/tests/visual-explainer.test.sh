#!/usr/bin/env bash
# Contract test for plugins/core/skills/visual-explainer/ and the /visualize command.
#
# This test NEVER runs a model, never opens a browser and makes no network call.
# It runs the skill's own self-test (shell passes the static checker, the checker
# catches seeded defects, to-artifact strips the document wrapper) and asserts the
# shipped docs still say the things that keep the skill honest: every resource the
# SKILL.md names exists, the description routes away from html-reporting and
# mermaid-viewer, the verification protocol keeps its four-run matrix and its
# warning about headless CLI screenshots, and the command stays a thin proxy.
#
# Style matches scripts/tests/design-pack-contract.test.sh (set -euo pipefail,
# pass/fail counters, one line per check). Dependencies: bash, grep, node.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SKILL="$ROOT/plugins/core/skills/visual-explainer"
CMD="$ROOT/plugins/core/commands/visualize.md"

pass=0
fail=0

ok()  { pass=$((pass + 1)); printf '  ok   - %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL - %s\n' "$1"; }

if ! command -v node >/dev/null 2>&1; then
  echo "  skip - node not on PATH; visual-explainer scripts need node"
  exit 0
fi

# ── 1. the skill's own self-test ────────────────────────────────────────────
if out="$(bash "$SKILL/scripts/test.sh" 2>&1)"; then
  ok "skills/visual-explainer/scripts/test.sh passes ($(tail -1 <<<"$out"))"
else
  bad "skills/visual-explainer/scripts/test.sh failed:"
  printf '%s\n' "$out" | sed 's/^/        /'
fi

# ── 2. every file the SKILL.md points at ships with it ──────────────────────
for rel in resources/content-mapping.md resources/components.md resources/verification.md \
           templates/shell.html scripts/check.mjs scripts/probe.js scripts/to-artifact.mjs scripts/test.sh; do
  if [ -f "$SKILL/$rel" ] && grep -q "$rel" "$SKILL/SKILL.md"; then
    ok "SKILL.md names $rel and it exists"
  else
    bad "SKILL.md / $rel out of sync"
  fi
done

# ── 3. routing: the description says when NOT to fire ───────────────────────
desc="$(grep -m1 '^description:' "$SKILL/SKILL.md")"
for sibling in html-reporting mermaid-viewer; do
  if grep -q "$sibling" <<<"$desc"; then ok "description routes away from $sibling"; else bad "description lost the $sibling exclusion"; fi
done

# ── 4. the verification protocol keeps its teeth ────────────────────────────
VER="$SKILL/resources/verification.md"
if grep -q '1440 × 900' "$VER" && grep -q '400 × 860' "$VER" && grep -qi 'dark' "$VER"; then
  ok "verification keeps the desktop/phone × light/dark matrix"
else
  bad "verification matrix missing from resources/verification.md"
fi
if grep -q 'headless' "$VER"; then ok "verification warns about headless CLI screenshots"; else bad "headless CLI warning missing"; fi
if grep -q 'probe.js' "$SKILL/SKILL.md" && grep -q 'check.mjs' "$SKILL/SKILL.md"; then
  ok "SKILL.md success criterion names check.mjs and probe.js"
else
  bad "SKILL.md success criterion lost a measurement"
fi

# ── 5. the command is a thin proxy ──────────────────────────────────────────
if grep -q 'Thin entry point' "$CMD" && grep -q 'visual-explainer' "$CMD"; then
  ok "/visualize is a thin proxy to visual-explainer"
else
  bad "/visualize lost its thin-proxy description"
fi
if [ "$(basename "$CMD" .md)" != "$(basename "$SKILL")" ]; then
  ok "command and skill names differ (no registry collision)"
else
  bad "command and skill share a name"
fi

# ── 6. no machine-absolute paths in shipped files ───────────────────────────
if grep -rnE '/(Users|home)/[a-z]' "$SKILL" "$CMD" >/dev/null 2>&1; then
  bad "a machine-absolute path is committed in the skill or command"
else
  ok "no machine-absolute paths"
fi

printf '\nvisual-explainer: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
