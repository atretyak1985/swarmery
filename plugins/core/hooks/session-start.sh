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
# Components live in two places: this project's own .claude/ overrides and
# the install root of every enabled plugin. Counting only the former made
# the banner read 0/0/0/0 in any consumer that follows the graduation rule
# and keeps no local copies (docs/EXTENDING.md) — i.e. in almost all of them.
# A plugin counts only when it is BOTH enabled and installed, so a pack that
# is switched on but missing from the install registry stays invisible here,
# exactly as it is invisible to the session.
component_counts() {
  SS_PROJECT_DIR="$PROJECT_DIR" node <<'NODE' 2>/dev/null
const fs = require('fs'), path = require('path'), os = require('os');

const projectDir = process.env.SS_PROJECT_DIR;
const claudeDir = path.join(projectDir, '.claude');
const home = os.homedir();

const readJson = (p) => { try { return JSON.parse(fs.readFileSync(p, 'utf8')); } catch (e) { return null; } };

// Later files win, mirroring how the harness layers settings.
const enabled = {};
for (const f of [
  path.join(home, '.claude', 'settings.json'),
  path.join(claudeDir, 'settings.json'),
  path.join(claudeDir, 'settings.local.json'),
]) {
  const s = readJson(f);
  if (s && s.enabledPlugins) Object.assign(enabled, s.enabledPlugins);
}

const reg = readJson(path.join(home, '.claude', 'plugins', 'installed_plugins.json')) || {};
const installed = reg.plugins || reg;

const roots = [claudeDir];
for (const [name, on] of Object.entries(enabled)) {
  if (!on) continue;
  const entries = installed[name];
  if (!Array.isArray(entries)) continue;
  // A project-scoped install pins this project to one version; otherwise the
  // user-scoped one applies. No match at all = enabled but not installed.
  const pick = entries.find((e) => e.scope === 'project' && e.projectPath === projectDir)
    || entries.find((e) => e.scope === 'user');
  if (pick && pick.installPath && fs.existsSync(pick.installPath)) roots.push(pick.installPath);
}

const walk = (dir, keep) => {
  let n = 0;
  let ents;
  try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch (e) { return 0; }
  for (const ent of ents) {
    if (ent.isDirectory()) n += walk(path.join(dir, ent.name), keep);
    else if (keep(ent.name)) n += 1;
  }
  return n;
};

const subdirs = (dir) => {
  try { return fs.readdirSync(dir, { withFileTypes: true }).filter((e) => e.isDirectory()).length; }
  catch (e) { return 0; }
};

const isDoc = (f) => f.endsWith('.md') && f !== 'README.md';
let agents = 0, commands = 0, skills = 0, hooks = 0;
for (const root of [...new Set(roots)]) {
  agents += walk(path.join(root, 'agents'), isDoc);
  commands += walk(path.join(root, 'commands'), isDoc);
  skills += subdirs(path.join(root, 'skills'));
  hooks += walk(path.join(root, 'hooks'), (f) => f.endsWith('.sh'));
}

process.stdout.write(`${agents} ${commands} ${skills} ${hooks}`);
NODE
}

counts=$(component_counts) || counts=""
if [ -z "$counts" ]; then
  # No node on PATH — fall back to this project's own files.
  counts="$(find "${CLAUDE_DIR}/agents" -name "*.md" -not -name "README.md" 2>/dev/null | wc -l | tr -d ' ') \
$(find "${CLAUDE_DIR}/commands" -name "*.md" -not -name "README.md" 2>/dev/null | wc -l | tr -d ' ') \
$(find "${CLAUDE_DIR}/skills" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ') \
$(find "${CLAUDE_DIR}/hooks" -name "*.sh" 2>/dev/null | wc -l | tr -d ' ')"
fi
read -r agent_count command_count skill_count hook_count <<EOF
$counts
EOF

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
  # .git is a dir in a normal checkout, a file in a worktree — accept both.
  if [ -e "${repo_path}/.git" ]; then
    branch=$(git -C "$repo_path" branch --show-current 2>/dev/null || echo "?")
    if [ "$branch" != "main" ] && [ "$branch" != "master" ] && [ -n "$branch" ]; then
      branches="${branches}  ${YELLOW}${repo}${RST} → ${WHITE}${branch}${RST}\n"
    fi
  fi
done <<EOF
$(project_repos)
EOF

# ── In-flight tasks (scan the workspace working/ dir) ────────────
# Task cards live at the task root README.md in two layouts:
#   flat legacy  working/<slug>/README.md              (depth 2)
#   dated        working/<YYYY>/<MM>/<DD>/<slug>/README.md (depth 5)
# swarmery model first (AGENT_PROJECT → sibling workspace); legacy fallback.
if [ -n "${AGENT_PROJECT:-}" ]; then
  working_dir="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace/working"
else
  working_dir="${PROJECT_DIR}/.claude-workspace/working"
fi
inflight=""
newest_next=""
if [ -d "$working_dir" ]; then
  readmes=$(
    { find "$working_dir" -mindepth 2 -maxdepth 2 -name README.md 2>/dev/null
      find "$working_dir" -mindepth 5 -maxdepth 5 -name README.md 2>/dev/null
    } | head -50
  )
  while IFS= read -r readme; do
    [ -n "$readme" ] || continue
    # Active = the "Status:" line reads active / in-progress.
    status_line=$(grep -m1 'Status:' "$readme" 2>/dev/null || true)
    [ -n "$status_line" ] || continue
    status_val=$(printf '%s' "$status_line" | sed 's/^.*Status:[*]*[[:space:]]*//')
    case "$status_val" in
      active*|Active*|ACTIVE*|in-progress*|in_progress*|"in progress"*|IN_PROGRESS*) ;;
      *) continue ;;
    esac
    # First goal line ("Goal:"), truncated to ~70 chars.
    goal=$(grep -m1 'Goal' "$readme" 2>/dev/null | sed 's/^.*Goal:[*]*[[:space:]]*//')
    if [ ${#goal} -gt 70 ]; then
      goal="${goal:0:69}…"
    fi
    name=$(basename "$(dirname "$readme")")
    inflight="${inflight}  ${GREEN}▸${RST} ${WHITE}${name}${RST}  ${DIM}${goal}${RST}\n"
  done <<EOF
$readmes
EOF
  # Newest NEXT.md pointer anywhere under working/ (any layout).
  # `stat -f '%m %N'` is the BSD/macOS spelling; GNU wants `-c '%Y %n'`. Both are
  # tried and the one that yields "<digits> <path>" wins — GNU's -f is not an
  # error (it prints filesystem status and exits 0), so an exit-code fallback
  # would silently sort an unusable block instead.
  newest_next=$(find "$working_dir" -maxdepth 6 -name NEXT.md \
    -exec stat -c '%Y %n' {} \; 2>/dev/null | grep -E '^[0-9]+ ' | sort -rn | head -1 | cut -d' ' -f2-)
  if [ -z "$newest_next" ]; then
    newest_next=$(find "$working_dir" -maxdepth 6 -name NEXT.md \
      -exec stat -f '%m %N' {} \; 2>/dev/null | grep -E '^[0-9]+ ' | sort -rn | head -1 | cut -d' ' -f2-)
  fi
fi

# ── Initialize fresh session file ─────────────────────────────────
# Don't clear previous — post-tool-observe appends to it
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

if [ -n "$inflight" ]; then
  echo -e "${CYAN}${BOLD}│${RST}  ${DIM}In-flight tasks:${RST}"
  echo -e "$inflight" | while IFS= read -r line; do
    [ -n "$line" ] && echo -e "${CYAN}${BOLD}│${RST}${line}"
  done || true
  if [ -n "$newest_next" ]; then
    echo -e "${CYAN}${BOLD}│${RST}    ${DIM}NEXT → ${newest_next#"${PROJECT_DIR}"/}${RST}"
  fi
  echo -e "${CYAN}${BOLD}│${RST}"
fi

echo -e "${CYAN}${BOLD}│${RST}  ${DIM}Quick commands:${RST}"
echo -e "${CYAN}${BOLD}│${RST}    ${WHITE}/dashboard${RST}  ${DIM}— session stats & system overview${RST}"
echo -e "${CYAN}${BOLD}│${RST}    ${WHITE}/cost${RST}       ${DIM}— token usage & cost${RST}"
# A plugin agent is addressed <plugin>:<agent>; only a project-local
# override under .claude/agents/ answers to the bare name.
if [ -f "${CLAUDE_DIR}/agents/tech-lead.md" ]; then
  tech_lead_ref="@tech-lead"
else
  tech_lead_ref="@core:tech-lead"
fi
echo -e "${CYAN}${BOLD}│${RST}    ${WHITE}${tech_lead_ref}${RST}  ${DIM}— orchestrate complex tasks${RST}"
echo -e "${CYAN}${BOLD}│${RST}"
echo -e "${CYAN}${BOLD}└──────────────────────────────────────────────────────┘${RST}"
echo ""

# ── Minimum Claude Code version ───────────────────────────────────
# core 3.0 leans on native harness behaviour that older builds lack:
# the Agent hook matcher, native read-before-edit (which replaced this
# plugin's own read-before-write.sh), and dynamic workflow orchestration
# on tech-lead's large route. Warn, never block — a stale build still
# works, it just silently loses those guarantees.
CORE_MIN_CC="2.1.160"
cc_ver=$(claude --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
if [ -n "$cc_ver" ]; then
  older=$(printf '%s\n%s\n' "$CORE_MIN_CC" "$cc_ver" | sort -V | head -1)
  if [ "$cc_ver" != "$CORE_MIN_CC" ] && [ "$older" = "$cc_ver" ]; then
    echo -e "${YELLOW}⚠  Claude Code ${cc_ver} is older than core 3.0's minimum ${CORE_MIN_CC}.${RST}"
    echo -e "${DIM}   Native read-before-edit, the Agent hook matcher and dynamic${RST}"
    echo -e "${DIM}   workflow routing may be missing — see docs/MIGRATION-core-3.md.${RST}"
    echo ""
  fi
fi

exit 0
