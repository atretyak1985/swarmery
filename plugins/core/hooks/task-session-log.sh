#!/bin/bash
# SessionStart hook: log this session's uuid on the active task card.
#
# When $AGENT_TASK_ID names an in-flight agent-work.sh task, append
#   | <date> | <session_uuid> | | |
# to that card's logs/sessions.md — the explicit task↔session link the
# control-plane workspace ingester reads (only uuid-shaped values are
# trustworthy there; this hook is the writer that makes them so).
#
# Writes ONLY when AGENT_TASK_ID is set; otherwise it exits untouched.
# Never fails session start: every error path exits 0.
set -u

[ -n "${AGENT_TASK_ID:-}" ] || exit 0

# Session uuid comes from the hook's stdin JSON ({"session_id": "..."}).
input=$(cat 2>/dev/null || true)
session_id=$(printf '%s' "$input" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -n "$session_id" ] || exit 0

# ── Workspace resolution — mirrors agent-work.sh (swarmery model first,
#    legacy project-local .claude-workspace fallback) ────────────────────
if [ -n "${AGENT_PROJECT:-}" ]; then
  ws_root="${AGENT_WORKSPACE_ROOT:-/Volumes/Work/swarmery-workspace}"
  working_dir="${ws_root}/${AGENT_PROJECT}/workspace/working"
else
  working_dir="${CLAUDE_PROJECT_DIR:-$(pwd)}/.claude-workspace/working"
fi

# Task dir: working/YYYY/MM/DD/<slug> derived from the canonical task id
# (yyyy-mm-dd-slug), with agent-work.sh's find-by-slug fallback for cards
# that predate the dated layout.
task_id="$AGENT_TASK_ID"
y="${task_id:0:4}" m="${task_id:5:2}" d="${task_id:8:2}" slug="${task_id:11}"
task_dir="${working_dir}/${y}/${m}/${d}/${slug}"
if [ ! -d "$task_dir" ]; then
  task_dir=$(find "$working_dir" -type d -path "*/${slug}" 2>/dev/null | head -1)
fi
[ -n "$task_dir" ] && [ -d "$task_dir" ] || exit 0

log="${task_dir}/logs/sessions.md"
mkdir -p "${task_dir}/logs" 2>/dev/null || exit 0

append_row() {
  [ -f "$log" ] || printf '| Дата | Сесія | Тривалість | Активність |\n|---|---|---|---|\n' > "$log"
  # Resume/clear re-fires SessionStart — one row per session uuid is enough.
  grep -q "$session_id" "$log" 2>/dev/null && return 0
  printf '| %s | %s | | |\n' "$(date +%Y-%m-%d)" "$session_id" >> "$log"
}

# Serialize concurrent SessionStart hooks: flock on the log itself where
# available (Linux); macOS ships no flock(1) → atomic-mkdir spin lock.
if command -v flock >/dev/null 2>&1; then
  exec 9>>"$log"
  flock -w 5 9 2>/dev/null && append_row
else
  lockdir="${log}.lock"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if mkdir "$lockdir" 2>/dev/null; then
      append_row
      rmdir "$lockdir" 2>/dev/null
      break
    fi
    sleep 0.2
  done
fi

exit 0
