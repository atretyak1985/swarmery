#!/bin/bash
# shellcheck disable=SC2034  # colour palette kept complete across hooks
# Subagent Stop Hook for Claude Code
# Shows agent completion with duration
set -e

# ── Colors ────────────────────────────────────────────────────────
RST='\033[0m'; BOLD='\033[1m'; DIM='\033[2m'
# shellcheck disable=SC2034  # full palette kept for consistency across hooks
GREEN='\033[0;32m'; WHITE='\033[1;37m'; RED='\033[0;31m'

# ── Read hook JSON ────────────────────────────────────────────────
input=$(cat)

# Malformed/non-JSON stdin: nothing to track — never break the tool call
# (non-blocking contract; every jq below assumes valid JSON).
if ! printf '%s' "$input" | jq -e . >/dev/null 2>&1; then
  exit 0
fi
# Agent name lives in different fields depending on the SubagentStop payload
# shape (Agent-tool agents carry .tool_input.subagent_type; workflow agents
# carry a top-level .agent_type). Pick the FIRST field that is a non-empty
# string — plain `//` does NOT fall through on an empty string ("" is truthy
# in jq), which is how ~35% of events used to record an empty name. `?`
# guards odd shapes (e.g. .tool_input being a scalar) from erroring out.
agent_type=$(echo "$input" | jq -r '[ .agent_type?, .subagent_type?, (.tool_input?.subagent_type?), (.tool_response?.subagent_type?) ] | map(select(type == "string" and (. | length) > 0)) | .[0] // ""' 2>/dev/null || true)
parent_id=$(echo "$input" | jq -r '.parent_tool_use_id // .tool_use_id // empty' 2>/dev/null || true)
session_id=$(echo "$input" | jq -r '.session_id // empty' 2>/dev/null || true)

# Guarantee a non-empty name: fall back to a truncated invocation/session id,
# then to the literal "unknown". Keeps `done:<name>` never blank downstream.
if [ -z "$agent_type" ]; then
  fallback_id="$parent_id"
  if [ -z "$fallback_id" ]; then
    fallback_id="$session_id"
  fi
  if [ -n "$fallback_id" ]; then
    agent_type="agent-${fallback_id:0:8}"
  else
    agent_type="unknown"
  fi
fi

model_observed=$(echo "$input" | jq -r '.tool_response.model // .model // empty' 2>/dev/null)

# Session log, debug dump and start stamps all live in the private run dir.
# shellcheck source=lib/session-file.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/session-file.sh"
RUN_DIR=$(swarmery_run_dir) || RUN_DIR=""

if [ "${CLAUDE_HOOK_DEBUG:-0}" = "1" ] && [ -n "$RUN_DIR" ]; then
  (umask 077 && echo "$input" >> "$RUN_DIR/claude-hook-payload-debug.jsonl") 2>/dev/null || true
fi

# ── Try to calculate duration from tracking file ──────────────────
duration_str=""
# Find a start stamp for this agent type (subagent-start.sh names it with the
# same sanitized type). Its content must be an epoch — it feeds arithmetic.
AGENT_TRACKING=""
if [ -n "$RUN_DIR" ]; then
  safe_type=$(printf '%s' "$agent_type" | tr -c 'A-Za-z0-9_:-' '_')
  AGENT_TRACKING=$(find "$RUN_DIR" -maxdepth 1 -name "claude-agent-${safe_type}.*" -type f 2>/dev/null | head -1)
fi
start_epoch=""
[ -n "$AGENT_TRACKING" ] && [ -f "$AGENT_TRACKING" ] && start_epoch=$(cat "$AGENT_TRACKING" 2>/dev/null)
[[ "$start_epoch" =~ ^[0-9]+$ ]] || start_epoch=""
if [ -n "$start_epoch" ]; then
  end_epoch=$(date +%s)
  diff_s=$((end_epoch - 10#$start_epoch))

  if [ $diff_s -ge 60 ]; then
    mins=$((diff_s / 60))
    secs=$((diff_s % 60))
    duration_str="${mins}m ${secs}s"
  else
    duration_str="${diff_s}s"
  fi

  rm -f "$AGENT_TRACKING"
fi

# ── Log to session file ──────────────────────────────────────────
SESSION_FILE=$(swarmery_session_file) || SESSION_FILE=/dev/null
jq -c -n \
  --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
  --arg cmd "done:${agent_type}" \
  --arg parent_id "$parent_id" \
  --arg session_id "$session_id" \
  --arg model_observed "$model_observed" \
  --arg duration_s "${diff_s:-}" \
  '{ts: $ts, tool: "AgentDone", file: "", cmd: $cmd, parent_id: $parent_id, session_id: $session_id, model_observed: $model_observed, duration_s: $duration_s}' >> "$SESSION_FILE"

# ── Ledger row (mechanical cells only) ────────────────────────────
# The 7-cell delegation ledger internal/wsingest reads
# (logs/agents.md → task_delegations). Four of the seven cells are
# mechanically derivable from this payload, so the model should not be
# hand-writing them: agent and loops are counted here, verdict and artifact
# are read out of the subagent's own final message. quality(1-5) and mistakes
# stay empty — those are the orchestrator's judgment, and judgment is the only
# thing worth spending prompt budget on.
#
# Row shape must match parseLedger() in internal/wsingest/artifacts.go:
#   | agent | phase | verdict | loops | quality | mistakes | artifact |
# It tolerates a short row, but 7 cells is what carries loops/quality.
emit_ledger_row() {
  local ws_root working_dir task_dir task_id y m d slug log transcript
  if [ -n "${AGENT_PROJECT:-}" ]; then
    ws_root="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}"
    working_dir="${ws_root}/${AGENT_PROJECT}/workspace/working"
  else
    working_dir="${CLAUDE_PROJECT_DIR:-$(pwd)}/.claude-workspace/working"
  fi
  [ -d "$working_dir" ] || return 0

  # Which task dir? AGENT_TASK_ID when the caller exports it, but nothing in
  # the fleet does today — so the working path is the transcript fallback:
  # the most recent working/YYYY/MM/DD/{slug}/ path this session touched, the
  # same resolution session-summary.sh uses to attribute a session to a task.
  task_id="${AGENT_TASK_ID:-}"
  if [ -z "$task_id" ]; then
    transcript=$(printf '%s' "$input" | jq -r '.transcript_path // ""' 2>/dev/null || true)
    if [ -n "$transcript" ] && [ -f "$transcript" ]; then
      task_id=$(grep -oE '/working/[0-9]{4}/[0-9]{2}/[0-9]{2}/[a-z0-9][a-z0-9-]*/' "$transcript" 2>/dev/null \
        | sed -E 's|.*/working/([0-9]{4})/([0-9]{2})/([0-9]{2})/([a-z0-9-]+)/$|\1-\2-\3-\4|' \
        | tail -1 || true)
    fi
  fi
  [ -n "$task_id" ] || return 0

  y="${task_id:0:4}"; m="${task_id:5:2}"
  d="${task_id:8:2}"; slug="${task_id:11}"
  [ -n "$slug" ] || return 0
  task_dir="${working_dir}/${y}/${m}/${d}/${slug}"
  if [ ! -d "$task_dir" ]; then
    task_dir=$(find "$working_dir" -type d -path "*/${slug}" 2>/dev/null | head -1)
  fi
  [ -n "$task_dir" ] && [ -d "$task_dir" ] || return 0

  local final verdict artifact phase loops
  final=$(printf '%s' "$input" | jq -r '.last_assistant_message // ""' 2>/dev/null || true)

  # Verdict: the platform grammar internal/verify parses. Absent for executors
  # that emit none — an empty cell is honest, a guessed PASS is not.
  verdict=$(printf '%s' "$final" \
    | grep -oE 'VERDICT:[[:space:]]*(PASS|FAIL|INCONCLUSIVE)' \
    | head -1 | sed -E 's/.*(PASS|FAIL|INCONCLUSIVE).*/\1/' || true)

  # Artifact: a workspace-relative path the agent says it wrote. Report and
  # phase docs only — a bare source path is a file it edited, not its artifact.
  artifact=$(printf '%s' "$final" \
    | grep -oE '(reports|phases|logs)/[A-Za-z0-9._/-]+\.(md|txt|json|html)' \
    | head -1 || true)

  phase="${AGENT_PHASE:-}"
  if [ -z "$phase" ] && [ -n "$artifact" ]; then
    phase=$(printf '%s' "$artifact" | grep -oE 'phase-[0-9]+' | head -1 || true)
  fi

  log="${task_dir}/logs/agents.md"
  mkdir -p "${task_dir}/logs" 2>/dev/null || return 0
  if [ ! -f "$log" ]; then
    printf '| agent | phase | verdict | loops | quality | mistakes | artifact |\n' > "$log"
    printf '|---|---|---|---|---|---|---|\n' >> "$log"
  fi

  # loops = how many times THIS agent already ran for THIS phase, +1. Counting
  # beats asking the model to remember across a compaction boundary.
  loops=$(grep -c "^| *@\?${agent_type} *| *${phase} *|" "$log" 2>/dev/null || true)
  [ -n "$loops" ] || loops=0
  loops=$((loops + 1))

  printf '| @%s | %s | %s | %s |  | — | %s |\n' \
    "$agent_type" "${phase:-—}" "${verdict:-—}" "$loops" "${artifact:-—}" >> "$log"
}
emit_ledger_row || true

# ── Print ─────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}┌─ ✅ AGENT DONE ────────────────────────────────────────${RST}"
echo -e "${GREEN}${BOLD}│${RST} ${WHITE}${BOLD}@${agent_type}${RST} completed"
if [ -n "$duration_str" ]; then
  echo -e "${GREEN}${BOLD}│${RST} ${DIM}Duration: ${duration_str}${RST}"
fi
echo -e "${GREEN}${BOLD}└───────────────────────────────────────────────────────${RST}"

exit 0
