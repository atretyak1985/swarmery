#!/bin/bash
# PreModelSwitch hook: refuse to move onto a model nobody has validated.
#
# This is the ONE hook in core allowed to block (exit 2), and it blocks on a
# definite negative only. Everything ambiguous — daemon down, no network,
# malformed payload, timeout — allows the switch with a warning. A laptop with
# the daemon stopped must not become unusable, and a gate that fires on its own
# infrastructure failing is a gate people delete.
#
#   downgrade               → allow + note (see "WHO INITIATED IT" below)
#   verdict pass            → allow
#   verdict fail            → BLOCK (exit 2)
#   no verdict recorded     → BLOCK (exit 2) — unknown is not the same as fine;
#                             catching exactly this is the point
#   verdict inconclusive    → BLOCK (exit 2) — too little evidence to move on
#   daemon unreachable      → allow + warn
#   malformed / no to_model → allow (nothing to judge)
#   SWARMERY_ALLOW_UNVALIDATED_MODEL=1 → allow + log
#
# The override is not a loophole, it is the validation path: trajectories on a
# new model cannot exist until somebody runs on it, so the operator switches
# deliberately, works, and the next `swarmery modeleval` scores those runs and
# opens the gate on its own.
#
# WHO INITIATED IT, AND WHY THIS HOOK DOES NOT ASK.
#
# The gate is meant for a HUMAN moving onto an unvalidated model. It must never
# fire on a switch the harness made for itself: Opus 5.5 answers a safeguard
# refusal by continuing the session on an older model, and blocking that leaves
# the session with nowhere to go — a safeguard fallback is the harness working
# as designed, not a decision to gate.
#
# The obvious fix would be a `reason` / `trigger` field on the payload. THIS
# HOOK READS NO SUCH FIELD, because none is known to exist. Everything this
# repository can demonstrate about the PreModelSwitch payload is three keys —
# `to_model`, `from_model`, `session_id` — and even those are attested only by
# the hooks that read them and the fixtures in
# scripts/tests/model-switch-hooks.test.sh, not by any captured live payload:
# grep the tree for PreModelSwitch and you get this file, post-model-switch.sh,
# hooks.json, plugins/core/README.md and two changelog lines. Inventing a field
# name here would produce a gate that silently never fires on the case it was
# added for, which is strictly worse than not having it.
#
# So the DIRECTION of the switch is used instead, which is observable from the
# two fields that do exist: a move to a weaker family or an older generation is
# allowed unconditionally. That fails OPEN for every safeguard fallback (they
# are downgrades by definition) and still gates the case the hook was written
# for — an operator moving UP onto something nothing has validated.
#
# To settle the question empirically, set SWARMERY_MODEL_SWITCH_PAYLOAD_LOG to a
# file path and switch models: every payload is appended verbatim, and if the
# harness turns out to name its trigger, this hook can then read a field that is
# known to be there.
set -u

input=$(cat 2>/dev/null || true)

if [ -n "${SWARMERY_MODEL_SWITCH_PAYLOAD_LOG:-}" ]; then
  printf '%s\n' "$input" >> "$SWARMERY_MODEL_SWITCH_PAYLOAD_LOG" 2>/dev/null || true
fi

# Nothing parseable → nothing to judge.
printf '%s' "$input" | jq -e . >/dev/null 2>&1 || exit 0

to_model=$(printf '%s' "$input" | jq -r '.to_model // empty' 2>/dev/null || true)
from_model=$(printf '%s' "$input" | jq -r '.from_model // empty' 2>/dev/null || true)
[ -n "$to_model" ] || exit 0

# A no-op switch (resume restoring the same model) is not a switch.
[ "$to_model" = "$from_model" ] && exit 0

MODEL_TIER_LIB="$(dirname "${BASH_SOURCE[0]}")/lib/model-tier.sh"
# shellcheck source=lib/model-tier.sh
[ -r "$MODEL_TIER_LIB" ] && . "$MODEL_TIER_LIB"
# Missing library ⇒ no downgrade is ever detected ⇒ the gate behaves exactly as
# it did before this section existed. The fallback is the OLD behaviour, never a
# blanket allow: a broken source file must not quietly disable the gate.
command -v model_is_downgrade >/dev/null 2>&1 || model_is_downgrade() { return 1; }

if model_is_downgrade "$from_model" "$to_model"; then
  echo "⚠️  model-switch gate: ${from_model:-?} → ${to_model} moves DOWN a tier or a generation — allowing it unvalidated (a safeguard fallback is the harness working as designed, and blocking it would strand the session)" >&2
  exit 0
fi

if [ "${SWARMERY_ALLOW_UNVALIDATED_MODEL:-0}" = "1" ]; then
  echo "⚠️  model-switch gate overridden for ${to_model} (SWARMERY_ALLOW_UNVALIDATED_MODEL=1)" >&2
  exit 0
fi

port="${SWARMERY_PORT:-7777}"
resp=$(curl -fsS --max-time 2 "http://127.0.0.1:${port}/api/models/${to_model}/validation" 2>/dev/null)
rc=$?

# 22 = HTTP error (404 included); anything else non-zero is an infrastructure
# problem, and infrastructure problems must not block a human's model switch.
if [ $rc -ne 0 ] && [ $rc -ne 22 ]; then
  echo "⚠️  model-switch gate: swarmery daemon unreachable on :${port} — allowing ${to_model} unchecked" >&2
  exit 0
fi

verdict=""
detail=""
if [ $rc -eq 0 ]; then
  verdict=$(printf '%s' "$resp" | jq -r '.verdict // empty' 2>/dev/null || true)
  detail=$(printf '%s' "$resp" | jq -r '.detail // empty' 2>/dev/null || true)
fi

if [ "$verdict" = "pass" ]; then
  exit 0
fi

case "$verdict" in
  fail)         reason="failed the golden set: ${detail}" ;;
  inconclusive) reason="not enough evidence yet: ${detail}" ;;
  *)            reason="has no recorded validation" ;;
esac

cat >&2 <<MSG
🚫 model switch blocked: ${to_model} ${reason}

   Validate it:   swarmery modeleval --model ${to_model}
   Override once: SWARMERY_ALLOW_UNVALIDATED_MODEL=1 claude …

   The override is the intended path for a brand-new model: run on it
   deliberately, then re-run modeleval — those runs are the evidence.
MSG
exit 2
