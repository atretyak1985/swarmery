#!/bin/bash
# Enhanced Notification Hook for Claude Code
# Sends context-rich desktop notifications with session info
set -e

# Read JSON input from stdin
input=$(cat)

# Extract fields
notification_type=$(echo "$input" | jq -r '.notification_type // "unknown"')
message=$(echo "$input" | jq -r '.message // ""')

# ── Gather session context ────────────────────────────────────────
today=$(date +%Y%m%d)
SESSION_FILE=""
for f in /tmp/claude-session-*-${today}.jsonl /tmp/claude-session-${today}.jsonl; do
  [ -f "$f" ] && [ -s "$f" ] && SESSION_FILE="$f"
done

tool_count=0
last_file=""
if [ -n "$SESSION_FILE" ] && [ -s "$SESSION_FILE" ]; then
  tool_count=$(wc -l < "$SESSION_FILE" | tr -d ' ')
  last_file=$(tail -1 "$SESSION_FILE" | jq -r '.file // ""' 2>/dev/null)
  [ -n "$last_file" ] && last_file=$(basename "$last_file")
fi

context_suffix=""
[ "$tool_count" -gt 0 ] && context_suffix=" (${tool_count} tool calls)"
[ -n "$last_file" ] && context_suffix="${context_suffix} — last: ${last_file}"

# ── Send notification per type ────────────────────────────────────
if [[ "$OSTYPE" == "darwin"* ]]; then
  case "$notification_type" in
    "awaiting_user_input")
      osascript -e "display notification \"Claude Code is awaiting your input${context_suffix}\" with title \"Claude Code\" subtitle \"Action Required\" sound name \"Glass\""
      ;;
    "permission_request")
      osascript -e "display notification \"Claude Code needs permission to proceed${context_suffix}\" with title \"Claude Code\" subtitle \"Permission\" sound name \"Ping\""
      ;;
    "task_complete")
      osascript -e "display notification \"Task completed${context_suffix}\" with title \"Claude Code\" subtitle \"Done\" sound name \"Hero\""
      ;;
    "error")
      osascript -e "display notification \"Error occurred${context_suffix}\" with title \"Claude Code\" subtitle \"Error\" sound name \"Basso\""
      ;;
    *)
      if [ -n "$message" ]; then
        osascript -e "display notification \"${message}${context_suffix}\" with title \"Claude Code\" sound name \"Pop\""
      else
        osascript -e "display notification \"Notification${context_suffix}\" with title \"Claude Code\""
      fi
      ;;
  esac
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
  case "$notification_type" in
    "awaiting_user_input")
      notify-send -u normal "Claude Code — Input Required" "Awaiting your input${context_suffix}"
      ;;
    "permission_request")
      notify-send -u critical "Claude Code — Permission" "Needs permission${context_suffix}"
      ;;
    "task_complete")
      notify-send -u low "Claude Code — Done" "Task completed${context_suffix}"
      ;;
    "error")
      notify-send -u critical "Claude Code — Error" "Error occurred${context_suffix}"
      ;;
    *)
      notify-send "Claude Code" "${message:-Notification}${context_suffix}"
      ;;
  esac
fi

exit 0
