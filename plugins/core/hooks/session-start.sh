#!/bin/bash
# Session Start Hook for Claude Code
# Shows a welcome banner with system stats on session start
set -e

# ── Colors ────────────────────────────────────────────────────────
RST='\033[0m'
BOLD='\033[1m'
DIM='\033[2m'
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; MAGENTA='\033[0;35m'
WHITE='\033[1;37m'

# ── Paths ─────────────────────────────────────────────────────────
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
CLAUDE_DIR="${PROJECT_DIR}/.claude"
PROJECT_JSON="${CLAUDE_DIR}/project.json"

# Per-project flavor (repos, name) comes from project.json — never hard-coded.
project_repos() {
  [ -f "$PROJECT_JSON" ] || return 0
  node -e "try{const r=require('$PROJECT_JSON').repos||[];process.stdout.write(r.join('\n'))}catch(e){}" 2>/dev/null
}

project_display_name() {
  local name=""
  if [ -f "$PROJECT_JSON" ]; then
    name=$(node -e "try{process.stdout.write(require('$PROJECT_JSON').displayName||'')}catch(e){}" 2>/dev/null)
  fi
  printf '%s' "${name:-Project}"
}

# ── Count system components ───────────────────────────────────────
agent_count=$(find "${CLAUDE_DIR}/agents" -name "*.md" -not -name "README.md" 2>/dev/null | wc -l | tr -d ' ')
command_count=$(find "${CLAUDE_DIR}/commands" -name "*.md" -not -name "README.md" 2>/dev/null | wc -l | tr -d ' ')
skill_count=$(find "${CLAUDE_DIR}/skills" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
hook_count=$(find "${CLAUDE_DIR}/hooks" -name "*.sh" 2>/dev/null | wc -l | tr -d ' ')

# ── Check for previous session data ──────────────────────────────
today=$(date +%Y%m%d)
prev_session="/tmp/claude-session-${today}.jsonl"
prev_calls=0
if [ -f "$prev_session" ] && [ -s "$prev_session" ]; then
  prev_calls=$(wc -l < "$prev_session" | tr -d ' ')
fi

# ── Current time ──────────────────────────────────────────────────
current_time=$(date +"%H:%M")
current_date=$(date +"%Y-%m-%d (%A)")

# ── Git branch info (quick, no fetch) ────────────────────────────
# Repos come from project.json (project.repos); the project root is also checked.
branches=""
while IFS= read -r repo; do
  [ -z "$repo" ] && continue
  repo_path="${PROJECT_DIR}/${repo}"
  if [ -d "${repo_path}/.git" ]; then
    branch=$(git -C "$repo_path" branch --show-current 2>/dev/null || echo "?")
    if [ "$branch" != "main" ] && [ "$branch" != "master" ] && [ -n "$branch" ]; then
      branches="${branches}  ${YELLOW}${repo}${RST} → ${WHITE}${branch}${RST}\n"
    fi
  fi
done <<EOF
$(project_repos)
EOF

# ── Initialize fresh session file ─────────────────────────────────
# Don't clear previous — activity-tracker appends to it
# Just mark session start
echo "{\"ts\":\"$(date -u +"%Y-%m-%dT%H:%M:%SZ")\",\"tool\":\"_session_start\",\"file\":\"\",\"cmd\":\"\"}" >> "$prev_session"

# ── Print banner ──────────────────────────────────────────────────
echo ""
echo -e "${CYAN}${BOLD}┌──────────────────────────────────────────────────────┐${RST}"
echo -e "${CYAN}${BOLD}│${RST}  ${WHITE}${BOLD}🤖 $(project_display_name) Agent System${RST}              ${DIM}${current_time}${RST}  ${CYAN}${BOLD}│${RST}"
echo -e "${CYAN}${BOLD}│${RST}  ${DIM}${current_date}${RST}"
echo -e "${CYAN}${BOLD}├──────────────────────────────────────────────────────┤${RST}"
echo -e "${CYAN}${BOLD}│${RST}"
echo -e "${CYAN}${BOLD}│${RST}  ${GREEN}Agents:${RST} ${WHITE}${agent_count}${RST}  ${BLUE}Commands:${RST} ${WHITE}${command_count}${RST}  ${MAGENTA}Skills:${RST} ${WHITE}${skill_count}${RST}  ${YELLOW}Hooks:${RST} ${WHITE}${hook_count}${RST}"
echo -e "${CYAN}${BOLD}│${RST}"

if [ "$prev_calls" -gt 1 ]; then
  echo -e "${CYAN}${BOLD}│${RST}  ${DIM}Previous activity today: ${prev_calls} tool calls${RST}"
  echo -e "${CYAN}${BOLD}│${RST}"
fi

if [ -n "$branches" ]; then
  echo -e "${CYAN}${BOLD}│${RST}  ${DIM}Active branches:${RST}"
  echo -e "$branches" | while IFS= read -r line; do
    [ -n "$line" ] && echo -e "${CYAN}${BOLD}│${RST}${line}"
  done || true
  echo -e "${CYAN}${BOLD}│${RST}"
fi

echo -e "${CYAN}${BOLD}│${RST}  ${DIM}Quick commands:${RST}"
echo -e "${CYAN}${BOLD}│${RST}    ${WHITE}/dashboard${RST}  ${DIM}— session stats & system overview${RST}"
echo -e "${CYAN}${BOLD}│${RST}    ${WHITE}/cost${RST}       ${DIM}— token usage & cost${RST}"
echo -e "${CYAN}${BOLD}│${RST}    ${WHITE}@tech-lead${RST}  ${DIM}— orchestrate complex tasks${RST}"
echo -e "${CYAN}${BOLD}│${RST}"
echo -e "${CYAN}${BOLD}└──────────────────────────────────────────────────────┘${RST}"
echo ""

exit 0
