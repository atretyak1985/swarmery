#!/bin/bash
# SessionStart hook: carry last session's state into a cold one.
#
# A fresh session starts blind to what the previous one left behind and burns
# tool calls re-deriving it: reading the workspace, guessing which task was in
# flight, re-reading a handoff brief it does not know exists. This hook injects
# two small, high-signal slices instead:
#
#   ### NEXT.md (<task-slug>)      — the newest handoff note in the workspace
#   ### Handoff brief (…)          — the newest daemon-generated brief for this
#                                    project (GET /api/handoffs/latest)
#
# Deliberately bounded: the NEXT.md slice is at most 40 lines / 2048 bytes, the
# brief at most 1024 bytes, and the whole block at most 3072 bytes — the bridge
# must cost far less than the rediscovery it replaces.
#
# Only a cold start gets the bridge. SessionStart also fires on `compact`, and
# a compaction is not a cold start: the session already read its NEXT.md and
# brief, and the point of compacting is to spend LESS context, not to append
# this block again. The hook input's `source` field decides.
#
# Which NEXT.md: the newest one whose task card is still active, and only when
# no task is active the newest overall. A NEXT.md left on a task that was
# completed since must not steer the next session towards finished work.
#
# Kill switch: SWARMERY_CONTEXT_BRIDGE=0 disables it entirely.
#
# Best-effort by construction. No workspace, no daemon, no jq, a malformed
# response — every one of them means "print nothing" and NEVER a non-zero exit:
# a context nicety must not be able to fail a session start.
#
# Vendor-neutral: the workspace root and project namespace come from the
# environment (AGENT_WORKSPACE_ROOT / AGENT_PROJECT), never hard-coded.
set -u

[ "${SWARMERY_CONTEXT_BRIDGE:-1}" = "0" ] && exit 0

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
PORT="${SWARMERY_PORT:-7777}"

MAX_TOTAL=3072    # the whole injected block
MAX_NEXT=2048     # the NEXT.md slice
MAX_HANDOFF=1024  # the handoff brief
NEXT_LINES=40

# ── Workspace location (same resolution as session-start.sh) ──────
if [ -n "${AGENT_PROJECT:-}" ]; then
  working_dir="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace/working"
else
  working_dir="${PROJECT_DIR}/.claude-workspace/working"
fi

# ── Which SessionStart is this? ───────────────────────────────────
# The harness pipes one line of JSON and closes the pipe. A hand-run hook, or
# a caller that inherited an open stdin, must not hang here: read ONE line with
# a bound, and treat anything unreadable as a cold start.
hook_source=""
hook_input=""
if [ ! -t 0 ]; then
  IFS= read -r -t 2 hook_input 2>/dev/null || true
  hook_source=$(printf '%s' "$hook_input" | sed -n 's/.*"source"[[:space:]]*:[[:space:]]*"\([a-z]*\)".*/\1/p' | head -1)
fi
[ "$hook_source" = "compact" ] && exit 0

# ── NEXT.md candidates under working/, newest first (any task layout) ──
# `stat -c '%Y %n'` is the GNU spelling, `stat -f '%m %N'` the BSD/macOS one.
# The one that yields "<digits> <path>" wins — an exit-code fallback would be
# wrong, because GNU's -f prints filesystem status and exits 0.
next_candidates=""
if [ -d "$working_dir" ]; then
  next_candidates=$(find "$working_dir" -maxdepth 6 -name NEXT.md \
    -exec stat -c '%Y %n' {} \; 2>/dev/null | grep -E '^[0-9]+ ' | sort -rn | cut -d' ' -f2-)
  if [ -z "$next_candidates" ]; then
    next_candidates=$(find "$working_dir" -maxdepth 6 -name NEXT.md \
      -exec stat -f '%m %N' {} \; 2>/dev/null | grep -E '^[0-9]+ ' | sort -rn | cut -d' ' -f2-)
  fi
fi

# task_active <NEXT.md> — the task card beside it says the task is still open.
# Same Status: vocabulary session-start.sh uses for its in-flight list.
task_active() {
  local card="$(dirname "$1")/README.md"
  [ -f "$card" ] || return 1
  grep -m1 'Status:' "$card" 2>/dev/null \
    | grep -qiE 'Status:[*]*[[:space:]]*(active|in[-_ ]?progress)'
}

# The newest ACTIVE task's NEXT.md; the newest overall only when none is active.
newest_next=""
active_next=""
while IFS= read -r cand; do
  [ -n "$cand" ] || continue
  [ -n "$newest_next" ] || newest_next="$cand"
  if task_active "$cand"; then
    active_next="$cand"
    break
  fi
done <<EOF
$next_candidates
EOF
[ -n "$active_next" ] && newest_next="$active_next"

# ── Small helpers (jq when present, python3 otherwise) ────────────
urlencode() {
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$1" | jq -sRr @uri
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import sys, urllib.parse; sys.stdout.write(urllib.parse.quote(sys.argv[1], safe=""))' "$1"
  else
    printf '%s' "$1"
  fi
}

# json_field <file> <key> — the key's value as text, empty when absent or when
# the body is not JSON at all.
json_field() {
  if command -v jq >/dev/null 2>&1; then
    jq -r --arg k "$2" '.[$k] // empty' "$1" 2>/dev/null
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json, sys
try:
    doc = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(0)
val = doc.get(sys.argv[2])
if val is not None:
    sys.stdout.write(str(val))
' "$1" "$2" 2>/dev/null
  fi
}

# ── Blocks ───────────────────────────────────────────────────────
emit_next() {
  [ -n "$newest_next" ] || return 0
  [ -f "$newest_next" ] || return 0
  local slug
  slug=$(basename "$(dirname "$newest_next")")
  printf '### NEXT.md (%s)\n' "$slug"
  head -n "$NEXT_LINES" "$newest_next" 2>/dev/null | head -c "$MAX_NEXT"
  printf '\n'
}

# 200 → print the brief; 204, a connection failure, or anything else → nothing.
emit_handoff() {
  command -v curl >/dev/null 2>&1 || return 0
  local tmp code created tokens brief
  tmp=$(mktemp 2>/dev/null) || return 0
  code=$(curl -sS -m 1 -o "$tmp" -w '%{http_code}' \
    "http://127.0.0.1:${PORT}/api/handoffs/latest?cwd=$(urlencode "$PROJECT_DIR")" 2>/dev/null) || code=""
  if [ "$code" = "200" ]; then
    created=$(json_field "$tmp" created_at)
    tokens=$(json_field "$tmp" context_tokens)
    brief=$(json_field "$tmp" brief)
    if [ -n "$brief" ]; then
      printf '### Handoff brief (%s, %s tokens)\n' "$created" "$tokens"
      printf '%s\n' "$brief" | head -c "$MAX_HANDOFF"
      printf '\n'
    fi
  fi
  rm -f "$tmp"
}

block=$(
  emit_next
  emit_handoff
)

[ -n "$block" ] || exit 0
printf '%s\n' "$block" | head -c "$MAX_TOTAL"
exit 0
