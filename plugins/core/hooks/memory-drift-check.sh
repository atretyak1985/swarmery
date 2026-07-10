#!/bin/bash
# memory-drift-check.sh — OPT-IN hook (not wired into hooks.json by default).
# Warns (never blocks) when .claude/agent-memory/*.md references repos/apps that
# no longer exist in the project's .claude/project.json — i.e. the per-agent
# memory files have drifted from reality after a refactor/rename.
#
# Wire it per-project (e.g. SubagentStop or SessionStart) if the project keeps
# agent-memory files:
#   { "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/memory-drift-check.sh" }
set -uo pipefail

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
MEM_DIR="${PROJECT_DIR}/.claude/agent-memory"
PROJECT_JSON="${PROJECT_DIR}/.claude/project.json"

[ -d "$MEM_DIR" ] || exit 0
[ -f "$PROJECT_JSON" ] || exit 0

# Known-good vocabulary: repos + apps + mainApp + device from project.json
known=$(node -e "
try {
  const p = require('$PROJECT_JSON');
  const v = new Set([...(p.repos||[]), ...(p.apps||[]), p.mainApp, p.device, p.monorepo].filter(Boolean));
  console.log([...v].join('\n'));
} catch (e) {}
" 2>/dev/null)
[ -n "$known" ] || exit 0

# Heuristic: repo-shaped tokens in memory files (path-like segments under apps/ or
# kebab-case dir refs followed by /) that are NOT in the known vocabulary.
drift=""
while IFS= read -r f; do
  hits=$(grep -ohE 'apps/[a-z0-9-]+' "$f" 2>/dev/null | sed 's#apps/##' | sort -u)
  while IFS= read -r h; do
    [ -z "$h" ] && continue
    echo "$known" | grep -qx "apps/$h" && continue
    echo "$known" | grep -qx "$h" && continue
    drift="${drift}  $(basename "$f"): references apps/$h (not in project.json)\n"
  done <<< "$hits"
done < <(find "$MEM_DIR" -name '*.md' 2>/dev/null)

if [ -n "$drift" ]; then
  echo "⚠️  agent-memory drift vs project.json (update the memory files):"
  printf '%b' "$drift"
fi
exit 0
