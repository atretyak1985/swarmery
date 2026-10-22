#!/bin/bash
# Behavioral tests: hooks that read .claude/project.json through node must take
# the path as data. A project directory whose name carries a single quote is a
# legal path, and the hooks must read its project.json exactly as they read any
# other — not drop it, and not let the name become part of the JS they run.
#
# Covered: session-start.sh (displayName + repos) and memory-drift-check.sh
# (known vocabulary). Everything lives under one mktemp dir; the session log is
# sandboxed through SWARMERY_RUN_DIR.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
START="$ROOT/plugins/core/hooks/session-start.sh"
DRIFT="$ROOT/plugins/core/hooks/memory-drift-check.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

if ! command -v node >/dev/null 2>&1; then
  echo "session-start-quote: node not on PATH — skipped"
  exit 0
fi

export SWARMERY_RUN_DIR="$TESTDIR/run"
unset AGENT_PROJECT

PROJ="$TESTDIR/it's a project"
mkdir -p "$PROJ/.claude/agent-memory" "$PROJ/apps/web"
cat > "$PROJ/.claude/project.json" <<'JSON'
{"displayName":"QuoteProbe","repos":["apps/web"],"apps":["apps/web"],"mainApp":"web"}
JSON
git -C "$PROJ/apps/web" init -q
git -C "$PROJ/apps/web" checkout -q -b feature-probe 2>/dev/null || true

# ── session-start: displayName and repos come through ─────────────
banner=$(printf '{}' | CLAUDE_PROJECT_DIR="$PROJ" "$START" 2>/dev/null | perl -pe 's/\e\[[0-9;]*m//g')
case "$banner" in
  *"QuoteProbe Agent System"*) ok ;;
  *) bad "session-start did not read displayName from a quoted project path" ;;
esac
case "$banner" in
  *"apps/web → feature-probe"*) ok ;;
  *) bad "session-start did not read repos from a quoted project path" ;;
esac

# ── memory-drift-check: the vocabulary is read, so drift is judged ─
printf 'see apps/web and apps/gone-app\n' > "$PROJ/.claude/agent-memory/notes.md"
drift=$(CLAUDE_PROJECT_DIR="$PROJ" "$DRIFT" 2>/dev/null)
case "$drift" in
  *"apps/gone-app"*) ok ;;
  *) bad "memory-drift-check did not read project.json from a quoted project path" ;;
esac
case "$drift" in
  *"references apps/web "*) bad "memory-drift-check flagged a known app as drift" ;;
  *) ok ;;
esac

printf 'session-start-quote: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
