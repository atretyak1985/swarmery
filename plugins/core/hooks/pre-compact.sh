#!/bin/bash
# PreCompact Hook for Claude Code
# Warns when context window is about to be compacted
set -e

# ── Colors ────────────────────────────────────────────────────────
RST='\033[0m'; BOLD='\033[1m'; DIM='\033[2m'
YELLOW='\033[1;33m'; WHITE='\033[1;37m'

# ── Log to session file ──────────────────────────────────────────
SESSION_FILE="/tmp/claude-session-$(date +%Y%m%d).jsonl"
echo "{\"ts\":\"$(date -u +"%Y-%m-%dT%H:%M:%SZ")\",\"tool\":\"_compact_start\",\"file\":\"\",\"cmd\":\"\"}" >> "$SESSION_FILE"

# ── Snapshot newest task checkpoint so post-compaction context can recover it ─
WORKING_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}/.claude-workspace/working"
# shellcheck disable=SC2012  # ls -t for mtime ordering; paths are glob-safe here
# Tasks live under working/YYYY/MM/DD/<slug>/; the extra globs cover the legacy
# working/YYYY/MM/<yyyy-mm-dd-slug>/ and any legacy-flat dirs.
latest_ckpt=$(ls -t "$WORKING_DIR"/*/*/*/*/checkpoint.json "$WORKING_DIR"/*/*/*/checkpoint.json "$WORKING_DIR"/*/checkpoint.json 2>/dev/null | head -1)
if [ -n "$latest_ckpt" ]; then
  jq -c -n \
    --arg ts "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg file "$latest_ckpt" \
    '{ts: $ts, tool: "_checkpoint_ref", file: $file, cmd: ""}' >> "$SESSION_FILE"
fi

# Count tool calls so far
total=$({ wc -l < "$SESSION_FILE" 2>/dev/null || echo 0; } | tr -d ' ')

# ── Print warning ─────────────────────────────────────────────────
echo ""
echo -e "${YELLOW}${BOLD}┌─ ⚠️  CONTEXT COMPACTION ───────────────────────────────${RST}"
echo -e "${YELLOW}${BOLD}│${RST} ${WHITE}Context window is being compressed${RST}"
echo -e "${YELLOW}${BOLD}│${RST} ${DIM}Tool calls so far: ${total}${RST}"
echo -e "${YELLOW}${BOLD}│${RST} ${DIM}Earlier messages will be summarized to free space${RST}"
echo -e "${YELLOW}${BOLD}└───────────────────────────────────────────────────────${RST}"

exit 0
