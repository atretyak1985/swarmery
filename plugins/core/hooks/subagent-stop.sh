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
agent_type=$(echo "$input" | jq -r '.agent_type // .tool_input.subagent_type // "general"' 2>/dev/null)
parent_id=$(echo "$input" | jq -r '.parent_tool_use_id // .tool_use_id // empty' 2>/dev/null)
session_id=$(echo "$input" | jq -r '.session_id // empty' 2>/dev/null)
model_observed=$(echo "$input" | jq -r '.tool_response.model // .model // empty' 2>/dev/null)

if [ "${CLAUDE_HOOK_DEBUG:-0}" = "1" ]; then
  echo "$input" >> /tmp/claude-hook-payload-debug.jsonl
fi

# ── Try to calculate duration from tracking file ──────────────────
duration_str=""
# Find most recent tracking file for this agent type
AGENT_TRACKING=$(find /tmp -maxdepth 1 -name "claude-agent-${agent_type}-*.tmp" -type f 2>/dev/null | head -1)
if [ -n "$AGENT_TRACKING" ] && [ -f "$AGENT_TRACKING" ]; then
  start_epoch=$(cat "$AGENT_TRACKING")
  end_epoch=$(date +%s)
  diff_s=$((end_epoch - start_epoch))

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
SESSION_FILE="/tmp/claude-session-$(date +%Y%m%d).jsonl"
jq -c -n \
  --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
  --arg cmd "done:${agent_type}" \
  --arg parent_id "$parent_id" \
  --arg session_id "$session_id" \
  --arg model_observed "$model_observed" \
  --arg duration_s "${diff_s:-}" \
  '{ts: $ts, tool: "AgentDone", file: "", cmd: $cmd, parent_id: $parent_id, session_id: $session_id, model_observed: $model_observed, duration_s: $duration_s}' >> "$SESSION_FILE"

# ── Print ─────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}┌─ ✅ AGENT DONE ────────────────────────────────────────${RST}"
echo -e "${GREEN}${BOLD}│${RST} ${WHITE}${BOLD}@${agent_type}${RST} completed"
if [ -n "$duration_str" ]; then
  echo -e "${GREEN}${BOLD}│${RST} ${DIM}Duration: ${duration_str}${RST}"
fi
echo -e "${GREEN}${BOLD}└───────────────────────────────────────────────────────${RST}"

exit 0
