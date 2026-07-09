#!/bin/bash
# Agent Work CLI
# Version: 5.2 (workspace standard: working/YYYY/MM/DD/<slug> task dirs grouped by
#               start year/month/day (mirrors archive/), README card, SUMMARY.md,
#               filesystem-generated INDEX.md — no manifest.json/index.json.
#               Canonical task-id stays <yyyy-mm-dd-slug>; the date is the YYYY/MM/DD
#               path prefix and the leaf folder is just the slug.)
# Location: .claude/scripts/agent-work.sh
# Standard: .claude/docs/03-usage-guides/AGENT-WORK-DOCUMENTATION.md
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Workspace resolution ──────────────────────────────────────────
# agentry model (preferred): a sibling agent-workspace/ namespaced by project.
#   AGENT_WORKSPACE_ROOT — path to the workspace repo (default: /Volumes/Work/agent-workspace)
#   AGENT_PROJECT        — project slug (set per-project in .claude/settings.json env)
# → WORKSPACE_DIR = $AGENT_WORKSPACE_ROOT/$AGENT_PROJECT/workspace
# Legacy fallback: walk up for a project-local .claude-workspace (pre-agentry layout).
if [ -n "${AGENT_PROJECT:-}" ]; then
    _ws_root="${AGENT_WORKSPACE_ROOT:-/Volumes/Work/agent-workspace}"
    PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
    WORKSPACE_DIR="${_ws_root}/${AGENT_PROJECT}/workspace"
else
    _root="$SCRIPT_DIR"
    while [ "$_root" != "/" ]; do
        if [ -d "${_root}/.claude-workspace" ] && [ -e "${_root}/.claude" ]; then break; fi
        _root="$(dirname "$_root")"
    done
    if [ "$_root" != "/" ]; then
        PROJECT_ROOT="$_root"
    else
        PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
    fi
    WORKSPACE_DIR="${PROJECT_ROOT}/.claude-workspace"
fi
WORKING_DIR="${WORKSPACE_DIR}/working"
ARCHIVE_DIR="${WORKSPACE_DIR}/archive"
LOGS_DIR="${WORKSPACE_DIR}/logs"
METRICS_DIR="${WORKSPACE_DIR}/metrics"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
# Logs go to stderr so command substitution ($(_resolve_task_dir …)) stays clean.
log_info() { echo -e "${BLUE}ℹ${NC} $1" >&2; }
log_success() { echo -e "${GREEN}✓${NC} $1" >&2; }
log_error() { echo -e "${RED}✗${NC} $1" >&2; }

trace_log() {
    local log_file
    log_file="${LOGS_DIR}/trace-$(date +%Y%m%d).jsonl"
    mkdir -p "${LOGS_DIR}" 2>/dev/null || true
    printf '{"ts":"%s","event":"%s","msg":"%s"}\n' \
        "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" "$1" "$2" >> "$log_file" 2>/dev/null || true
}

slugify() {
    echo "$1" | tr '[:upper:]' '[:lower:]' | sed -e 's/[^a-z0-9]/-/g' -e 's/--*/-/g' -e 's/^-//' -e 's/-$//' | cut -c1-50
}

# Extract a "- **Field**: value" line from a README card
_readme_field() {
    local readme="$1" field="$2"
    [ -f "$readme" ] || { echo "?"; return; }
    grep -m1 "\*\*${field}\*\*" "$readme" 2>/dev/null | sed -e 's/^.*\*\*'"${field}"'\*\*:[[:space:]]*//' -e 's/[[:space:]]*·.*$//' || echo "?"
}

# Tasks live under working/YYYY/MM/DD/<slug> (mirrors archive/). The canonical task-id
# stays <yyyy-mm-dd-slug>: the YYYY/MM/DD are derived from its embedded start date and
# the leaf folder is just the slug, so no lookup is needed for the common path. A find()
# fallback (by slug leaf) covers ids that predate this layout.
_task_dir_for() {
    local task_id="$1"
    local y="${task_id:0:4}" m="${task_id:5:2}" d="${task_id:8:2}" slug="${task_id:11}"
    local dir="${WORKING_DIR}/${y}/${m}/${d}/${slug}"
    if [ -d "$dir" ]; then echo "$dir"; return; fi
    find "${WORKING_DIR}" -type d \( -path "*/${slug}" -o -name "$task_id" \) 2>/dev/null | head -1
}

# Reconstruct the canonical task-id (yyyy-mm-dd-slug) from a YYYY/MM/DD/slug dir path.
_id_from_dir() {
    local dir="${1%/}"
    local slug; slug=$(basename "$dir")
    local d; d=$(basename "$(dirname "$dir")")
    local m; m=$(basename "$(dirname "$(dirname "$dir")")")
    local y; y=$(basename "$(dirname "$(dirname "$(dirname "$dir")")")")
    echo "${y}-${m}-${d}-${slug}"
}

_latest_task_id() {
    local dir
    dir=$(find "${WORKING_DIR}" -mindepth 4 -maxdepth 4 -type d -exec stat -f '%m %N' {} + 2>/dev/null \
        | sort -rn | head -1 | sed -E 's|^[0-9]+ ||')
    [ -n "$dir" ] && _id_from_dir "$dir"
}

_resolve_task_dir() {
    local task_id="$1"
    [ "$task_id" = "--latest" ] && task_id=$(_latest_task_id)
    [ -z "$task_id" ] && { log_error "Task ID required (or --latest)"; exit 1; }
    local dir; dir=$(_task_dir_for "$task_id")
    [ -n "$dir" ] && [ -d "$dir" ] || { log_error "Not found in working/: ${task_id}"; exit 1; }
    echo "$dir"
}

cmd_setup() {
    mkdir -p "${WORKING_DIR}" "${ARCHIVE_DIR}" "${WORKSPACE_DIR}/sessions" "${LOGS_DIR}" "${METRICS_DIR}"
    trace_log "system.setup" "workspace initialized"
    log_success "Workspace ready: ${WORKSPACE_DIR}"
}

# init <task name> [type]   type: audit|feature|refactor|research|incident|infra
cmd_init() {
    local task_name="$1" task_type="${2:-feature}"
    [ -z "$task_name" ] && { log_error "Task name required"; exit 1; }

    local slug task_id
    slug="$(slugify "$task_name")"
    task_id="$(date +%Y-%m-%d)-${slug}"
    # Group task dirs by start year/month/day — working/YYYY/MM/DD/<slug> — mirroring archive/.
    local task_dir="${WORKING_DIR}/$(date +%Y)/$(date +%m)/$(date +%d)/${slug}"
    [ -d "$task_dir" ] && { log_error "Already exists: ${task_id}"; exit 1; }

    mkdir -p "${task_dir}/plan" "${task_dir}/reports" "${task_dir}/logs"

    cat > "${task_dir}/README.md" <<EOF
# ${task_name}

- **ID**: ${task_id}
- **Статус**: active
- **Тип**: ${task_type}
- **Старт**: $(date +%Y-%m-%d) · **Завершено**: —
- **Репо**:
- **Ціль**:
- **Артефакти**:
EOF

    printf '| Агент | Фаза | Вердикт | Артефакт |\n|---|---|---|---|\n' > "${task_dir}/logs/agents.md"
    printf '| Дата | Сесія | Тривалість | Активність |\n|---|---|---|---|\n' > "${task_dir}/logs/sessions.md"

    cmd_index >/dev/null 2>&1 || true
    trace_log "task.init" "${task_id}"
    log_success "Task created: ${task_id}"
    echo "${task_id}"
}

# phase <id|--latest> <NN-name>  — create a phase artifact from template when available
cmd_phase() {
    local task_dir; task_dir=$(_resolve_task_dir "$1")
    local phase="$2"
    [ -z "$phase" ] && { log_error "Phase name required (e.g. 01-understanding)"; exit 1; }
    mkdir -p "${task_dir}/phases"
    local tmpl="${PROJECT_ROOT}/.claude/templates/working/phases/${phase}.template.md"
    local target="${task_dir}/phases/${phase}.md"
    if [ -f "$tmpl" ] && [ ! -f "$target" ]; then
        cp "$tmpl" "$target"
    else
        touch "$target"
    fi
    log_success "Phase file: ${target}"
}

# complete <id|--latest> — README → done, SUMMARY skeleton, move to archive/YYYY/MM/
cmd_complete() {
    local task_dir; task_dir=$(_resolve_task_dir "$1")
    local task_id; task_id=$(_id_from_dir "$task_dir")
    local today; today=$(date +%Y-%m-%d)

    # README: status + completion date (gsed-free, BSD-sed compatible)
    sed -i '' -e "s/\*\*Статус\*\*: active/\*\*Статус\*\*: done/" \
              -e "s/\*\*Завершено\*\*: —/\*\*Завершено\*\*: ${today}/" \
              "${task_dir}/README.md" 2>/dev/null \
    || sed -i -e "s/\*\*Статус\*\*: active/\*\*Статус\*\*: done/" \
              -e "s/\*\*Завершено\*\*: —/\*\*Завершено\*\*: ${today}/" \
              "${task_dir}/README.md"

    if [ ! -f "${task_dir}/SUMMARY.md" ]; then
        cat > "${task_dir}/SUMMARY.md" <<EOF
# Summary — ${task_id}

- **Результат**: <!-- 1 абзац: що зроблено, чи досягнута ціль -->
- **Змінені файли**: <!-- список або git diff --stat -->
- **Агенти**: <!-- з logs/agents.md, або «пряма сесія, без делегування» -->
- **Сесії**: <!-- з logs/sessions.md -->
- **Відхилення від плану**:
- **Follow-ups**:
EOF
        log_info "SUMMARY.md skeleton created — заповни перед фінальним звітом"
    fi

    # Archive mirrors working: group by the task's own start date (from its id), not today.
    local dest_dir
    dest_dir="${ARCHIVE_DIR}/${task_id:0:4}/${task_id:5:2}/${task_id:8:2}"
    mkdir -p "$dest_dir"
    mv "$task_dir" "${dest_dir}/"
    # Prune now-empty working/YYYY/MM/DD (and MM, YYYY) parents left behind by the move.
    rmdir "$(dirname "$task_dir")" 2>/dev/null \
      && rmdir "$(dirname "$(dirname "$task_dir")")" 2>/dev/null \
      && rmdir "$(dirname "$(dirname "$(dirname "$task_dir")")")" 2>/dev/null || true
    cmd_index >/dev/null 2>&1 || true
    trace_log "task.completed" "${task_id}"
    log_success "Completed and archived: ${dest_dir}/${task_id:11} (id: ${task_id})"
}

# index — regenerate INDEX.md from filesystem (called by SessionEnd hook too)
cmd_index() {
    local index_file="${WORKSPACE_DIR}/INDEX.md"
    {
        echo "# Workspace Index"
        echo ""
        echo "> Авто-генерується \`agent-work.sh index\` (і SessionEnd-хуком). Не редагувати руками."
        echo "> Оновлено: $(date +"%Y-%m-%d %H:%M")"
        echo ""
        echo "## Активні (working/)"
        echo ""
        echo "| ID | Тип | Статус | Остання активність |"
        echo "|---|---|---|---|"
        for dir in "${WORKING_DIR}"/*/*/*/*/; do
            [ -d "$dir" ] || continue
            local id; id=$(_id_from_dir "$dir")
            local rel; rel=${dir#"${WORKSPACE_DIR}/"}
            local typ; typ=$(_readme_field "${dir}README.md" "Тип")
            local st; st=$(_readme_field "${dir}README.md" "Статус")
            local mtime; mtime=$(stat -f '%Sm' -t '%Y-%m-%d' "$dir" 2>/dev/null || date -r "$(stat -c %Y "$dir" 2>/dev/null || echo 0)" +%Y-%m-%d 2>/dev/null || echo "?")
            echo "| [${id}](${rel}README.md) | ${typ} | ${st} | ${mtime} |"
        done
        echo ""
        echo "## Архів"
        echo ""
        echo "| ID | Тип | Звіт |"
        echo "|---|---|---|"
        for dir in "${ARCHIVE_DIR}"/*/*/*/*/; do
            [ -d "$dir" ] || continue
            local id; id=$(_id_from_dir "$dir")
            local rel; rel=${dir#"${WORKSPACE_DIR}/"}
            local typ; typ=$(_readme_field "${dir}README.md" "Тип")
            local sm="—"
            [ -f "${dir}SUMMARY.md" ] && sm="[SUMMARY](${rel}SUMMARY.md)"
            echo "| [${id}](${rel}README.md) | ${typ} | ${sm} |"
        done
    } > "$index_file"
    log_success "INDEX.md regenerated"
}

cmd_list() {
    local filter="${1:-all}"
    for dir in "${WORKING_DIR}"/*/*/*/*/; do
        [ -d "$dir" ] || continue
        local id; id=$(_id_from_dir "$dir")
        local st; st=$(_readme_field "${dir}README.md" "Статус")
        [ "$filter" = "all" ] || [ "$filter" = "$st" ] || continue
        case "$st" in
            done) echo -e "${GREEN}✓${NC} ${id}";;
            active) echo -e "${YELLOW}◐${NC} ${id}";;
            *) echo -e "${BLUE}?${NC} ${id} (${st})";;
        esac
    done
}

cmd_search() {
    local query="$1"
    [ -z "$query" ] && { log_error "Query required"; exit 1; }
    grep -ril "$query" "${WORKING_DIR}" "${ARCHIVE_DIR}" 2>/dev/null \
        | sed -e "s|${WORKSPACE_DIR}/||" | sort -u | head -30
}

cmd_view() {
    local task_dir; task_dir=$(_resolve_task_dir "$1")
    cat "${task_dir}/README.md" 2>/dev/null || log_error "No README.md"
    echo ""
    echo -e "${CYAN}Files:${NC}"
    find "$task_dir" -type f | sed -e "s|${task_dir}/|  |" | sort
}

cmd_metrics() {
    local active=0 done_n=0 archived=0
    for dir in "${WORKING_DIR}"/*/*/*/*/; do
        [ -d "$dir" ] || continue
        case "$(_readme_field "${dir}README.md" "Статус")" in
            done) done_n=$((done_n+1));; *) active=$((active+1));;
        esac
    done
    archived=$(find "${ARCHIVE_DIR}" -mindepth 4 -maxdepth 4 -type d 2>/dev/null | wc -l | tr -d ' ')
    echo "Working: active=${active} done-not-archived=${done_n} | Archived: ${archived}"
}

cmd_cleanup() {
    local days="${1:-30}"
    find "${METRICS_DIR}" -name '*.jsonl' -type f -mtime +"${days}" -delete 2>/dev/null || true
    find "${WORKSPACE_DIR}/sessions" -name '*.json' -type f -mtime +"${days}" -delete 2>/dev/null || true
    find "${LOGS_DIR}" -name '*.jsonl' -type f -mtime +"${days}" -delete 2>/dev/null || true
    log_success "Cleanup complete (>${days}d metrics/sessions/logs)"
}

cmd_help() {
    echo "Agent Work CLI v5.2 — workspace standard working/YYYY/MM/DD/<slug> (id: yyyy-mm-dd-slug)"
    echo "Usage: agent-work.sh <command> [options]"
    echo ""
    echo "  setup                       Create workspace directories"
    echo "  init <name> [type]          New task dir working/YYYY/MM/DD/<slug> (type: audit|feature|refactor|research|incident|infra)"
    echo "  phase <id|--latest> <NN-x>  Create phases/NN-x.md from template"
    echo "  complete <id|--latest>      README→done + SUMMARY skeleton + move to archive/YYYY/MM/DD/"
    echo "  index                       Regenerate .claude-workspace/INDEX.md"
    echo "  list [active|done]          List working tasks"
    echo "  search <query>              Grep across working/ + archive/"
    echo "  view <id|--latest>          Show task card + file tree"
    echo "  metrics                     Counts"
    echo "  cleanup [days]              Rotate metrics/sessions/trace logs (default 30d)"
}

case "${1:-help}" in
    setup) cmd_setup;; init) cmd_init "$2" "$3";; phase) cmd_phase "$2" "$3";;
    complete) cmd_complete "$2";; index) cmd_index;; list) cmd_list "$2";;
    search) cmd_search "$2";; view) cmd_view "$2";; metrics) cmd_metrics;;
    cleanup) cmd_cleanup "$2";; help|--help|-h) cmd_help;;
    *) log_error "Unknown: $1"; cmd_help; exit 1;;
esac
