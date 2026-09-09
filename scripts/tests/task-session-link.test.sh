#!/usr/bin/env bash
# task-session-link.test.sh — the explicit task<->session link in logs/sessions.md.
#
# internal/wsingest trusts only uuid-shaped (or >=8-hex) values in column 2;
# its own comment records that 20/21 legacy cells held junk 5-digit ids — which
# is what a pid looks like. Two hooks write this one file, so both the selection
# rule and the column contract are tested here.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/task-session-log.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()  { echo "ok   $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1: $2"; fail=$((fail + 1)); }

UUID="3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
WS="$TMP/ws/proj/workspace/working"

mk_task() { # mk_task <slug> <status>
  local d="$WS/2026/09/02/$1"
  mkdir -p "$d/logs"
  printf '# Task: %s\n\n**Status:** %s\n**Goal:** fixture\n' "$1" "$2" > "$d/README.md"
  echo "$d"
}
run() { printf '{"session_id":"%s"}' "$UUID" | AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj "$HOOK" >/dev/null 2>&1 || true; }

# The hook has two locking branches: flock(1) where it exists (Linux) and an
# atomic-mkdir spin lock where it does not (macOS). They are NOT equivalent --
# the flock branch opens the log with `exec 9>>`, which CREATES the file before
# the header check runs. A header guard of `[ -f ]` therefore silently skips the
# header on Linux only, which is exactly how this shipped and was caught by CI
# rather than locally. Both branches are forced here so the host cannot hide it.
FLOCK_STUB="$TMP/bin-flock"
mkdir -p "$FLOCK_STUB"
printf '#!/bin/sh\nexit 0\n' > "$FLOCK_STUB/flock"
chmod +x "$FLOCK_STUB/flock"
run_with_flock() {
  printf '{"session_id":"%s"}' "$1" \
    | PATH="$FLOCK_STUB:$PATH" AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj \
      "$HOOK" >/dev/null 2>&1 || true
}


# ── 1. one active task, AGENT_TASK_ID unset → the link IS written ──────
# This is the whole point: nothing in the fleet exports AGENT_TASK_ID, so
# before the fix this hook wrote nothing, ever.
A=$(mk_task alpha active)
run
if grep -q "$UUID" "$A/logs/sessions.md" 2>/dev/null; then
  ok "writes the link for the single active task without AGENT_TASK_ID"
else
  bad "active-task fallback" "no row written — the hook is still dead code"
fi

# ── 2. the header matches session-summary.sh, the other writer ─────────
if head -1 "$A/logs/sessions.md" | grep -q '| Дата | Сесія | Тулзи | Активність |'; then
  ok "header agrees with the SessionEnd writer"
else
  bad "header" "got: $(head -1 "$A/logs/sessions.md")"
fi

# ── 3. column 2 is uuid-shaped, i.e. what wsingest actually trusts ─────
cell=$(grep "$UUID" "$A/logs/sessions.md" | awk -F'|' '{gsub(/ /,"",$3); print $3}')
if printf '%s' "$cell" | grep -qE '^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$'; then
  ok "session column holds a uuid, not a pid"
else
  bad "uuid column" "got '$cell' — wsingest discards anything under 8 hex"
fi

# ── 4. re-fire (resume/clear) does not duplicate the row ──────────────
run
if [ "$(grep -c "$UUID" "$A/logs/sessions.md")" -eq 1 ]; then
  ok "SessionStart re-fire writes one row per session"
else
  bad "dedupe" "row written $(grep -c "$UUID" "$A/logs/sessions.md") times"
fi

# ── 5. two active tasks → writes nothing rather than guessing ─────────
B=$(mk_task bravo active)
UUID2="9e8d7c6b-5a4f-4321-9876-fedcba098765"
printf '{"session_id":"%s"}' "$UUID2" | AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj "$HOOK" >/dev/null 2>&1 || true
if ! grep -rq "$UUID2" "$A/logs" "$B/logs" 2>/dev/null; then
  ok "ambiguous active set: writes nothing rather than the wrong card"
else
  bad "ambiguity" "guessed a card when two tasks were active"
fi

# ── 6. no active task → nothing written, exit 0 ───────────────────────
rm -rf "$TMP/ws2"; WS2="$TMP/ws2/proj/workspace/working/2026/09/02/idle"
mkdir -p "$WS2/logs"; printf '# Task: idle\n\n**Status:** done\n' > "$WS2/README.md"
rc=0
printf '{"session_id":"%s"}' "$UUID" | AGENT_WORKSPACE_ROOT="$TMP/ws2" AGENT_PROJECT=proj "$HOOK" >/dev/null 2>&1 || rc=$?
if [ "$rc" -eq 0 ] && [ ! -f "$WS2/logs/sessions.md" ]; then
  ok "no active task: writes nothing, exits 0"
else
  bad "idle" "rc=$rc or a file appeared"
fi

# ── 7. malformed stdin never fails session start ──────────────────────
rc=0
printf 'not json' | AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj "$HOOK" >/dev/null 2>&1 || rc=$?
[ "$rc" -eq 0 ] && ok "malformed stdin exits 0" || bad "malformed stdin" "rc=$rc"

# ── 8. the header survives the flock branch, on every host ────────────
# Case 2 already covered whichever branch this host takes natively (mkdir on
# macOS, flock on Linux). This forces the flock branch everywhere, because that
# is the one that regressed: `exec 9>>` pre-creates the log, so an `[ -f ]`
# header guard skips the header for good. macOS could never have shown it.
rm -rf "$TMP/ws/proj/workspace/working/2026/09/02/bravo"          # single active again
rm -rf "$TMP/ws/proj/workspace/working/2026/09/02/alpha/logs"
mkdir -p "$TMP/ws/proj/workspace/working/2026/09/02/alpha/logs"
run_with_flock "1a2b3c4d-5e6f-4071-8293-a4b5c6d7e8f9"
L="$TMP/ws/proj/workspace/working/2026/09/02/alpha/logs/sessions.md"
if head -1 "$L" 2>/dev/null | grep -q '| Дата | Сесія | Тулзи | Активність |'; then
  ok "header written on the flock branch (forced on every host)"
else
  bad "header on flock branch" "got: $(head -1 "$L" 2>/dev/null || echo '<no file>')"
fi

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
