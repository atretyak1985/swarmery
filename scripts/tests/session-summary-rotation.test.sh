#!/bin/bash
# Behavioral tests for the log rotation in plugins/core/hooks/session-summary.sh.
#
# The hook used to rotate exactly one of the append-only logs it feeds (the
# workspace sessions mirror, 30d). metrics/*.jsonl, logs/trace-*.jsonl and the
# per-day statusline substrate in /tmp grew forever. These tests pin the three
# new windows AND the two things rotation must never touch: a working/ task dir
# (the actual work product) and any file inside the retention window.
#
# Same framework-free style as the other hook suites. Every path the hook could
# delete from is redirected into a sandbox — CLAUDE_SESSION_TMP for /tmp and
# AGENT_WORKSPACE_ROOT for the workspace — so running this suite can never
# remove a real session log.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/session-summary.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

# aged <file> <days-ago> — create a file whose mtime is N days in the past.
# `touch -t` needs an absolute stamp, so derive one per platform (BSD date on
# macOS, GNU date elsewhere) rather than assuming either.
aged() {
  local file="$1" days="$2" stamp
  mkdir -p "$(dirname "$file")"
  : > "$file"
  if stamp=$(date -v-"${days}"d +%Y%m%d%H%M 2>/dev/null); then
    :
  else
    stamp=$(date -d "${days} days ago" +%Y%m%d%H%M)
  fi
  touch -t "$stamp" "$file"
}

exists() { [ -f "$1" ]; }

# ── Fixture: a workspace + a sandboxed /tmp, each with old and fresh files ──
AGENT_PROJECT="rotation-suite"
WS_ROOT="$TESTDIR/workspace-root/$AGENT_PROJECT/workspace"
TMP_SANDBOX="$TESTDIR/tmp"
mkdir -p "$TMP_SANDBOX"

# The hook only reaches its workspace block when it finds today's session file.
today=$(date +%Y%m%d)
printf '{"tool":"Bash","cmd":"ls","file":""}\n' > "$TMP_SANDBOX/claude-session-${today}.jsonl"

# Past the window (must go).
aged "$WS_ROOT/metrics/session-old-1.jsonl"        40
aged "$WS_ROOT/logs/trace-20260101.jsonl"          40
aged "$TMP_SANDBOX/claude-session-20260101.jsonl"  40
aged "$WS_ROOT/sessions/old.json"                  40
# Inside the window (must stay).
aged "$WS_ROOT/metrics/session-new-1.jsonl"         3
aged "$WS_ROOT/logs/trace-20260901.jsonl"           3
aged "$TMP_SANDBOX/claude-session-20260902.jsonl"   3
aged "$WS_ROOT/sessions/new.json"                   3
# The statusline substrate has a 7-day window, not 30: a 10-day-old file goes
# even though the same age survives in the workspace dirs.
aged "$TMP_SANDBOX/claude-session-20260908.jsonl"  10
aged "$WS_ROOT/metrics/session-mid.jsonl"          10
# The metrics sweep is 'session-*.jsonl', not '*.jsonl', and that narrowing is
# load-bearing: these two are SINGLE append-only audit logs, so an mtime sweep
# would delete the whole guard/bypass history 30 days after its last write.
aged "$WS_ROOT/metrics/bash-shape-guard.jsonl"     40
aged "$WS_ROOT/metrics/gate-bypasses.jsonl"        40
# Work product — must survive regardless of age.
aged "$WS_ROOT/working/2026/01/01/old-task/plan/phase-1-x.md"     40
aged "$WS_ROOT/working/2026/01/01/old-task/reports/report.md"     40
aged "$WS_ROOT/working/2026/01/01/old-task/metrics/session-x.jsonl" 40

# Run the hook with every deletable path redirected into the sandbox.
(
  cd "$TESTDIR" || exit 1
  AGENT_PROJECT="$AGENT_PROJECT" \
  AGENT_WORKSPACE_ROOT="$TESTDIR/workspace-root" \
  CLAUDE_SESSION_TMP="$TMP_SANDBOX" \
  CLAUDE_PROJECT_DIR="$TESTDIR/project" \
  "$HOOK"
) >/dev/null 2>&1
hook_status=$?

# The hook is a SessionEnd surface: it must never fail the session.
if [ "$hook_status" -eq 0 ]; then ok; else bad "hook exited $hook_status, want 0"; fi

# ── Expired files are gone ────────────────────────────────────────
for f in \
  "$WS_ROOT/metrics/session-old-1.jsonl" \
  "$WS_ROOT/logs/trace-20260101.jsonl" \
  "$TMP_SANDBOX/claude-session-20260101.jsonl" \
  "$WS_ROOT/sessions/old.json"
do
  if exists "$f"; then bad "expired file survived rotation: ${f#"$TESTDIR"/}"; else ok; fi
done

# The 7-day window is tighter than the 30-day one.
if exists "$TMP_SANDBOX/claude-session-20260908.jsonl"; then
  bad "a 10-day-old statusline file survived the 7-day window"
else
  ok
fi
if exists "$WS_ROOT/metrics/session-mid.jsonl"; then
  ok
else
  bad "a 10-day-old metrics file was deleted; the workspace window is 30 days"
fi

# ── Fresh files are kept ──────────────────────────────────────────
for f in \
  "$WS_ROOT/metrics/session-new-1.jsonl" \
  "$WS_ROOT/logs/trace-20260901.jsonl" \
  "$TMP_SANDBOX/claude-session-20260902.jsonl" \
  "$WS_ROOT/sessions/new.json"
do
  if exists "$f"; then ok; else bad "in-window file was deleted: ${f#"$TESTDIR"/}"; fi
done

# Today's own session file must survive — the statusline still reads it.
if exists "$TMP_SANDBOX/claude-session-${today}.jsonl"; then
  ok
else
  bad "the hook deleted today's own session file"
fi

# ── working/ is never swept, at any age ───────────────────────────
for f in \
  "$WS_ROOT/working/2026/01/01/old-task/plan/phase-1-x.md" \
  "$WS_ROOT/working/2026/01/01/old-task/reports/report.md" \
  "$WS_ROOT/working/2026/01/01/old-task/metrics/session-x.jsonl"
do
  if exists "$f"; then ok; else bad "rotation deleted a working/ task file: ${f#"$TESTDIR"/}"; fi
done

# The task dir itself must still be there.
if [ -d "$WS_ROOT/working/2026/01/01/old-task" ]; then ok; else bad "a working/ task dir was removed"; fi

# ── The sweep must be scoped, not recursive ───────────────────────
# A 40-day-old metrics file nested under working/ proves the metrics sweep is
# not a find over the whole workspace (it is asserted alive above); this checks
# the converse — that the top-level sweep did run at all, so the assertion above
# cannot pass merely because nothing was swept anywhere.
if exists "$WS_ROOT/metrics/session-old-1.jsonl"; then
  bad "top-level metrics sweep did not run, so the working/ assertion is vacuous"
else
  ok
fi

# ── The metrics sweep is narrowed to session-*.jsonl on purpose ────
# bash-shape-guard.jsonl and gate-bypasses.jsonl are single append-only audit
# files: one mtime sweep over metrics/*.jsonl would take the entire history.
# They are 40 days old and sit beside a session-*.jsonl of the same age that
# the assertions above prove was deleted, so this is not vacuous.
for f in \
  "$WS_ROOT/metrics/bash-shape-guard.jsonl" \
  "$WS_ROOT/metrics/gate-bypasses.jsonl"
do
  if exists "$f"; then ok; else bad "rotation deleted an append-only audit log: ${f#"$TESTDIR"/}"; fi
done

# ── The /tmp sweep must not depend on the workspace resolving ──────
# WS_ROOT is empty when AGENT_PROJECT is unset and no ancestor carries both
# .claude-workspace and .claude. The sweep used to live inside the workspace
# block, so in that configuration /tmp grew forever — the exact unbounded growth
# this rotation exists to stop. Run the hook again with the workspace knobs
# cleared and a fresh /tmp sandbox.
TMP_NOWS="$TESTDIR/tmp-nows"
mkdir -p "$TMP_NOWS"
printf '{"tool":"Bash","cmd":"ls","file":""}\n' > "$TMP_NOWS/claude-session-${today}.jsonl"
aged "$TMP_NOWS/claude-session-20260101.jsonl" 40
aged "$TMP_NOWS/claude-session-20260902.jsonl"  3

# NOWS_HOME keeps the hook's $HOME fallback (AGENT_WORKSPACE_ROOT unset →
# $HOME/swarmery-workspace) pointed at an empty dir, so a real workspace on the
# developer's machine cannot accidentally resolve WS_ROOT here.
NOWS_HOME="$TESTDIR/nows-home"
mkdir -p "$NOWS_HOME" "$TESTDIR/nows-project"
(
  cd "$TESTDIR/nows-project" || exit 1
  env -u AGENT_PROJECT -u AGENT_WORKSPACE_ROOT \
    HOME="$NOWS_HOME" \
    CLAUDE_SESSION_TMP="$TMP_NOWS" \
    CLAUDE_PROJECT_DIR="$TESTDIR/nows-project" \
    "$HOOK"
) >/dev/null 2>&1
nows_status=$?

if [ "$nows_status" -eq 0 ]; then ok; else bad "hook exited $nows_status without a workspace, want 0"; fi
if exists "$TMP_NOWS/claude-session-20260101.jsonl"; then
  bad "the /tmp sweep is still gated on the workspace resolving: a 40-day-old file survived"
else
  ok
fi
if exists "$TMP_NOWS/claude-session-20260902.jsonl"; then ok; else bad "the no-workspace /tmp sweep deleted an in-window file"; fi
if exists "$TMP_NOWS/claude-session-${today}.jsonl"; then ok; else bad "the no-workspace /tmp sweep deleted today's own session file"; fi

# ── No substrate file → the hook exits 0 and sweeps NOTHING ────────
# Both invocations above create today's substrate file first, because the hook
# needs one to do anything. That makes them blind to the hook's real
# precondition: the SESSION_FILE check exits 0 when today's file is missing or
# empty, and that exit is UPSTREAM of every sweep. So the rotation is reached,
# not guaranteed — a session that recorded no tool calls rotates nothing at all.
#
# This is documented behaviour, not a bug (no substrate file today means nothing
# was added to /tmp today either, so the backlog cannot grow while the sweep is
# skipped), but it was asserted nowhere. Pin it, so the comments in the hook
# that now say "outside the workspace block" rather than "unconditionally"
# cannot quietly drift back into a claim the code does not make.
TMP_NOFILE="$TESTDIR/tmp-nofile"
mkdir -p "$TMP_NOFILE"
# Deliberately NO claude-session-${today}.jsonl here. Only expired files, which
# a sweep that DID run would delete.
aged "$TMP_NOFILE/claude-session-20260101.jsonl" 40
NOFILE_WS="$TESTDIR/workspace-root/$AGENT_PROJECT/workspace"
aged "$NOFILE_WS/metrics/session-nofile.jsonl" 40
aged "$NOFILE_WS/logs/trace-nofile.jsonl"      40

(
  cd "$TESTDIR" || exit 1
  AGENT_PROJECT="$AGENT_PROJECT" \
  AGENT_WORKSPACE_ROOT="$TESTDIR/workspace-root" \
  CLAUDE_SESSION_TMP="$TMP_NOFILE" \
  CLAUDE_PROJECT_DIR="$TESTDIR/project" \
  "$HOOK"
) >/dev/null 2>&1
nofile_status=$?

# It is a SessionEnd surface: no substrate file is a normal session, not a failure.
if [ "$nofile_status" -eq 0 ]; then ok; else bad "hook exited $nofile_status with no substrate file, want 0"; fi

# Nothing was swept — in /tmp or in the workspace. These files are 40 days old
# and identical in shape to ones the assertions above prove get deleted when the
# hook runs to completion, so this is not vacuous.
for f in \
  "$TMP_NOFILE/claude-session-20260101.jsonl" \
  "$NOFILE_WS/metrics/session-nofile.jsonl" \
  "$NOFILE_WS/logs/trace-nofile.jsonl"
do
  if exists "$f"; then
    ok
  else
    bad "a sweep ran without today's substrate file; the early exit is upstream of every sweep: ${f#"$TESTDIR"/}"
  fi
done

# An EMPTY substrate file is the same case: the check is [ -s ], not [ -f ].
TMP_EMPTY="$TESTDIR/tmp-empty"
mkdir -p "$TMP_EMPTY"
: > "$TMP_EMPTY/claude-session-${today}.jsonl"
aged "$TMP_EMPTY/claude-session-20260101.jsonl" 40
(
  cd "$TESTDIR" || exit 1
  AGENT_PROJECT="$AGENT_PROJECT" \
  AGENT_WORKSPACE_ROOT="$TESTDIR/workspace-root" \
  CLAUDE_SESSION_TMP="$TMP_EMPTY" \
  CLAUDE_PROJECT_DIR="$TESTDIR/project" \
  "$HOOK"
) >/dev/null 2>&1
empty_status=$?
if [ "$empty_status" -eq 0 ]; then ok; else bad "hook exited $empty_status on an empty substrate file, want 0"; fi
if exists "$TMP_EMPTY/claude-session-20260101.jsonl"; then
  ok
else
  bad "an empty substrate file was treated as present; the early exit tests -s, not -f"
fi

printf 'session-summary-rotation: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
