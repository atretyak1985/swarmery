#!/usr/bin/env bash
# task-card-status.test.sh — the task card's status line, written and read alike.
#
# Three SessionStart hooks select the active task by its README card's status
# line. For a long while they grepped for the substring `Status:`, while the
# card agent-work.sh init wrote said `- **Статус**: active` and the daemon's
# taskdir wrote `- **Status**: active` — neither contains that substring (the
# colon follows the closing `**`), so the hooks never found an active task.
# Only the hand-written `**Status:** active` of the other suites' fixtures
# matched, which is exactly why those suites stayed green. This one drives the
# REAL writer against the REAL readers instead:
#
#   agent-work.sh init   → session-start.sh lists the task, task-session-log.sh
#                          links the session to it, session-context-bridge.sh
#                          prefers its NEXT.md over a finished task's newer one
#   pause / resume /
#   complete / restore   → the canonical line is edited, the hooks follow
#   legacy **Статус**    → still read, still edited in place, label kept
#   `**Status**: active` (daemon), `**Status:** active`, `Status: active`
#                        → all read; prose that merely says "Status: active"
#                          is not a status line
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AW="$ROOT/plugins/core/bin/agent-work.sh"
START="$ROOT/plugins/core/hooks/session-start.sh"
LINK="$ROOT/plugins/core/hooks/task-session-log.sh"
BRIDGE="$ROOT/plugins/core/hooks/session-context-bridge.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()  { echo "ok   $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1: $2"; fail=$((fail + 1)); }

REPO="$TMP/repo"; HOMEDIR="$TMP/home"
mkdir -p "$REPO" "$HOMEDIR"
AWLOG="$TMP/agent-work.log"

# A port nothing listens on — the bridge's daemon lookup must fail fast.
DEAD_PORT=$(python3 -c 'import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()' 2>/dev/null || echo 9)

TODAY=$(date +%Y-%m-%d)
TODAY_PATH="$(date +%Y)/$(date +%m)/$(date +%d)"

# aw <workspace-root> <args…> — the CLI against one workspace; stdout only,
# stderr (its coloured log lines) kept aside for failure messages.
aw() {
  local ws="$1"; shift
  AGENT_WORKSPACE_ROOT="$ws" AGENT_PROJECT=proj CLAUDE_PROJECT_DIR="$REPO" \
    bash "$AW" "$@" 2>>"$AWLOG"
}

# banner <workspace-root> — session-start.sh's stdout, run the way the harness
# runs it (by path, so the shebang decides the interpreter).
banner() {
  printf '{"session_id":"t","hook_event_name":"SessionStart","source":"startup"}' \
    | HOME="$HOMEDIR" CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$1" AGENT_PROJECT=proj \
      "$START" 2>/dev/null
}

# link <workspace-root> <uuid> — task-session-log.sh for one session uuid.
link() {
  printf '{"session_id":"%s"}' "$2" \
    | HOME="$HOMEDIR" CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$1" AGENT_PROJECT=proj \
      "$LINK" >/dev/null 2>&1 || true
}

# bridge <workspace-root> — session-context-bridge.sh's stdout with the daemon down.
bridge() {
  printf '{"session_id":"t","hook_event_name":"SessionStart","source":"startup"}' \
    | HOME="$HOMEDIR" CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$1" AGENT_PROJECT=proj \
      SWARMERY_PORT="$DEAD_PORT" "$BRIDGE" 2>/dev/null
}

# card <workspace-root> <date-path> <slug> <body> — a hand-written task card
# (body with \n escapes); echoes the task dir.
card() {
  local d="$1/proj/workspace/working/$2/$3"
  mkdir -p "$d/logs"
  printf '%b' "$4" > "$d/README.md"
  echo "$d"
}

contains() { printf '%s' "$1" | grep -qF -- "$2"; }
status_lines() { grep -iE 'Status|Статус' "$1" 2>/dev/null | tr '\n' ' '; }

UUID1="3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
UUID2="9e8d7c6b-5a4f-4321-9876-fedcba098765"
UUID3="1a2b3c4d-5e6f-4071-8293-a4b5c6d7e8f9"

# ── 1. the real writer ────────────────────────────────────────────────
WS="$TMP/ws"
TASK_ID=$(aw "$WS" init "Card status probe" feature | tail -1)
TASK_DIR="$WS/proj/workspace/working/$TODAY_PATH/card-status-probe"
if [ -f "$TASK_DIR/README.md" ] && [ "$TASK_ID" = "$TODAY-card-status-probe" ]; then
  ok "init created the card ($TASK_ID)"
else
  bad "init" "no card at $TASK_DIR (id printed: '$TASK_ID'); log: $(tail -3 "$AWLOG" 2>/dev/null | tr '\n' ' ')"
fi
if grep -qF -- '- **Status**: active' "$TASK_DIR/README.md" 2>/dev/null; then
  ok "init writes the canonical status line"
else
  bad "canonical marker" "card says: $(status_lines "$TASK_DIR/README.md")"
fi

# ── 2. session-start.sh lists it as in flight ─────────────────────────
out=$(banner "$WS")
if contains "$out" "In-flight tasks:" && contains "$out" "card-status-probe"; then
  ok "session-start.sh lists the init-created task as in flight"
else
  bad "banner" "no in-flight entry for card-status-probe"
fi

# ── 3. task-session-log.sh links the session to it ────────────────────
link "$WS" "$UUID1"
if grep -q "$UUID1" "$TASK_DIR/logs/sessions.md" 2>/dev/null; then
  ok "task-session-log.sh writes the task↔session link onto the init-created card"
else
  bad "link" "no row for $UUID1 in logs/sessions.md"
fi

# ── 4. session-context-bridge.sh prefers the active task's NEXT.md ────
# A finished task (daemon-written card) with a NEWER NEXT.md must lose.
printf '# NEXT — probe\n- keep going\n' > "$TASK_DIR/NEXT.md"
done_dir=$(card "$WS" 2026/09/30 finished-daemon-task \
  '# finished\n\n- **Status**: done\n- **Goal**: all done\n')
printf '# NEXT — finished\n- nothing left\n' > "$done_dir/NEXT.md"
touch -t 202609010900 "$TASK_DIR/NEXT.md"
touch -t 202609020900 "$done_dir/NEXT.md"
out=$(bridge "$WS")
if contains "$out" "### NEXT.md (card-status-probe)" && ! contains "$out" "finished-daemon-task"; then
  ok "session-context-bridge.sh picks the active task's NEXT.md over a finished task's newer one"
else
  bad "bridge" "got: $(printf '%s' "$out" | head -2 | tr '\n' ' ')"
fi

# ── 5. pause → the hooks let go; resume → they pick it up again ───────
aw "$WS" pause "$TASK_ID" >/dev/null
if grep -qF -- '- **Status**: paused' "$TASK_DIR/README.md"; then
  ok "pause flips the canonical line to paused"
else
  bad "pause" "card says: $(status_lines "$TASK_DIR/README.md")"
fi
out=$(banner "$WS")
if contains "$out" "card-status-probe"; then
  bad "banner after pause" "a paused task is still listed as in flight"
else
  ok "a paused task leaves the in-flight banner"
fi
link "$WS" "$UUID2"
if grep -rq "$UUID2" "$WS" 2>/dev/null; then
  bad "link after pause" "a row for $UUID2 appeared with no active task"
else
  ok "no active task: task-session-log.sh writes no link"
fi
aw "$WS" resume "$TASK_ID" >/dev/null
if grep -qF -- '- **Status**: active' "$TASK_DIR/README.md" \
   && [ "$(grep -cE 'Status|Статус' "$TASK_DIR/README.md")" -eq 1 ]; then
  ok "resume flips it back: one status line, label unchanged"
else
  bad "resume" "card says: $(status_lines "$TASK_DIR/README.md")"
fi
out=$(banner "$WS")
if contains "$out" "card-status-probe"; then
  ok "the resumed task is back in the banner"
else
  bad "banner after resume" "not listed"
fi
if contains "$(aw "$WS" list active)" "$TASK_ID"; then
  ok "list active reads the canonical line"
else
  bad "list active" "got: $(aw "$WS" list | tr '\n' ' ')"
fi
m=$(aw "$WS" metrics)
if [ "$m" = "Working: active=1 paused=0 done-not-archived=1 | Archived: 0" ]; then
  ok "metrics reads the canonical line on the CLI card and the daemon card alike"
else
  bad "metrics" "got: $m"
fi

# ── 6. complete → done + archived; restore → active again ─────────────
aw "$WS" complete "$TASK_ID" >/dev/null
ARCH="$WS/proj/workspace/archive/$TODAY_PATH/card-status-probe"
if grep -qF -- '- **Status**: done' "$ARCH/README.md" 2>/dev/null \
   && grep -qF -- "**Завершено**: $TODAY" "$ARCH/README.md"; then
  ok "complete flips the canonical line to done and stamps the date"
else
  bad "complete" "archive card says: $(status_lines "$ARCH/README.md")"
fi
aw "$WS" restore "$TASK_ID" >/dev/null
if grep -qF -- '- **Status**: active' "$TASK_DIR/README.md" 2>/dev/null \
   && grep -qF -- '**Завершено**: —' "$TASK_DIR/README.md"; then
  ok "restore flips it back to active and clears the date"
else
  bad "restore" "card says: $(status_lines "$TASK_DIR/README.md")"
fi

# ── 7. legacy cards (v5.3 and earlier): read, edited in place, label kept ──
WS2="$TMP/ws-legacy"
LEGACY_ID="2026-09-02-legacy-card"
legacy=$(card "$WS2" 2026/09/02 legacy-card \
  '# Legacy card\n\n- **ID**: 2026-09-02-legacy-card\n- **Статус**: active\n- **Тип**: feature\n- **Старт**: 2026-09-02 · **Завершено**: —\n- **Ціль**: still read\n')
out=$(banner "$WS2")
if contains "$out" "legacy-card"; then
  ok "session-start.sh still lists a **Статус** card"
else
  bad "legacy banner" "not listed"
fi
link "$WS2" "$UUID3"
if grep -q "$UUID3" "$legacy/logs/sessions.md" 2>/dev/null; then
  ok "task-session-log.sh still links to a **Статус** card"
else
  bad "legacy link" "no row for $UUID3"
fi
aw "$WS2" pause "$LEGACY_ID" >/dev/null
if grep -qF -- '- **Статус**: paused' "$legacy/README.md" && ! grep -q 'Status' "$legacy/README.md"; then
  ok "pause edits a legacy card in place and keeps its label"
else
  bad "legacy pause" "card says: $(status_lines "$legacy/README.md")"
fi
aw "$WS2" resume "$LEGACY_ID" >/dev/null
if grep -qF -- '- **Статус**: active' "$legacy/README.md"; then
  ok "resume edits a legacy card in place"
else
  bad "legacy resume" "card says: $(status_lines "$legacy/README.md")"
fi
aw "$WS2" complete "$LEGACY_ID" >/dev/null
LARCH="$WS2/proj/workspace/archive/2026/09/02/legacy-card"
if grep -qF -- '- **Статус**: done' "$LARCH/README.md" 2>/dev/null \
   && grep -qF -- "**Завершено**: $TODAY" "$LARCH/README.md"; then
  ok "complete edits a legacy card in place"
else
  bad "legacy complete" "archive card says: $(status_lines "$LARCH/README.md")"
fi
aw "$WS2" restore "$LEGACY_ID" >/dev/null
if grep -qF -- '- **Статус**: active' "$legacy/README.md" 2>/dev/null; then
  ok "restore edits a legacy card in place"
else
  bad "legacy restore" "card says: $(status_lines "$legacy/README.md")"
fi

# ── 8. every spelling a card can carry is read; prose is not ──────────
WS3="$TMP/ws-forms"
card "$WS3" 2026/09/03 daemon-card '# Daemon card\n\n- **Status**: active\n- **Goal**: minted by taskdir\n' >/dev/null
card "$WS3" 2026/09/04 bold-colon-card '# Task: bold-colon\n\n**Status:** active\n**Goal:** fixture\n' >/dev/null
card "$WS3" 2026/09/05 plain-card '# plain\n\nStatus: active\n' >/dev/null
card "$WS3" 2026/09/06 prose-card '# prose\n\nThe note "Status: active" in this sentence is prose, not a status line.\n' >/dev/null
card "$WS3" 2026/09/07 done-then-prose '# tricky\n\n- **Status**: done\nIts old Status: active remark is stale.\n' >/dev/null
out=$(banner "$WS3")
for slug in daemon-card bold-colon-card plain-card; do
  if contains "$out" "$slug"; then ok "banner reads the $slug form"; else bad "$slug" "not listed"; fi
done
for slug in prose-card done-then-prose; do
  if contains "$out" "$slug"; then bad "$slug" "listed from prose"; else ok "$slug is not in flight"; fi
done

# ── 9. the three readers share one rule, the template the one marker ──
for hook in "$START" "$LINK" "$BRIDGE"; do
  if grep -qF '(Status|Статус)' "$hook"; then
    ok "$(basename "$hook") accepts both labels"
  else
    bad "$(basename "$hook")" "its status-line pattern no longer accepts both labels"
  fi
done
if grep -qF -- '- **Status**: active' "$ROOT/plugins/core/templates/working/README.template.md"; then
  ok "the README card template carries the canonical marker"
else
  bad "template" "README.template.md does not carry '- **Status**: active'"
fi

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
