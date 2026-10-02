#!/bin/bash
# SessionStart hook: log this session's uuid on the active task card.
#
# When $AGENT_TASK_ID names an in-flight agent-work.sh task, append
#   | <date> | <session_uuid> | | |
# to that card's logs/sessions.md — the explicit task↔session link the
# control-plane workspace ingester reads (only uuid-shaped values are
# trustworthy there; this hook is the writer that makes them so).
#
# Task selection: $AGENT_TASK_ID when the caller exports it, otherwise the one
# task whose README says it is active — the same "in-flight" rule session-start.sh
# renders in its banner. Nothing in the fleet actually exports AGENT_TASK_ID, so
# for years the guard here meant this hook wrote nothing at all, leaving
# wsingest's explicit task<->session link with no writer (its heuristic carried
# everything). If two tasks are active the hook does nothing: writing the link
# onto the wrong card is worse than leaving it to the heuristic.
# Never fails session start: every error path exits 0.
set -u

# Session uuid comes from the hook's stdin JSON ({"session_id": "..."}).
input=$(cat 2>/dev/null || true)
session_id=$(printf '%s' "$input" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -n "$session_id" ] || exit 0

# ── Workspace resolution — mirrors agent-work.sh (swarmery model first,
#    legacy project-local .claude-workspace fallback) ────────────────────
if [ -n "${AGENT_PROJECT:-}" ]; then
  ws_root="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}"
  working_dir="${ws_root}/${AGENT_PROJECT}/workspace/working"
else
  working_dir="${CLAUDE_PROJECT_DIR:-$(pwd)}/.claude-workspace/working"
fi

# Task dir: working/YYYY/MM/DD/<slug> derived from the canonical task id
# (yyyy-mm-dd-slug), with agent-work.sh's find-by-slug fallback for cards
# that predate the dated layout.
task_dir=""
if [ -n "${AGENT_TASK_ID:-}" ]; then
  task_id="$AGENT_TASK_ID"
  y="${task_id:0:4}" m="${task_id:5:2}" d="${task_id:8:2}" slug="${task_id:11}"
  task_dir="${working_dir}/${y}/${m}/${d}/${slug}"
  if [ ! -d "$task_dir" ]; then
    task_dir=$(find "$working_dir" -type d -path "*/${slug}" 2>/dev/null | head -1)
  fi
else
  # Exactly one active task, by the same README status-line rule session-start.sh
  # uses for its banner. Zero or several → stay out of it.
  # The card's status line in every spelling a card can carry: the canonical
  # `- **Status**: <value>` (what agent-work.sh init and the daemon's taskdir
  # write — core 3.9.7), the pre-3.9.7 `- **Статус**: <value>`, and the
  # hand-written `Status:` / `**Status:**`. Anchored to the line start so prose
  # that merely mentions a status cannot match.
  card_status_re='^[[:space:]]*(-[[:space:]]*)?[*]*(Status|Статус)[*]*:'
  active=""
  count=0
  while IFS= read -r readme; do
    [ -n "$readme" ] || continue
    status_val=$(grep -m1 -E "$card_status_re" "$readme" 2>/dev/null \
      | sed -E "s/${card_status_re}[*]*[[:space:]]*//" | tr '[:upper:]' '[:lower:]')
    case "$status_val" in
      active*|in-progress*|in_progress*|"in progress"*) ;;
      *) continue ;;
    esac
    active=$(dirname "$readme")
    count=$((count + 1))
  done <<EOF
$(find "$working_dir" -mindepth 5 -maxdepth 5 -name README.md 2>/dev/null | head -50)
EOF
  [ "$count" -eq 1 ] && task_dir="$active"
fi
[ -n "$task_dir" ] && [ -d "$task_dir" ] || exit 0

log="${task_dir}/logs/sessions.md"
mkdir -p "${task_dir}/logs" 2>/dev/null || exit 0

append_row() {
  # Header must match session-summary.sh, the SessionEnd writer of this same
  # file — otherwise whichever hook runs first decides what column 3 means.
  #
  # -s, not -f: on Linux the flock branch below opens fd 9 with `exec 9>>"$log"`,
  # which CREATES the file before this runs. An -f test therefore sees it as
  # existing and skips the header forever, appending rows to a headerless table.
  # macOS has no flock(1) and takes the mkdir path, so this only ever failed on
  # Linux — caught by CI, invisible locally.
  [ -s "$log" ] || printf '| Дата | Сесія | Тулзи | Активність |\n|---|---|---|---|\n' > "$log"
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
