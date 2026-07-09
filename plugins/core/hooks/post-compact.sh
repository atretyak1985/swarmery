#!/bin/bash
# PostCompact Hook for Claude Code
# Confirms context compaction completed
set -e

# ── Colors ────────────────────────────────────────────────────────
RST='\033[0m'; BOLD='\033[1m'; DIM='\033[2m'
GREEN='\033[0;32m'; WHITE='\033[1;37m'

# ── Log to session file ──────────────────────────────────────────
SESSION_FILE="/tmp/claude-session-$(date +%Y%m%d).jsonl"
echo "{\"ts\":\"$(date -u +"%Y-%m-%dT%H:%M:%SZ")\",\"tool\":\"_compact_done\",\"file\":\"\",\"cmd\":\"\"}" >> "$SESSION_FILE"

# Count compactions today
compact_count=$({ grep -c '"_compact_done"' "$SESSION_FILE" 2>/dev/null || true; } | tr -d '[:space:]')
[ -z "$compact_count" ] && compact_count=1

# ── Print ─────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}┌─ ✅ COMPACTION DONE ────────────────────────────────────${RST}"
echo -e "${GREEN}${BOLD}│${RST} ${WHITE}Context compressed successfully${RST}"
echo -e "${GREEN}${BOLD}│${RST} ${DIM}Compactions today: ${compact_count}${RST}"
echo -e "${GREEN}${BOLD}│${RST} ${DIM}Session continues with summarized history${RST}"
latest_ckpt=$(grep '"_checkpoint_ref"' "$SESSION_FILE" 2>/dev/null | tail -1 | jq -r '.file // empty' 2>/dev/null)
if [ -n "$latest_ckpt" ]; then
  echo -e "${GREEN}${BOLD}│${RST} ${DIM}Resume checkpoint: ${latest_ckpt}${RST}"
fi
echo -e "${GREEN}${BOLD}└───────────────────────────────────────────────────────${RST}"

exit 0
