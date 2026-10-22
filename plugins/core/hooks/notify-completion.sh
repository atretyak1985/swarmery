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
# shellcheck source=lib/session-file.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib/session-file.sh"
SESSION_FILE=$(swarmery_session_file_find)

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
# The text carries outside data (the payload message, a file name), so it is
# handed to osascript as argv and read inside the script as `item N of argv`.
# It is never spliced into the AppleScript source, where a quote in it would
# end the string literal and the rest would compile as code.
# notify_mac <text> <subtitle> <sound> — empty subtitle/sound are omitted.
notify_mac() {
  osascript \
    -e 'on run argv' \
    -e 'set msg to item 1 of argv' \
    -e 'set ttl to item 2 of argv' \
    -e 'set sub to item 3 of argv' \
    -e 'set snd to item 4 of argv' \
    -e 'if sub is "" and snd is "" then' \
    -e 'display notification msg with title ttl' \
    -e 'else if sub is "" then' \
    -e 'display notification msg with title ttl sound name snd' \
    -e 'else if snd is "" then' \
    -e 'display notification msg with title ttl subtitle sub' \
    -e 'else' \
    -e 'display notification msg with title ttl subtitle sub sound name snd' \
    -e 'end if' \
    -e 'end run' \
    -- "$1" "Claude Code" "$2" "$3"
}

if [[ "$OSTYPE" == "darwin"* ]]; then
  case "$notification_type" in
    "awaiting_user_input")
      notify_mac "Claude Code is awaiting your input${context_suffix}" "Action Required" "Glass"
      ;;
    "permission_request")
      notify_mac "Claude Code needs permission to proceed${context_suffix}" "Permission" "Ping"
      ;;
    "task_complete")
      notify_mac "Task completed${context_suffix}" "Done" "Hero"
      ;;
    "error")
      notify_mac "Error occurred${context_suffix}" "Error" "Basso"
      ;;
    *)
      if [ -n "$message" ]; then
        notify_mac "${message}${context_suffix}" "" "Pop"
      else
        notify_mac "Notification${context_suffix}" "" ""
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
