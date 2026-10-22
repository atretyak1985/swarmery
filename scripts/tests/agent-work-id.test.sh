#!/bin/bash
# Behavioral tests: agent-work.sh accepts only canonical task ids.
#
# Every entry point that takes a task id (phase, pause, resume, view, complete /
# archive, restore) builds a directory path from it. An id is
# <yyyy-mm-dd>-<slug>, slug [a-z0-9][a-z0-9-]*, and nothing else: an id that
# carries "/" or ".." would name a directory outside working/ or archive/, and
# `complete` / `restore` MOVE what they resolve. Anything else exits 2 before a
# path is built. `--latest` stays accepted.
#
# Hermetic: the workspace is a mktemp dir reached through AGENT_PROJECT +
# AGENT_WORKSPACE_ROOT, so no real workspace is read or written.
# Run: bash scripts/tests/agent-work-id.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AW="$ROOT/plugins/core/bin/agent-work.sh"

pass=0
fail=0
record() {
  local expected="$1" actual="$2" desc="$3"
  if [ "$actual" -eq "$expected" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s (expected exit %s, got %s)\n' "$desc" "$expected" "$actual"
  fi
}

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
WS="$T/wsroot/id-suite/workspace"
mkdir -p "$WS/working/2026/10/22/ok" "$WS/archive/2026/10/21/old" "$WS/victim"
printf -- '- **Status**: active\n' > "$WS/working/2026/10/22/ok/README.md"
printf -- '- **Status**: done\n'   > "$WS/archive/2026/10/21/old/README.md"
printf 'not a task\n'              > "$WS/victim/README.md"

# aw <args...> — run the CLI against the sandbox workspace; sets RC.
RC=0
aw() {
  env AGENT_PROJECT=id-suite AGENT_WORKSPACE_ROOT="$T/wsroot" HOME="$T" \
    CLAUDE_PROJECT_DIR="$T" bash "$AW" "$@" >/dev/null 2>&1
  RC=$?
}

# ── Rejected (exit 2) ────────────────────────────────────────────
aw view '../../x';                          record 2 "$RC" "view ../../x"
aw view '2026-10-22-../../../../victim';    record 2 "$RC" "view: slug climbing out of working/"
aw pause '2026-10-22-../../../../victim';   record 2 "$RC" "pause: slug climbing out of working/"
aw resume '2026-10-22-ok/../ok';            record 2 "$RC" "resume: slug with a path separator"
aw phase '2026-10-22-../../../../victim' 01-x; record 2 "$RC" "phase: slug climbing out of working/"
aw view '2026-10-22-OK';                    record 2 "$RC" "view: upper-case slug"
aw view '2026-10-22-';                      record 2 "$RC" "view: empty slug"
aw view '-rf';                              record 2 "$RC" "view: option-shaped id"
aw complete '2026-10-22-../../../../victim'; record 2 "$RC" "complete: slug climbing out of working/"
if [ -f "$WS/victim/README.md" ]; then
  record 0 0 "complete did not move a dir outside working/"
else
  record 0 1 "complete moved a dir outside working/"
fi
aw restore '2026-10-21-../../../../victim'; record 2 "$RC" "restore: slug climbing out of archive/"
if [ -f "$WS/victim/README.md" ]; then
  record 0 0 "restore did not move a dir outside archive/"
else
  record 0 1 "restore moved a dir outside archive/"
fi

# ── Accepted ─────────────────────────────────────────────────────
aw view '2026-10-22-ok';   record 0 "$RC" "view 2026-10-22-ok"
aw view --latest;          record 0 "$RC" "view --latest"
aw pause '2026-10-22-ok';  record 0 "$RC" "pause 2026-10-22-ok"
aw resume '2026-10-22-ok'; record 0 "$RC" "resume 2026-10-22-ok"
# A well-formed id that does not exist is "not found" (1), not "invalid" (2).
aw view '2026-10-22-missing'; record 1 "$RC" "view of a missing but valid id"

printf 'agent-work-id: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
