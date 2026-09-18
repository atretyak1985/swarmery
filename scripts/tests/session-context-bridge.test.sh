#!/bin/bash
# Behavioral tests for plugins/core/hooks/session-context-bridge.sh.
#
# The hook injects last session's state (the newest NEXT.md + the newest daemon
# handoff brief) into a cold session. Two properties matter more than the
# content and are asserted hardest here: it must stay BOUNDED (a context nicety
# that grows without limit is a cost regression), and it must never be able to
# fail a session start — no daemon, no workspace, a garbage response, all of
# them are "print nothing, exit 0".
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/session-context-bridge.sh"
STUB="$ROOT/scripts/tests/fixtures/handoff_stub.py"

TESTDIR=$(mktemp -d)
STUB_PID=""
STUB_PORT=""
cleanup() {
  [ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
  rm -rf "$TESTDIR"
}
trap cleanup EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

contains() { # <haystack> <needle> <description>
  if printf '%s' "$1" | grep -qF -- "$2"; then ok; else bad "$3 — expected to find '$2'"; fi
}
lacks() { # <haystack> <needle> <description>
  if printf '%s' "$1" | grep -qF -- "$2"; then bad "$3 — did not expect '$2'"; else ok; fi
}

REPO="$TESTDIR/repo"
mkdir -p "$REPO"

# task <workspace-root> <date-path> <slug> — creates the task dir, echoes it.
# Callers that care about mtime use `touch -t CCYYMMDDhhmm`, the POSIX spelling
# both BSD and GNU accept, so the fixture needs no date -v / date -d split.
task() {
  local ws="$1" datepath="$2" slug="$3" dir
  dir="$ws/proj/workspace/working/$datepath/$slug"
  mkdir -p "$dir"
  printf '%s' "$dir"
}

# hook_out <workspace-root> <port> [bridge-flag] — the hook's stdout.
hook_out() {
  CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$1" AGENT_PROJECT=proj \
    SWARMERY_PORT="$2" SWARMERY_CONTEXT_BRIDGE="${3:-1}" "$HOOK" 2>/dev/null
}

# hook_rc <workspace-root> <port> [bridge-flag] — the hook's exit code.
hook_rc() {
  CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$1" AGENT_PROJECT=proj \
    SWARMERY_PORT="$2" SWARMERY_CONTEXT_BRIDGE="${3:-1}" "$HOOK" >/dev/null 2>&1
  printf '%s' "$?"
}

# start_stub <mode> — sets STUB_PORT to the port the stub bound, and STUB_PID to
# its pid. It must NOT print the port for a caller to capture: `$(start_stub …)`
# runs the body in a subshell, so STUB_PID would die with it and stop_stub/the
# EXIT trap would kill nothing, orphaning a serve_forever() per suite run.
start_stub() {
  local log port i
  log="$TESTDIR/stub-$1.log"
  python3 "$STUB" "$1" > "$log" 2>&1 &
  STUB_PID=$!
  i=0
  port=""
  while [ "$i" -lt 60 ]; do
    port=$(grep -m1 '^PORT ' "$log" 2>/dev/null | cut -d' ' -f2)
    [ -n "$port" ] && break
    sleep 0.1
    i=$((i + 1))
  done
  STUB_PORT="$port"
}

stop_stub() {
  [ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
  wait "$STUB_PID" 2>/dev/null
  STUB_PID=""
  STUB_PORT=""
}

# A port nothing listens on — "the daemon is down".
DEAD_PORT=$(python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()')

# ── Fixtures ──────────────────────────────────────────────────────
# WS: two tasks, different mtimes — the newer one must win.
WS="$TESTDIR/ws"
older=$(task "$WS" 2026/09/01 older-task)
newer=$(task "$WS" 2026/09/02 newer-task)
printf '# NEXT — older\n- do the old thing\n' > "$older/NEXT.md"
printf '# NEXT — newer\n- do the new thing\n' > "$newer/NEXT.md"
touch -t 202609010900 "$older/NEXT.md"
touch -t 202609020900 "$newer/NEXT.md"

# WSLINES: one long NEXT.md of short lines — proves the 40-line slice.
WSLINES="$TESTDIR/ws-lines"
lines_task=$(task "$WSLINES" 2026/09/03 long-task)
: > "$lines_task/NEXT.md"
i=1
while [ "$i" -le 120 ]; do
  printf 'line-%03d\n' "$i" >> "$lines_task/NEXT.md"
  i=$((i + 1))
done

# WSBIG: an oversized NEXT.md — with an oversized brief it proves the total cap.
WSBIG="$TESTDIR/ws-big"
big_task=$(task "$WSBIG" 2026/09/04 big-task)
: > "$big_task/NEXT.md"
i=1
while [ "$i" -le 200 ]; do
  printf 'next-filler-%03d %s\n' "$i" "$(printf 'x%.0s' $(seq 1 100))" >> "$big_task/NEXT.md"
  i=$((i + 1))
done

# WSTINY: a one-line NEXT.md — paired with the oversized brief it isolates the
# handoff cap, since neither the NEXT cap nor the total cap can bind.
WSTINY="$TESTDIR/ws-tiny"
tiny_task=$(task "$WSTINY" 2026/09/05 tiny-task)
printf -- '- tiny\n' > "$tiny_task/NEXT.md"

# WSEMPTY: a workspace root with no tasks at all.
WSEMPTY="$TESTDIR/ws-empty"
mkdir -p "$WSEMPTY/proj/workspace/working"

# ── Daemon down: the NEXT.md block, and only it ───────────────────
out=$(hook_out "$WS" "$DEAD_PORT")
contains "$out" '### NEXT.md (newer-task)' "the newest NEXT.md names its task slug"
contains "$out" 'do the new thing'         "the newest NEXT.md body is injected"
lacks    "$out" 'do the old thing'         "an older NEXT.md must not win"
lacks    "$out" 'Handoff brief'            "no daemon means no handoff block"
rc=$(hook_rc "$WS" "$DEAD_PORT")
if [ "$rc" = "0" ]; then ok; else bad "a dead daemon must still exit 0 (got $rc)"; fi

# ── Daemon serving a brief: both blocks ───────────────────────────
start_stub hit
port="$STUB_PORT"
if [ -z "$port" ]; then
  bad "the stub daemon never reported a port"
else
  out=$(hook_out "$WS" "$port")
  contains "$out" '### NEXT.md (newer-task)' "the NEXT.md block survives alongside the brief"
  contains "$out" '### Handoff brief (2026-09-17T18:30:00Z, 164000 tokens)' \
    "the handoff header carries created_at and context_tokens"
  contains "$out" 'finish phase 1' "the brief body is injected"
fi
stop_stub

# ── 200 then 204: the second start gets no brief ──────────────────
start_stub hit-then-empty
port="$STUB_PORT"
if [ -z "$port" ]; then
  bad "the stub daemon never reported a port"
else
  first=$(hook_out "$WS" "$port")
  second=$(hook_out "$WS" "$port")
  contains "$first"  'Handoff brief'            "a 200 injects the brief"
  lacks    "$second" 'Handoff brief'            "a 204 injects nothing"
  contains "$second" '### NEXT.md (newer-task)' "a 204 leaves the NEXT.md block intact"
fi
stop_stub

# ── A daemon that predates the route ──────────────────────────────
# Measured against the live daemon: an unknown /api path falls through to the
# SPA, so the hook gets 200 + index.html, NOT a 404. Injecting that would put a
# page of HTML into the model's context on every session start.
start_stub html
port="$STUB_PORT"
if [ -z "$port" ]; then
  bad "the stub daemon never reported a port"
else
  out=$(hook_out "$WS" "$port")
  lacks    "$out" 'Handoff brief'            "a 200 that is not JSON injects nothing"
  lacks    "$out" '<!doctype html>'          "the SPA fallback never reaches the context"
  contains "$out" '### NEXT.md (newer-task)' "an old daemon leaves the NEXT.md block intact"
fi
stop_stub

# ── The kill switch ───────────────────────────────────────────────
start_stub hit
port="$STUB_PORT"
if [ -z "$port" ]; then
  bad "the stub daemon never reported a port"
else
  out=$(hook_out "$WS" "$port" 0)
  if [ -z "$out" ]; then ok; else bad "SWARMERY_CONTEXT_BRIDGE=0 must print nothing (got ${#out} bytes)"; fi
  rc=$(hook_rc "$WS" "$port" 0)
  if [ "$rc" = "0" ]; then ok; else bad "the kill switch must exit 0 (got $rc)"; fi
fi
stop_stub

# ── Bounds ────────────────────────────────────────────────────────
# The slice stops at 40 lines, whatever the file's length.
out=$(hook_out "$WSLINES" "$DEAD_PORT")
contains "$out" 'line-040' "the NEXT.md slice reaches its 40th line"
lacks    "$out" 'line-041' "the NEXT.md slice stops at 40 lines"

# MAX_NEXT alone: an oversized NEXT.md with NO daemon, so the total cap (3072)
# cannot be what stops it. 40 lines x 117 bytes would be 4680; the 2048-byte
# slice plus the "### NEXT.md (big-task)" header and its newline lands at 2072.
# Drop `head -c "$MAX_NEXT"` from the hook and this reads 3072 instead.
CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$WSBIG" AGENT_PROJECT=proj \
  SWARMERY_PORT="$DEAD_PORT" "$HOOK" > "$TESTDIR/next-cap.out" 2>/dev/null
next_bytes=$(wc -c < "$TESTDIR/next-cap.out" | tr -d ' ')
if [ "$next_bytes" -gt 2048 ] && [ "$next_bytes" -le 2100 ]; then
  ok
else
  bad "the NEXT.md slice must cap the block at 2049..2100 bytes (got $next_bytes)"
fi

# THE bound: the whole injected block, oversized note + oversized brief.
start_stub big
port="$STUB_PORT"
if [ -z "$port" ]; then
  bad "the stub daemon never reported a port"
else
  # Measured on the raw stream, not on "$(…)" — command substitution eats the
  # trailing newline and would under-report the very byte the cap is about.
  CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$WSBIG" AGENT_PROJECT=proj \
    SWARMERY_PORT="$port" "$HOOK" > "$TESTDIR/big.out" 2>/dev/null
  bytes=$(wc -c < "$TESTDIR/big.out" | tr -d ' ')
  if [ "$bytes" -le 3072 ] && [ "$bytes" -gt 0 ]; then
    ok
  else
    bad "the injected block must be 1..3072 bytes (got $bytes)"
  fi

  # MAX_HANDOFF alone: a one-line NEXT.md against the same oversized brief, so
  # the whole block stays well under 3072 and only the handoff cap can bind.
  # The brief portion is the file past the "### Handoff brief" header: 1024
  # sliced bytes plus the newline the hook appends. Drop `head -c
  # "$MAX_HANDOFF"` from the hook and this reads ~2984 instead.
  CLAUDE_PROJECT_DIR="$REPO" AGENT_WORKSPACE_ROOT="$WSTINY" AGENT_PROJECT=proj \
    SWARMERY_PORT="$port" "$HOOK" > "$TESTDIR/tiny.out" 2>/dev/null
  brief_bytes=$(sed -n '/^### Handoff brief/,$p' "$TESTDIR/tiny.out" | tail -n +2 | wc -c | tr -d ' ')
  if [ "$brief_bytes" -ge 1000 ] && [ "$brief_bytes" -le 1060 ]; then
    ok
  else
    bad "the handoff brief must be capped at ~1024 bytes (got $brief_bytes)"
  fi
fi
stop_stub

# ── Degenerate inputs never trap a session start ──────────────────
out=$(hook_out "$WSEMPTY" "$DEAD_PORT")
if [ -z "$out" ]; then ok; else bad "an empty workspace must print nothing (got '$out')"; fi
rc=$(hook_rc "$WSEMPTY" "$DEAD_PORT")
if [ "$rc" = "0" ]; then ok; else bad "an empty workspace must exit 0 (got $rc)"; fi

rc=$(hook_rc "$TESTDIR/does-not-exist" "$DEAD_PORT")
if [ "$rc" = "0" ]; then ok; else bad "a missing workspace root must exit 0 (got $rc)"; fi

# No workspace env at all — the hook falls back to the project dir and finds none.
CLAUDE_PROJECT_DIR="$REPO" SWARMERY_PORT="$DEAD_PORT" "$HOOK" >/dev/null 2>&1
rc=$?
if [ "$rc" = "0" ]; then ok; else bad "an unconfigured workspace must exit 0 (got $rc)"; fi

# ── The block has to read as markdown ─────────────────────────────
# session-start.sh's banner is ANSI-coloured; this output is model context, so
# an escape sequence here is noise the model pays for.
out=$(hook_out "$WS" "$DEAD_PORT")
if printf '%s' "$out" | grep -q $'\033'; then
  bad "the injected block must contain no ANSI escapes"
else
  ok
fi

# ── The hook is registered where it can actually run ──────────────
if grep -q 'session-context-bridge.sh' "$ROOT/plugins/core/hooks/hooks.json"; then ok
else bad "session-context-bridge.sh is not registered in hooks.json"; fi
if [ -x "$HOOK" ]; then ok; else bad "the hook is not executable"; fi

printf 'session-context-bridge: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
