#!/bin/bash
# shellcheck disable=SC2034  # colour palette kept complete across hooks
# Subagent Start Hook for Claude Code
# Tracks agent spawns with colored output
set -e

# ── Colors ────────────────────────────────────────────────────────
RST='\033[0m'; BOLD='\033[1m'; DIM='\033[2m'
# shellcheck disable=SC2034  # full palette kept for consistency across hooks
RED='\033[0;31m'; WHITE='\033[1;37m'; MAGENTA='\033[0;35m'

# ── Read hook JSON ────────────────────────────────────────────────
input=$(cat)

# Malformed/non-JSON stdin: nothing to track — never break the tool call
# (non-blocking contract; every jq below assumes valid JSON).
if ! printf '%s' "$input" | jq -e . >/dev/null 2>&1; then
  exit 0
fi
agent_type=$(echo "$input" | jq -r '.tool_input.subagent_type // .tool_input.type // "general"' 2>/dev/null || echo general)
description=$(echo "$input" | jq -r '.tool_input.description // ""' 2>/dev/null || true)
# Correlation fields — availability depends on Claude Code version; set CLAUDE_HOOK_DEBUG=1
# and inspect /tmp/claude-hook-payload-debug.jsonl to confirm which keys the payload carries
parent_id=$(echo "$input" | jq -r '.parent_tool_use_id // .tool_use_id // empty' 2>/dev/null || true)
session_id=$(echo "$input" | jq -r '.session_id // empty' 2>/dev/null || true)
model_requested=$(echo "$input" | jq -r '.tool_input.model // empty' 2>/dev/null || true)

if [ "${CLAUDE_HOOK_DEBUG:-0}" = "1" ]; then
  echo "$input" >> /tmp/claude-hook-payload-debug.jsonl
fi

# ── Log to session file ──────────────────────────────────────────
SESSION_FILE="/tmp/claude-session-$(date +%Y%m%d).jsonl"
jq -c -n \
  --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
  --arg cmd "spawn:${agent_type}" \
  --arg parent_id "$parent_id" \
  --arg session_id "$session_id" \
  --arg model_requested "$model_requested" \
  '{ts: $ts, tool: "Agent", file: "", cmd: $cmd, parent_id: $parent_id, session_id: $session_id, model_requested: $model_requested}' >> "$SESSION_FILE"

# ── Log agent start time for duration tracking ────────────────────
AGENT_TRACKING="/tmp/claude-agent-${agent_type}-$(date +%s).tmp"
date +%s > "$AGENT_TRACKING"

# ── Print ─────────────────────────────────────────────────────────
echo ""
echo -e "${RED}${BOLD}┌─ 🤖 AGENT SPAWN ──────────────────────────────────────${RST}"
echo -e "${RED}${BOLD}│${RST} ${WHITE}${BOLD}@${agent_type}${RST}"
if [ -n "$description" ]; then
  # Truncate long descriptions
  if [ ${#description} -gt 60 ]; then
    description="${description:0:57}..."
  fi
  echo -e "${RED}${BOLD}│${RST} ${DIM}${description}${RST}"
fi

# Count total agents spawned today
agent_total=$({ grep -c '"spawn:' "$SESSION_FILE" 2>/dev/null || true; } | tr -d '[:space:]')
[ -z "$agent_total" ] && agent_total=0
echo -e "${RED}${BOLD}│${RST} ${DIM}Agents spawned today: ${agent_total}${RST}"
echo -e "${RED}${BOLD}└───────────────────────────────────────────────────────${RST}"

exit 0
