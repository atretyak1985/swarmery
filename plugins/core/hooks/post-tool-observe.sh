#!/bin/bash
# post-tool-observe.sh — the single unmatched PostToolUse hook.
#
# Replaces the former activity-tracker.sh + session-budget.sh pair: with no
# matcher this slot runs on EVERY tool call (500-2000 per session), so it must
# be one process, not two, and do the minimum. Two jobs:
#
#   1. Append one compact JSONL line per tool call to the shared session file
#      (/tmp/claude-session-YYYYMMDD.jsonl) — the substrate the statusline,
#      /dashboard, session-summary.sh, notify-completion.sh and the subagent
#      hooks all read. Agent dispatches also record requested-vs-observed
#      model, and a DOWNGRADE logs a ModelFallback event for the routing
#      report. The former per-call colored activity box is gone deliberately:
#      it cost a dozen jq/grep processes per tool call and nobody parsed it.
#
#   1b. The MAIN session's model, watched for the same downgrade. A safeguard
#      refusal moves the session onto an older model and keeps working there;
#      until now only Agent dispatches were compared, so the one change that
#      actually affects a whole session's output was the one nothing saw.
#
#   2. Session budget: once per session, when context occupancy crosses
#      SWARMERY_SESSION_BUDGET_TOKENS (default 240000 — 80% of the advisor's
#      R9 fat-session line; scripts/tests/post-tool-observe.test.sh pins the
#      relation), emit a systemMessage asking for a handoff at the next
#      checkpoint. Cost in a long session is quadratic in its length; the
#      advice must land while the session can still act on it.
#
#      THE 240k DEFAULT, RE-CHECKED AGAINST OPUS 5.5 PRICING (2026-09-23).
#      Opus 5.5 reads cache at $0.20/M against Opus 5's $0.50/M, so the
#      marginal cache-read cost of one 240k-token turn fell from $0.120 to
#      $0.048 — 2.5x cheaper, and the honest reading is that the MONEY case for
#      this threshold is now 2.5x weaker. It stays at 240000 anyway, because
#      money was never the binding half: the quadratic is in TURNS, a 240k
#      context still costs ~$0.05 to re-read on every one of the next hundred
#      tool calls, and long-context quality decay is priced in no table. What
#      did change is the tone of the message below (it no longer demands an
#      immediate stop) — the threshold buys a checkpoint, not an alarm.
#
# Contract: always exits 0 and, when it prints anything, prints valid JSON —
# observation and advice never block a tool call.
set -u

SESSION_FILE="${SWARMERY_SESSION_FILE:-/tmp/claude-session-$(date +%Y%m%d).jsonl}"
BUDGET_TOKENS="${SWARMERY_SESSION_BUDGET_TOKENS:-240000}"

# Family/generation comparison, shared with the two model-switch hooks. Sourced
# defensively and stubbed out if it is missing: a hook that runs on every tool
# call must degrade to silence, never to an error.
MODEL_TIER_LIB="$(dirname "${BASH_SOURCE[0]}")/lib/model-tier.sh"
# shellcheck source=lib/model-tier.sh
[ -r "$MODEL_TIER_LIB" ] && . "$MODEL_TIER_LIB"
command -v model_is_downgrade >/dev/null 2>&1 || model_is_downgrade() { return 1; }

input=$(cat)

pass() { echo '{"continue": true}'; exit 0; }

command -v jq >/dev/null 2>&1 || pass
printf '%s' "$input" | jq -e . >/dev/null 2>&1 || pass

# ── 1. Activity log ─────────────────────────────────────────────────────────
# One jq invocation extracts everything the log line needs.
# \x1f (unit separator) as the field delimiter: unlike TAB it is not IFS
# whitespace, so empty fields survive the read instead of collapsing.
IFS=$'\x1f' read -r tool_name file_path command_str model_requested model_observed transcript session_id <<EOF
$(printf '%s' "$input" | jq -r '[
    (.tool_name // "unknown"),
    (.tool_input.file_path // .tool_input.path // ""),
    (.tool_input.command // ""),
    (if (.tool_name // "") == "Agent" then (.tool_input.model // "") else "" end),
    (if (.tool_name // "") == "Agent" then (.tool_response.model // .tool_response.usage.model // "") else "" end),
    (.transcript_path // ""),
    (.session_id // "")
  ] | map(tostring | gsub("\u001f"; " ") | gsub("\n"; " ")) | join("\u001f")' 2>/dev/null)
EOF
[ -n "${tool_name:-}" ] || pass

{
  jq -c -n \
    --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg tool "$tool_name" \
    --arg file "$file_path" \
    --arg cmd "$command_str" \
    --arg model_requested "$model_requested" \
    --arg model_observed "$model_observed" \
    '{ts: $ts, tool: $tool, file: $file, cmd: $cmd}
     + (if $model_requested != "" then {model_requested: $model_requested} else {} end)
     + (if $model_observed != "" then {model_observed: $model_observed} else {} end)'
  # A DOWNGRADE, not a difference. The requested side is an alias (`opus`) and
  # the observed side an id (`claude-opus-5-5`); comparing them as strings made
  # nearly every dispatch look like a fallback, which is how a real one stopped
  # being visible. model_is_downgrade compares family tier and generation and
  # stays silent on anything it cannot justify.
  if [ -n "$model_requested" ] && [ -n "$model_observed" ] \
     && model_is_downgrade "$model_requested" "$model_observed"; then
    jq -c -n \
      --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
      --arg cmd "fallback:${model_requested}->${model_observed}" \
      '{ts: $ts, tool: "ModelFallback", file: "", cmd: $cmd}'
  fi
} >> "$SESSION_FILE" 2>/dev/null || true

[ -n "$transcript" ] && [ -f "$transcript" ] || pass
# No session id → cannot tell a first crossing from the hundredth → stay silent
# rather than becoming the noise this check exists to avoid.
[ -n "$session_id" ] || pass
safe_session=$(printf '%s' "$session_id" | tr -c 'A-Za-z0-9_.-' '_')

# ── 1b. Main-session model drift ────────────────────────────────────────────
# Cheap by construction: ONE `tail` of the transcript, then bash's own regex.
# The greedy `.*` makes the match land on the LAST "model":"…" in the tail, i.e.
# the newest line that names a model. A name the tier table does not recognise
# is ignored rather than guessed at, which also filters the tool_input.model of
# an Agent dispatch that happens to sit in the tail.
#
# The remembered value is updated on EVERY change, so a downgrade logs once and
# a switch back logs nothing — this is a change detector, not a poll.
model_state_dir="${TMPDIR:-/tmp}/swarmery-session-model"
model_state="${model_state_dir}/${safe_session}"
tail_txt=$(tail -n 40 "$transcript" 2>/dev/null)
main_model=""
if [[ "$tail_txt" =~ .*\"model\":\"([^\"]+)\" ]]; then
  main_model="${BASH_REMATCH[1]}"
  model_rank "$main_model" 2>/dev/null || true
  [ "${MODEL_TIER:-0}" -gt 0 ] 2>/dev/null || main_model=""
fi
if [ -n "$main_model" ]; then
  prev_model=""
  [ -r "$model_state" ] && read -r prev_model < "$model_state"
  if [ "$main_model" != "${prev_model:-}" ]; then
    mkdir -p "$model_state_dir" 2>/dev/null || true
    printf '%s\n' "$main_model" > "$model_state" 2>/dev/null || true
    if [ -n "${prev_model:-}" ] && model_is_downgrade "$prev_model" "$main_model"; then
      jq -c -n \
        --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
        --arg cmd "fallback:session:${prev_model}->${main_model}" \
        --arg session_id "$session_id" \
        --arg model_observed "$main_model" \
        '{ts: $ts, tool: "ModelFallback", file: "", cmd: $cmd, session_id: $session_id, model_observed: $model_observed}' \
        >> "$SESSION_FILE" 2>/dev/null || true
    fi
  fi
fi

# ── 2. Session budget (once per session) ────────────────────────────────────
marker="${TMPDIR:-/tmp}/swarmery-session-budget/${safe_session}"
[ -f "$marker" ] && pass

# Context occupancy = the newest assistant turn's input + cache-read +
# cache-creation tokens — the same three fields the statusline and the
# advisor's R9 query sum, so all three surfaces mean one thing by "context".
used=$(jq -rs '
  [ .[] | select(.message.usage != null) ] | last
  | (.message.usage.input_tokens // 0)
    + (.message.usage.cache_read_input_tokens // 0)
    + (.message.usage.cache_creation_input_tokens // 0)
' "$transcript" 2>/dev/null)
[[ "$used" =~ ^[0-9]+$ ]] || pass
[ "$used" -ge "$BUDGET_TOKENS" ] || pass

mkdir -p "$(dirname "$marker")" 2>/dev/null || true
: > "$marker" 2>/dev/null || true

# The wording is deliberately calm and deliberately NOT an order.
#
# The previous text shouted two imperatives in caps — "CLOSE this session",
# "START a new session" — at a model that treats a named stop as a stop. Opus
# 5.5 obeys those: the note could land between a failing edit and its fix and
# end the turn there, which under `claude -p` ends the PROCESS. The threshold is
# a soft budget crossed mid-work by definition; the correct response to it is a
# handoff written at the next natural checkpoint, not an immediate stop. The
# actions named have not changed, only their urgency and who picks the moment.
printf '{"continue": true, "systemMessage": "Context is at ~%dk tokens (soft budget %dk). Every further turn re-sends this whole context, so cost from here grows with the square of the session rather than with the work. Nothing is wrong and nothing needs to stop right now — finish the step you are on. At the next natural checkpoint (a step done, a check green), write the handoff into the task doc or your Completion Report: what shipped, what is in flight, and the exact next step — the reply alone does not carry it. Then close this session with that report, and start a new session carrying a short state summary (the goal, the files in play, the next step) and nothing else. If what remains is recurring monitoring rather than work, a routine is the cheaper home for it: it reads state with a fresh small context every run."}\n' \
  "$((used / 1000))" "$((BUDGET_TOKENS / 1000))"
exit 0
