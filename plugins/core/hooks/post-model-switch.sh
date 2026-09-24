#!/bin/bash
# PostModelSwitch hook: record that the session's model changed.
#
# Warn-only, always exit 0. This fires on deliberate switches AND on session
# resume — Claude Code restores a session's model on its own — so it must be
# cheap and must not narrate a no-op.
#
# Two jobs:
#   1. Append the switch to the shared session log the daemon already ingests,
#      so a model change is visible next to the work it affected.
#   2. Warn once when the new model is absent from the daemon's pricing table:
#      an unpriced model costs NULL silently, and that silence is exactly how a
#      new generation hides from the cost layer until someone audits it.
set -u

input=$(cat 2>/dev/null || true)
printf '%s' "$input" | jq -e . >/dev/null 2>&1 || exit 0

to_model=$(printf '%s' "$input" | jq -r '.to_model // empty' 2>/dev/null || true)
from_model=$(printf '%s' "$input" | jq -r '.from_model // empty' 2>/dev/null || true)
session_id=$(printf '%s' "$input" | jq -r '.session_id // empty' 2>/dev/null || true)
[ -n "$to_model" ] || exit 0

# Resume restoring the same model is not a switch worth a row.
[ "$to_model" = "$from_model" ] && exit 0

SESSION_FILE="/tmp/claude-session-$(date +%Y%m%d).jsonl"
jq -c -n \
  --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
  --arg cmd "model:${from_model:-?}->${to_model}" \
  --arg session_id "$session_id" \
  --arg model_observed "$to_model" \
  '{ts: $ts, tool: "ModelSwitch", file: "", cmd: $cmd, session_id: $session_id, model_observed: $model_observed}' \
  >> "$SESSION_FILE" 2>/dev/null || true

# A DOWNGRADE deserves one line on stderr, because it is the switch nobody asked
# for. Claude Code answers a safeguard refusal by moving the session onto an
# older model and carrying on: the work continues, quieter and weaker, and the
# operator's only clue today is that the statusline shows a different name than
# it did an hour ago. Say it once, name the likely cause, and name the way back.
MODEL_TIER_LIB="$(dirname "${BASH_SOURCE[0]}")/lib/model-tier.sh"
# shellcheck source=lib/model-tier.sh
[ -r "$MODEL_TIER_LIB" ] && . "$MODEL_TIER_LIB"
if command -v model_is_downgrade >/dev/null 2>&1 \
   && model_is_downgrade "$from_model" "$to_model"; then
  echo "⚠️  session moved from ${from_model:-?} to ${to_model} (likely a safety classifier); /model to switch back" >&2
fi

# Pricing check. Best-effort: no pricing file, no jq match, no complaint.
pricing="${CLAUDE_PLUGIN_ROOT:-}/../../tools/swarmery/config/pricing.json"
[ -f "$pricing" ] || pricing="${SWARMERY_PRICING_JSON:-}"
if [ -n "$pricing" ] && [ -f "$pricing" ]; then
  known=$(jq -r --arg m "$to_model" '
      (.models // {}) | has($m)
      or ((. // {}) | keys | map(select($m | startswith(.))) | length > 0)
    ' "$pricing" 2>/dev/null || echo "true")
  if [ "$known" = "false" ]; then
    echo "⚠️  ${to_model} is not in the swarmery pricing table — its turns will cost NULL until config/pricing.json learns it." >&2
  fi
fi

exit 0
