#!/bin/bash
# live-core-hooks.sh — live check that the core hooks, loaded from THIS checkout
# with --plugin-dir, keep a real headless session's log in the private run dir.
#
# Unit suites run each hook with crafted stdin; this one crosses the engine
# boundary: Claude Code itself invokes the hooks, with its own env and payloads.
# It costs one small real model run, so it is deliberately NOT a *.test.sh —
# CI and scripts/tests/run-suites.sh skip it. Run it by hand:
#
#   bash scripts/tests/live-core-hooks.sh
#
# Isolation:
#   - a scratch project and a scratch SWARMERY_RUN_DIR under one mktemp dir;
#   - --setting-sources project + --strict-mcp-config: no user/local settings,
#     so no installed plugins or user hooks load — only --plugin-dir core;
#   - --no-session-persistence: no transcript is written for the daemon to index;
#   - SWARMERY_PORT=1: the context bridge cannot reach a running daemon;
#   - workspace env (AGENT_PROJECT, AGENT_WORKSPACE_ROOT) is unset, so no hook
#     writes into a real workspace. No settings file is edited.
#
# The shared /tmp is only READ: older plugin versions in other sessions may be
# appending to /tmp/claude-session-*.jsonl right now, so the check is that no
# line there names this run's scratch project and no new file appeared.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PLUGIN="$ROOT/plugins/core"

pass=0
fail=0
ok() { pass=$((pass + 1)); printf '  ✓ %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

command -v claude >/dev/null 2>&1 || { echo "live-core-hooks: claude is not on PATH"; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "live-core-hooks: jq is not on PATH"; exit 1; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
PROJ="$WORK/proj"
RUN="$WORK/run"
mkdir -p "$PROJ"
PROJ=$(cd "$PROJ" && pwd -P)
git -C "$PROJ" init -q

mode_of() {
  local m
  m=$(stat -c '%a' "$1" 2>/dev/null)
  case "$m" in ''|*[!0-7]*) m=$(stat -f '%Lp' "$1" 2>/dev/null) ;; esac
  printf '%s' "$m"
}

# list_tmp_logs — the shared legacy logs, one per line (glob, not ls).
list_tmp_logs() {
  local f
  for f in /tmp/claude-session-*.jsonl; do [ -f "$f" ] && printf '%s\n' "$f"; done | sort
}
tmp_logs_before=$(list_tmp_logs)

echo "live-core-hooks: claude $(claude --version 2>/dev/null | head -1), plugin $PLUGIN"
echo "live-core-hooks: scratch project $PROJ"
out=$(
  cd "$PROJ" && env \
    -u AGENT_PROJECT -u AGENT_WORKSPACE_ROOT -u SWARMERY_SESSION_FILE -u CLAUDE_SESSION_TMP \
    -u CLAUDE_PROJECT_DIR -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT \
    SWARMERY_RUN_DIR="$RUN" SWARMERY_PORT=1 \
    claude -p "create a file named live.txt containing ok" \
      --plugin-dir "$PLUGIN" \
      --permission-mode acceptEdits \
      --setting-sources project \
      --strict-mcp-config \
      --no-session-persistence \
      --max-budget-usd 1 \
      --model haiku \
      < /dev/null 2>&1
)
rc=$?
echo "live-core-hooks: claude exit $rc"
printf '%s\n' "$out" | tail -5 | sed 's/^/    | /'

if [ "$rc" -eq 0 ]; then ok "claude -p exited 0"; else bad "claude -p exited $rc"; fi
if grep -qx 'ok' "$PROJ/live.txt" 2>/dev/null; then ok "the session wrote live.txt"; else bad "live.txt missing or wrong"; fi

today=$(date +%Y%m%d)
log="$RUN/claude-session-${today}.jsonl"
if [ -f "$log" ]; then ok "session log exists in the scratch run dir ($log)"; else bad "no session log at $log"; fi
if [ "$(mode_of "$RUN")" = "700" ]; then ok "run dir mode 700"; else bad "run dir mode $(mode_of "$RUN")"; fi
if [ "$(mode_of "$log")" = "600" ]; then ok "session log mode 600"; else bad "session log mode $(mode_of "$log")"; fi
if jq -e --arg p "$PROJ/live.txt" 'select(.file == $p)' "$log" >/dev/null 2>&1; then
  ok "post-tool-observe logged the write of live.txt"
else
  bad "no log line for $PROJ/live.txt"
fi
if jq -e 'select(.tool == "_session_start")' "$log" >/dev/null 2>&1; then
  ok "session-start marked the session start in the run-dir log"
else
  bad "no _session_start line in the run-dir log"
fi
echo "live-core-hooks: log lines: $(wc -l < "$log" 2>/dev/null | tr -d ' '); tools: $(jq -r '.tool' "$log" 2>/dev/null | sort | uniq -c | tr -s ' ' | tr '\n' ';')"

tmp_logs_after=$(list_tmp_logs)
new_tmp=$(comm -13 <(printf '%s\n' "$tmp_logs_before") <(printf '%s\n' "$tmp_logs_after") | sed '/^$/d')
if [ -z "$new_tmp" ]; then ok "no new /tmp/claude-session-* file"; else bad "new shared log file(s): $new_tmp"; fi
leaked=""
for f in /tmp/claude-session-*.jsonl; do
  [ -f "$f" ] || continue
  grep -qF "$PROJ" "$f" 2>/dev/null && leaked="$leaked $f"
done
if [ -z "$leaked" ]; then ok "no /tmp/claude-session-* line names the scratch project"; else bad "scratch project logged to:$leaked"; fi

printf 'live-core-hooks: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
