#!/bin/bash
# shellcheck disable=SC2034  # colour palette kept complete across hooks
# Activity Tracker Hook for Claude Code
# PostToolUse hook — structured colored output after each tool call
# Tracks session stats in a temp file for aggregation
set -e

# ── Colors & Symbols ──────────────────────────────────────────────
RST='\033[0m'
BOLD='\033[1m'
DIM='\033[2m'
# Foreground
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; MAGENTA='\033[0;35m'
WHITE='\033[1;37m'
# Background accents (palette kept complete for future panels)
# shellcheck disable=SC2034
BG_BLUE='\033[44m'; BG_GREEN='\033[42m'; BG_YELLOW='\033[43m'; BG_RED='\033[41m'; BG_MAGENTA='\033[45m'; BG_CYAN='\033[46m'

# ── Session file (one per day, shared across all hook invocations) ─
SESSION_FILE="/tmp/claude-session-$(date +%Y%m%d).jsonl"

# ── Read hook JSON from stdin ─────────────────────────────────────
input=$(cat)

# Malformed/non-JSON stdin: nothing to track — never break the tool call
# (non-blocking contract; every jq below assumes valid JSON).
if ! printf '%s' "$input" | jq -e . >/dev/null 2>&1; then
  exit 0
fi

tool_name=$(echo "$input" | jq -r '.tool_name // "unknown"' 2>/dev/null || echo unknown)
file_path=$(echo "$input" | jq -r '.tool_input.file_path // .tool_input.path // empty' 2>/dev/null || true)
command=$(echo "$input" | jq -r '.tool_input.command // empty')
pattern=$(echo "$input" | jq -r '.tool_input.pattern // empty')

# ── Model telemetry (Agent dispatches only) ───────────────────────
# requested = what the frontmatter/caller asked for; observed = what the API served.
# A mismatch is logged as a model_fallback event — basis for the routing/cost report.
model_requested=""
model_observed=""
if [ "$tool_name" = "Agent" ]; then
  model_requested=$(echo "$input" | jq -r '.tool_input.model // empty' 2>/dev/null)
  model_observed=$(echo "$input" | jq -r '.tool_response.model // .tool_response.usage.model // empty' 2>/dev/null)
fi

# ── Detect project from file path ────────────────────────────────
# Label = the `apps/<name>` segment if present, else "agents" for .claude edits.
# Generic — no hard-coded repo names; works for any project.
detect_project() {
  local fp="$1"
  case "$fp" in
    */apps/*) local rest="${fp##*/apps/}"; echo "${rest%%/*}";;
    */.claude/*) echo "agents";;
    *) echo "";;
  esac
}

# ── Log tool call to session file (compact, one line per entry) ────
log_entry=$(jq -c -n \
  --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
  --arg tool "$tool_name" \
  --arg file "$file_path" \
  --arg cmd "$command" \
  --arg model_requested "$model_requested" \
  --arg model_observed "$model_observed" \
  '{ts: $ts, tool: $tool, file: $file, cmd: $cmd}
   + (if $model_requested != "" then {model_requested: $model_requested} else {} end)
   + (if $model_observed != "" then {model_observed: $model_observed} else {} end)')
echo "$log_entry" >> "$SESSION_FILE"

if [ -n "$model_requested" ] && [ -n "$model_observed" ] && [ "$model_requested" != "$model_observed" ]; then
  jq -c -n \
    --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg requested "$model_requested" \
    --arg observed "$model_observed" \
    '{ts: $ts, tool: "ModelFallback", file: "", cmd: ("fallback:" + $requested + "->" + $observed)}' >> "$SESSION_FILE"
fi

# ── Count session stats ───────────────────────────────────────────
total_calls=$(wc -l < "$SESSION_FILE" 2>/dev/null | tr -d ' ')
_count() { grep -c "$1" "$SESSION_FILE" 2>/dev/null || true; }
edit_count=$(_count '"tool":"Edit"')
read_count=$(_count '"tool":"Read"')
bash_count=$(_count '"tool":"Bash"')
write_count=$(_count '"tool":"Write"')
grep_count=$(_count '"tool":"Grep"')
glob_count=$(_count '"tool":"Glob"')
agent_count=$(_count '"tool":"Agent"')

# ── Choose icon & color per tool ──────────────────────────────────
case "$tool_name" in
  Edit)   icon="✏️";  color="$YELLOW"; label="EDIT";;
  Write)  icon="📝"; color="$GREEN";  label="WRITE";;
  Read)   icon="📖"; color="$CYAN";   label="READ";;
  Bash)   icon="⚡"; color="$MAGENTA"; label="BASH";;
  Grep)   icon="🔍"; color="$BLUE";   label="GREP";;
  Glob)   icon="📂"; color="$BLUE";   label="GLOB";;
  Agent)  icon="🤖"; color="$RED";    label="AGENT";;
  *)      icon="🔧"; color="$DIM";    label="$tool_name";;
esac

# ── Build the output ──────────────────────────────────────────────
# Shorten file path for display
short_path="$file_path"
if [ -n "$file_path" ]; then
  # Remove the project-root prefix
  short_path="${file_path#${CLAUDE_PROJECT_DIR:-$(pwd)}/}"
fi

project=$(detect_project "$file_path")
project_badge=""
if [ -n "$project" ]; then
  project_badge="${DIM}[${project}]${RST}"
fi

# Stats bar (trim whitespace from counts)
edit_count=$(echo "$edit_count" | tr -d '[:space:]')
read_count=$(echo "$read_count" | tr -d '[:space:]')
bash_count=$(echo "$bash_count" | tr -d '[:space:]')
write_count=$(echo "$write_count" | tr -d '[:space:]')
grep_count=$(echo "$grep_count" | tr -d '[:space:]')
glob_count=$(echo "$glob_count" | tr -d '[:space:]')
agent_count=$(echo "$agent_count" | tr -d '[:space:]')
total_calls=$(echo "$total_calls" | tr -d '[:space:]')

stats="${DIM}#${total_calls} | E:${edit_count} R:${read_count} B:${bash_count} W:${write_count} G:${grep_count} A:${agent_count}${RST}"

# ── Print structured output ───────────────────────────────────────
echo ""
echo -e "${color}┌─ ${icon} ${BOLD}${label}${RST}${color} ─────────────────────────────────────────${RST}"

if [ -n "$short_path" ]; then
  echo -e "${color}│${RST} ${WHITE}${short_path}${RST} ${project_badge}"
fi

if [ -n "$command" ]; then
  # Truncate long commands
  display_cmd="$command"
  if [ ${#display_cmd} -gt 70 ]; then
    display_cmd="${display_cmd:0:67}..."
  fi
  echo -e "${color}│${RST} ${DIM}\$ ${display_cmd}${RST}"
fi

if [ -n "$pattern" ] && [ "$tool_name" = "Grep" ] || [ "$tool_name" = "Glob" ]; then
  echo -e "${color}│${RST} ${DIM}pattern: ${pattern}${RST}"
fi

echo -e "${color}│${RST} ${stats}"
echo -e "${color}└──────────────────────────────────────────────────${RST}"

exit 0
