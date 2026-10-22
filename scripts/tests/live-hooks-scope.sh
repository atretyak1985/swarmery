#!/bin/bash
# live-hooks-scope.sh — live check: in a real headless Claude Code session the
# core plugin's SessionEnd summary does not run git in a folder outside the
# project, and still writes its summary.
#
# NOT a *.test.sh suite: it needs an authenticated `claude` CLI and spends API
# tokens, so CI and run-suites.sh never pick it up. Run it by hand:
#   bash scripts/tests/live-hooks-scope.sh
#
# What it does:
#   1. makes a scratch project (a git repo) and a sibling "foreign" git repo
#      whose core.fsmonitor is a harmless sentinel: `touch <scratch>/FSMON`;
#   2. runs `claude -p "read <foreign>/README.md and then reply ok"` in the
#      project with ONLY this checkout's core plugin loaded (--plugin-dir,
#      --setting-sources project: no user plugins, no user hooks), the foreign
#      dir allowed for reading (--add-dir), every hook side channel pointed at
#      the scratch dir, and SWARMERY_PORT at a closed port so no hook can reach
#      a running dashboard;
#   3. asserts the session log recorded the foreign read (so the check is not
#      vacuous), FSMON is absent, and the session summary JSON was written.
# It never edits operator settings and never talks to a live daemon.
# LIVE_PLUGIN_DIR=<dir> points --plugin-dir at another copy of the core plugin
# (e.g. an older revision, to watch the check fail before the fix).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLAUDE_BIN="${CLAUDE_BIN:-$(command -v claude || true)}"
if [ -z "$CLAUDE_BIN" ]; then
  echo "live-hooks-scope: claude CLI not found" >&2
  exit 2
fi

T=$(mktemp -d)
T=$(cd -P "$T" && pwd -P)
trap 'rm -rf "$T"' EXIT
PROJ="$T/project"
FOREIGN="$T/foreign"
today=$(date +%Y%m%d)
LOG="$T/session/claude-session-${today}.jsonl"
mkdir -p "$PROJ" "$FOREIGN" "$T/session" "$T/run" "$T/wsroot"

mkrepo() {
  git -C "$1" init -q
  printf 'hello\n' > "$1/README.md"
  git -C "$1" add README.md
  git -C "$1" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false commit -qm init
  printf 'changed\n' >> "$1/README.md"
}
mkrepo "$PROJ"
mkrepo "$FOREIGN"
git -C "$FOREIGN" config core.fsmonitor "touch $T/FSMON"

# The whole session is bounded with perl's alarm (on stock macOS; timeout is not).
out=$(cd "$PROJ" && env \
  SWARMERY_SESSION_FILE="$LOG" CLAUDE_SESSION_TMP="$T/session" SWARMERY_RUN_DIR="$T/run" \
  AGENT_PROJECT="live-scope" AGENT_WORKSPACE_ROOT="$T/wsroot" SWARMERY_PORT=1 \
  perl -e 'alarm shift; exec @ARGV or exit 127' "${LIVE_TIMEOUT:-300}" "$CLAUDE_BIN" -p "Read the file $FOREIGN/README.md and then reply with just: ok" \
    --plugin-dir "${LIVE_PLUGIN_DIR:-$ROOT/plugins/core}" --setting-sources project --strict-mcp-config \
    --add-dir "$FOREIGN" --permission-mode acceptEdits --no-session-persistence 2>&1)
rc=$?
printf 'claude exit %s; reply: %s\n' "$rc" "$(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"

pass=0
fail=0
ok() { pass=$((pass + 1)); printf '  ✓ %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

if [ "$rc" -eq 0 ]; then ok "claude -p exited 0"; else bad "claude -p exited $rc"; fi
if [ -f "$LOG" ] && grep -qF "$FOREIGN/README.md" "$LOG"; then
  ok "the session log recorded the read of the foreign file"
else
  bad "the session log has no read of the foreign file (check is vacuous): $(cat "$LOG" 2>/dev/null | head -5)"
fi
if [ -e "$T/FSMON" ]; then
  bad "git ran the foreign repo's fsmonitor (FSMON exists)"
else
  ok "FSMON absent: no git ran with the foreign repo's fsmonitor"
fi
summary=$(find "$PROJ/.swarmery/sessions" -name '*.json' -type f 2>/dev/null | head -1)
if [ -n "$summary" ] && jq -e . "$summary" >/dev/null 2>&1; then
  ok "session summary written: ${summary#"$T"/}"
  printf '    repos in summary: %s\n' "$(jq -c '[.repos[].path]' "$summary" 2>/dev/null)"
else
  bad "no session summary JSON under $PROJ/.swarmery/sessions"
fi

printf 'live-hooks-scope: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
