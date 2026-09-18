#!/bin/bash
# Session Summary Hook for Claude Code
# Stop hook — aggregates all tool calls and shows a colored session summary
#
# It also owns log ROTATION for the append-only files it feeds, because this is
# the workspace's write path (the daemon only ever reads the workspace). Windows:
#   ${WS_ROOT}/sessions/*.json          30d
#   ${WS_ROOT}/metrics/session-*.jsonl  30d
#   ${WS_ROOT}/logs/trace-*.jsonl       30d
#   /tmp/claude-session-*.jsonl          7d   (per-day statusline substrate)
#
# The metrics window covers session-*.jsonl ONLY, never metrics/*.jsonl. The
# other files there (bash-shape-guard.jsonl, gate-bypasses.jsonl) are single
# append-only AUDIT logs, not one-file-per-session: an mtime sweep would delete
# the entire guard/bypass history 30 days after its last write — i.e. exactly
# when a quiet period makes it most worth keeping. Only per-session files rotate.
#
# ${WS_ROOT}/working/** is never swept — task dirs are the work product.
# The /tmp sweep runs OUTSIDE the workspace block (which is NOT the same as
# unconditionally): WS_ROOT is empty whenever AGENT_PROJECT is unset and no
# ancestor carries both .claude-workspace and .claude, and in that configuration
# a workspace-gated sweep would let /tmp grow forever — the unbounded growth
# this rotation exists to stop.
#
# The real precondition for EVERY sweep here is the early exit further down:
# with no non-empty substrate file for today, the hook `exit 0`s before any
# rotation runs, so a session that recorded no tool calls rotates nothing at
# all. That is accepted rather than fixed — this is a summary hook with nothing
# to summarise, and a machine idle enough to produce no substrate file is not
# the machine whose /tmp is growing — but it does mean the sweeps are reached,
# not guaranteed.
# Covered by scripts/tests/session-summary-rotation.test.sh.
set -e

# ── Colors ────────────────────────────────────────────────────────
RST='\033[0m'
BOLD='\033[1m'
DIM='\033[2m'
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; MAGENTA='\033[0;35m'
WHITE='\033[1;37m'

# ── Find today's session file ────────────────────────────────────
today=$(date +%Y%m%d)
SESSION_FILE=""

# Where the per-day statusline substrate lives. The default is the literal /tmp
# every other hook in this pack writes to (session-start, subagent-start/stop,
# pre/post-compact, notify-completion) — this variable exists so the rotation
# test can point the hook at a sandbox instead of sweeping a real developer's
# /tmp. Overriding it in production would desync this hook from the six that
# hardcode /tmp, so leave it unset outside tests.
CLAUDE_SESSION_TMP="${CLAUDE_SESSION_TMP:-/tmp}"

# Try to find the most recent session file for today
for f in "${CLAUDE_SESSION_TMP}"/claude-session-*-"${today}".jsonl "${CLAUDE_SESSION_TMP}/claude-session-${today}.jsonl"; do
  if [ -f "$f" ] && [ -s "$f" ]; then
    SESSION_FILE="$f"
  fi
done

if [ -z "$SESSION_FILE" ] || [ ! -s "$SESSION_FILE" ]; then
  exit 0
fi

# ── Calculate stats ───────────────────────────────────────────────
total=$(wc -l < "$SESSION_FILE" | tr -d ' ')

# Tool counts (grep -c outputs "0" on no match but exits 1; || true prevents set -e abort)
_count() { grep -c "$1" "$SESSION_FILE" 2>/dev/null || true; }
edit_n=$(_count '"tool":"Edit"')
read_n=$(_count '"tool":"Read"')
bash_n=$(_count '"tool":"Bash"')
write_n=$(_count '"tool":"Write"')
grep_n=$(_count '"tool":"Grep"')
glob_n=$(_count '"tool":"Glob"')
agent_n=$(_count '"tool":"Agent"')

# Session duration (first entry to last entry)
first_ts=$(head -1 "$SESSION_FILE" | jq -r '.ts // empty' 2>/dev/null)
last_ts=$(tail -1 "$SESSION_FILE" | jq -r '.ts // empty' 2>/dev/null)

duration_str="unknown"
if [ -n "$first_ts" ] && [ -n "$last_ts" ]; then
  # macOS date parsing
  first_epoch=$(date -j -f "%Y-%m-%dT%H:%M:%SZ" "$first_ts" +%s 2>/dev/null || date -d "$first_ts" +%s 2>/dev/null || echo 0)
  last_epoch=$(date -j -f "%Y-%m-%dT%H:%M:%SZ" "$last_ts" +%s 2>/dev/null || date -d "$last_ts" +%s 2>/dev/null || echo 0)

  if [ "$first_epoch" -gt 0 ] && [ "$last_epoch" -gt 0 ]; then
    diff_s=$((last_epoch - first_epoch))
    mins=$((diff_s / 60))
    secs=$((diff_s % 60))
    if [ $mins -gt 60 ]; then
      hours=$((mins / 60))
      mins=$((mins % 60))
      duration_str="${hours}h ${mins}m ${secs}s"
    elif [ $mins -gt 0 ]; then
      duration_str="${mins}m ${secs}s"
    else
      duration_str="${secs}s"
    fi
  fi
fi

# Unique files touched
files_touched=$(jq -r 'select(.file != "") | .file' "$SESSION_FILE" 2>/dev/null | sort -u | wc -l | tr -d ' ')

# Unique projects
# Component label = the `apps/<name>` segment, else "agents" — generic, no hard-coded repos.
# Case patterns need the leading "(" — bash 3.2 (macOS /bin/bash) misparses a bare
# "pattern)" inside $(...) as the end of the command substitution.
projects=$(jq -r 'select(.file != "") | .file' "$SESSION_FILE" 2>/dev/null | while read -r fp; do
  case "$fp" in
    (*/apps/*) rest="${fp##*/apps/}"; echo "${rest%%/*}";;
    (*/.claude/*) echo "agents";;
  esac
done | sort -u | tr '\n' ', ' | sed 's/,$//')

# ── Build tool breakdown bar ──────────────────────────────────────
tool_bar=""
[ "$edit_n" -gt 0 ] && tool_bar="${tool_bar}${YELLOW}Edit:${edit_n}${RST} "
[ "$read_n" -gt 0 ] && tool_bar="${tool_bar}${CYAN}Read:${read_n}${RST} "
[ "$bash_n" -gt 0 ] && tool_bar="${tool_bar}${MAGENTA}Bash:${bash_n}${RST} "
[ "$write_n" -gt 0 ] && tool_bar="${tool_bar}${GREEN}Write:${write_n}${RST} "
[ "$grep_n" -gt 0 ] && tool_bar="${tool_bar}${BLUE}Grep:${grep_n}${RST} "
[ "$glob_n" -gt 0 ] && tool_bar="${tool_bar}${BLUE}Glob:${glob_n}${RST} "
[ "$agent_n" -gt 0 ] && tool_bar="${tool_bar}${RED}Agent:${agent_n}${RST} "

# ── Visual bar chart ─────────────────────────────────────────────
bar_width=30
make_bar() {
  local count=$1 max=$2 color=$3
  if [ "$max" -eq 0 ]; then echo ""; return; fi
  local filled=$(( (count * bar_width) / max ))
  [ "$filled" -eq 0 ] && [ "$count" -gt 0 ] && filled=1
  local empty=$((bar_width - filled))
  printf '%b' "${color}"
  printf '█%.0s' $(seq 1 "$filled" 2>/dev/null) 2>/dev/null || printf '#%.0s' $(seq 1 "$filled")
  printf '%b' "${DIM}"
  [ "$empty" -gt 0 ] && printf '░%.0s' $(seq 1 "$empty" 2>/dev/null) 2>/dev/null || true
  printf '%b' "${RST}"
}

max_tool=$edit_n
[ "$read_n" -gt "$max_tool" ] && max_tool=$read_n
[ "$bash_n" -gt "$max_tool" ] && max_tool=$bash_n
[ "$write_n" -gt "$max_tool" ] && max_tool=$write_n
[ "$grep_n" -gt "$max_tool" ] && max_tool=$grep_n
[ "$glob_n" -gt "$max_tool" ] && max_tool=$glob_n
[ "$agent_n" -gt "$max_tool" ] && max_tool=$agent_n

# ── Print summary ────────────────────────────────────────────────
echo ""
echo ""
echo -e "${CYAN}${BOLD}╔══════════════════════════════════════════════════════╗${RST}"
echo -e "${CYAN}${BOLD}║${RST}  ${WHITE}${BOLD}📊 Session Summary${RST}                                  ${CYAN}${BOLD}║${RST}"
echo -e "${CYAN}${BOLD}╠══════════════════════════════════════════════════════╣${RST}"
echo -e "${CYAN}${BOLD}║${RST}                                                      ${CYAN}${BOLD}║${RST}"
echo -e "${CYAN}${BOLD}║${RST}  ${DIM}Duration:${RST}     ${WHITE}${BOLD}${duration_str}${RST}"
echo -e "${CYAN}${BOLD}║${RST}  ${DIM}Tool calls:${RST}   ${WHITE}${BOLD}${total}${RST} total"
echo -e "${CYAN}${BOLD}║${RST}  ${DIM}Files:${RST}        ${WHITE}${files_touched}${RST} unique files"
[ -n "$projects" ] && echo -e "${CYAN}${BOLD}║${RST}  ${DIM}Projects:${RST}     ${GREEN}${projects}${RST}"
echo -e "${CYAN}${BOLD}║${RST}"
echo -e "${CYAN}${BOLD}║${RST}  ${DIM}── Tool Breakdown ──${RST}"
echo -e "${CYAN}${BOLD}║${RST}  ${tool_bar}"
echo -e "${CYAN}${BOLD}║${RST}"

# Print bar chart for tools with > 0 calls
if [ "$edit_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${YELLOW}Edit ${RST} $(make_bar "$edit_n" "$max_tool" "$YELLOW") ${DIM}${edit_n}${RST}"
fi
if [ "$read_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${CYAN}Read ${RST} $(make_bar "$read_n" "$max_tool" "$CYAN") ${DIM}${read_n}${RST}"
fi
if [ "$bash_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${MAGENTA}Bash ${RST} $(make_bar "$bash_n" "$max_tool" "$MAGENTA") ${DIM}${bash_n}${RST}"
fi
if [ "$write_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${GREEN}Write${RST} $(make_bar "$write_n" "$max_tool" "$GREEN") ${DIM}${write_n}${RST}"
fi
if [ "$grep_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${BLUE}Grep ${RST} $(make_bar "$grep_n" "$max_tool" "$BLUE") ${DIM}${grep_n}${RST}"
fi
if [ "$glob_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${BLUE}Glob ${RST} $(make_bar "$glob_n" "$max_tool" "$BLUE") ${DIM}${glob_n}${RST}"
fi
if [ "$agent_n" -gt 0 ]; then
  echo -e "${CYAN}${BOLD}║${RST}  ${RED}Agent${RST} $(make_bar "$agent_n" "$max_tool" "$RED") ${DIM}${agent_n}${RST}"
fi

echo -e "${CYAN}${BOLD}║${RST}"
echo -e "${CYAN}${BOLD}║${RST}  ${DIM}Tip: use ${WHITE}/cost${RST}${DIM} for token & cost details${RST}"
echo -e "${CYAN}${BOLD}║${RST}"
echo -e "${CYAN}${BOLD}╚══════════════════════════════════════════════════════╝${RST}"
echo ""

# ── Archive session log to workspace metrics ──────────────────────
# Workspace resolution: swarmery model (AGENT_PROJECT → sibling swarmery-workspace) with
# legacy project-local .claude-workspace fallback.
if [ -n "${AGENT_PROJECT:-}" ]; then
  _WS="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace"
else
  _WS="${CLAUDE_PROJECT_DIR:-.}/.claude-workspace"
fi
METRICS_DIR="${_WS}/metrics"
if [ -d "$METRICS_DIR" ] || mkdir -p "$METRICS_DIR" 2>/dev/null; then
  cp "$SESSION_FILE" "${METRICS_DIR}/session-${today}-$$.jsonl" 2>/dev/null || true
fi

# ── Structured diff log (.swarmery/sessions/<date>.json) ───────────
# Audit trail: which repos changed, what file list, diff stats. Used by
# the session-closeout and founder-reality-check skills. Best-effort; never blocks the hook.
SESSIONS_DIR="${CLAUDE_PROJECT_DIR:-.}/.swarmery/sessions"
if mkdir -p "$SESSIONS_DIR" 2>/dev/null; then
  diff_json_file="${SESSIONS_DIR}/${today}-$$.json"

  # Extract unique repo roots from files-touched list. A repo root is the
  # nearest ancestor containing a .git directory. We sample up to 50 unique
  # files to avoid pathological scans.
  declare -a REPO_ROOTS=()
  while IFS= read -r fp; do
    [ -z "$fp" ] && continue
    [ ! -e "$fp" ] && continue
    dir="$(dirname "$fp")"
    while [ "$dir" != "/" ] && [ "$dir" != "." ]; do
      if [ -d "$dir/.git" ] || [ -f "$dir/.git" ]; then
        # de-dup
        if [[ " ${REPO_ROOTS[*]:-} " != *" ${dir} "* ]]; then
          REPO_ROOTS+=("$dir")
        fi
        break
      fi
      dir="$(dirname "$dir")"
    done
  done < <(jq -r 'select(.file != "") | .file' "$SESSION_FILE" 2>/dev/null | sort -u | head -50)

  # Build per-repo diff stats (best-effort; ignore non-git or read-only).
  repos_json="["
  first=1
  for repo in "${REPO_ROOTS[@]:-}"; do
    [ -z "$repo" ] && continue
    name="$(basename "$repo")"
    # head -1 + tr: a commitless repo prints "HEAD" to stdout AND exits non-zero,
    # which used to embed a newline ("HEAD\nunknown") and break the JSON.
    branch=$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null | head -1 | tr -d '\n')
    [ -z "$branch" ] && branch="unknown"
    stat_line=$(git -C "$repo" diff --shortstat 2>/dev/null | tr -d '\n' | sed 's/"/\\"/g')
    staged_stat=$(git -C "$repo" diff --cached --shortstat 2>/dev/null | tr -d '\n' | sed 's/"/\\"/g')
    files_changed=$(git -C "$repo" diff --name-only 2>/dev/null | head -50 | jq -R . | jq -s . 2>/dev/null || echo "[]")
    if [ $first -eq 0 ]; then repos_json="${repos_json},"; fi
    repos_json="${repos_json}{\"name\":\"${name}\",\"path\":\"${repo}\",\"branch\":\"${branch}\",\"unstaged\":\"${stat_line}\",\"staged\":\"${staged_stat}\",\"files\":${files_changed}}"
    first=0
  done
  repos_json="${repos_json}]"

  end_ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

  cat > "$diff_json_file" <<JSON
{
  "session_id": "$$",
  "date": "${today}",
  "ended_at": "${end_ts}",
  "duration": "${duration_str}",
  "tool_counts": {
    "edit": ${edit_n:-0},
    "read": ${read_n:-0},
    "bash": ${bash_n:-0},
    "write": ${write_n:-0},
    "grep": ${grep_n:-0},
    "glob": ${glob_n:-0},
    "agent": ${agent_n:-0},
    "total": ${total:-0}
  },
  "files_touched": ${files_touched:-0},
  "repos": ${repos_json}
}
JSON

  # Validate JSON; keep a .bad copy on failure so breakage is debuggable.
  if command -v jq >/dev/null 2>&1; then
    if ! jq empty "$diff_json_file" >/dev/null 2>&1; then
      mv "$diff_json_file" "${diff_json_file}.bad" 2>/dev/null || rm -f "$diff_json_file"
      diff_json_file=""
    fi
  fi

  # Rotate: drop session logs older than 30 days to prevent unbounded growth.
  find "$SESSIONS_DIR" -name '*.json' -type f -mtime +30 -delete 2>/dev/null || true
fi

# ── Workspace standard (2026-06-10): session mirror + task linking + INDEX ──
# swarmery model first (AGENT_PROJECT → sibling swarmery-workspace); legacy walk-up fallback.
WS_ROOT=""
if [ -n "${AGENT_PROJECT:-}" ] && [ -d "${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace" ]; then
  WS_ROOT="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace"
else
  _p="${CLAUDE_PROJECT_DIR:-$PWD}"
  while [ "$_p" != "/" ]; do
    # true root carries BOTH dirs — guards against stray .claude-workspace in sub-repos
    if [ -d "${_p}/.claude-workspace" ] && [ -e "${_p}/.claude" ]; then WS_ROOT="${_p}/.claude-workspace"; break; fi
    _p="$(dirname "$_p")"
  done
fi

# 0) Rotate the per-day statusline substrate in /tmp. This sweep is OUTSIDE the
#    workspace block on purpose: WS_ROOT is empty whenever AGENT_PROJECT is unset
#    AND no ancestor of the project dir carries both .claude-workspace and
#    .claude, and gating the sweep on it meant /tmp grew without bound in exactly
#    that configuration — the failure this rotation exists to prevent. The files
#    are regenerated per day and only ever read for "today", hence 7d, tighter
#    than the 30d workspace windows below. -maxdepth 1 and -type f keep it to the
#    flat substrate files; || true keeps a SessionEnd hook from failing on a
#    read-only or absent path.
#
#    "Outside the workspace block" is the ONLY guarantee here — it is not
#    unconditional. The SESSION_FILE check near the top of this script exits 0
#    when today's substrate file is missing or empty, which is upstream of this
#    line, so a session that recorded no tool calls never reaches this sweep.
#    Accepted: no substrate file today means nothing was added to /tmp today
#    either, so the backlog cannot grow while the sweep is being skipped.
find "$CLAUDE_SESSION_TMP" -maxdepth 1 -name 'claude-session-*.jsonl' -type f -mtime +7 -delete 2>/dev/null || true

if [ -n "$WS_ROOT" ]; then
  human_date=$(date +%Y-%m-%d)

  # 1) Mirror this session's structured JSON into the workspace (human-dated name).
  if mkdir -p "${WS_ROOT}/sessions" 2>/dev/null; then
    [ -n "${diff_json_file:-}" ] && [ -f "${diff_json_file:-}" ] && \
      cp "$diff_json_file" "${WS_ROOT}/sessions/${human_date}-$$.json" 2>/dev/null || true
    find "${WS_ROOT}/sessions" -name '*.json' -type f -mtime +30 -delete 2>/dev/null || true
  fi

  # 1b) Rotate the other append-only workspace logs nobody rotates. Each of these
  #     grows one file per session/day forever; the sessions mirror above was the
  #     only one with a window. 30d matches that mirror. (The /tmp substrate has
  #     its own, tighter window and is swept above — outside this workspace
  #     block, though still downstream of the SESSION_FILE early exit, so
  #     "outside the workspace block" rather than "unconditionally".)
  #
  #     The metrics pattern is 'session-*.jsonl', NOT '*.jsonl': the other files
  #     in that directory (bash-shape-guard.jsonl, gate-bypasses.jsonl) are SINGLE
  #     append-only audit logs rather than one-file-per-session, so an mtime sweep
  #     would delete the whole audit trail 30 days after its last write.
  #
  #     Deliberately NOT swept here: ${WS_ROOT}/working/** (task dirs are the
  #     work product — pruning them would delete plans and reports), and
  #     ${WS_ROOT}/sessions, which the mirror above already handled. -type f keeps
  #     the globs from ever matching a directory; || true keeps a SessionEnd hook
  #     from failing on a read-only or absent path.
  find "${WS_ROOT}/metrics" -name 'session-*.jsonl' -type f -mtime +30 -delete 2>/dev/null || true
  find "${WS_ROOT}/logs" -name 'trace-*.jsonl' -type f -mtime +30 -delete 2>/dev/null || true

  # 2) Link session → tasks: any working/YYYY/MM/DD/{slug}/ path touched this session
  #    gets a row appended to that task's logs/sessions.md. The canonical task-id is the
  #    yyyy-mm-dd-slug reconstructed from the YYYY/MM/DD/slug path; a legacy date-prefixed
  #    leaf (working/YYYY/MM/yyyy-mm-dd-slug) is still recognised for backward compatibility.
  while IFS= read -r task_id; do
    [ -z "$task_id" ] && continue
    slug="${task_id:11}"
    task_dir="${WS_ROOT}/working/${task_id:0:4}/${task_id:5:2}/${task_id:8:2}/${slug}"
    [ -d "$task_dir" ] || task_dir="$(find "${WS_ROOT}/working" -type d \( -path "*/${slug}" -o -name "$task_id" \) 2>/dev/null | head -1)"
    [ -d "$task_dir" ] || continue
    task_log="${task_dir}/logs/sessions.md"
    mkdir -p "${task_dir}/logs" 2>/dev/null || true
    # -s, not -f: same reason as task-session-log.sh — a file that exists but
    # is empty still needs its header.
    [ -s "$task_log" ] || printf '| Дата | Сесія | Тулзи | Активність |\n|---|---|---|---|\n' > "$task_log"
    # Column 2 must hold the session UUID, not $$. internal/wsingest only
    # trusts values matching a uuid or a >=8-hex prefix — its own comment
    # records that 20/21 legacy cells held junk 5-digit ids, which is exactly
    # what a pid looks like. Writing $$ here fills the column with rows the
    # ingester is guaranteed to discard. Fall back to $$ only when no uuid was
    # observed, so the row still reads for a human.
    sess_uuid=$(jq -r '.session_id // empty' "$SESSION_FILE" 2>/dev/null \
      | grep -m1 -E '^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$' || true)
    sess_cell="${sess_uuid:-$$}"
    grep -q "| ${sess_cell} |" "$task_log" 2>/dev/null && continue   # one row per session
    printf '| %s | %s | %s | edits:%s bash:%s reads:%s writes:%s agents:%s |\n' \
      "$human_date" "$sess_cell" "${total:-?}" "${edit_n:-0}" "${bash_n:-0}" "${read_n:-0}" "${write_n:-0}" "${agent_n:-0}" \
      >> "$task_log" 2>/dev/null || true
  done < <(jq -r 'select(.file != "") | .file' "$SESSION_FILE" 2>/dev/null \
            | grep -oE '/working/[0-9]{4}/[0-9]{2}/[0-9]{2}/[a-z0-9][a-z0-9-]*/|/working/([0-9]{4}/[0-9]{2}/)?[0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9-]+/' \
            | sed -E -e 's|.*/working/([0-9]{4})/([0-9]{2})/([0-9]{2})/([a-z0-9-]+)/$|\1-\2-\3-\4|' \
                     -e 's|.*/([0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9-]+)/$|\1|' | sort -u)

  # 3) Regenerate the workspace INDEX.md from README cards.
  # Prefer the plugin CLI; fall back to the legacy consumer path.
  AW="${CLAUDE_PLUGIN_ROOT:-}/bin/agent-work.sh"
  [ -f "$AW" ] || AW="${WS_ROOT%/.claude-workspace}/.claude/scripts/agent-work.sh"
  [ -f "$AW" ] && bash "$AW" index >/dev/null 2>&1 || true
fi

exit 0
