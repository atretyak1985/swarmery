#!/usr/bin/env bash
# subagent-ledger.test.sh — behavioural tests for the SubagentStop ledger row.
#
# The hook derives the four mechanical cells of the 7-cell delegation ledger
# (agent, phase, verdict, loops, artifact) from the SubagentStop payload, so
# the orchestrator prompt no longer has to. quality and mistakes stay empty —
# they are judgment, not data.
#
# Row shape is a contract with parseLedger() in
# tools/swarmery/internal/wsingest/artifacts.go; the last case asserts the
# emitted rows survive a round-trip through that parser's own rules.
set -euo pipefail

HOOK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/plugins/core/hooks/subagent-stop.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { echo "ok   $1"; pass=$((pass + 1)); }
bad()  { echo "FAIL $1: $2"; fail=$((fail + 1)); }

TASK_ID="2026-09-02-ledger-fixture"
TASK_DIR="$TMP/ws/proj/workspace/working/2026/09/02/ledger-fixture"
mkdir -p "$TASK_DIR"
LOG="$TASK_DIR/logs/agents.md"

run_hook() { # run_hook <json>
  printf '%s' "$1" | AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj \
    AGENT_TASK_ID="$TASK_ID" AGENT_PHASE="${AGENT_PHASE:-}" \
    "$HOOK" >/dev/null 2>&1 || true
}

# ── 1. verdict + artifact are read out of the final message ───────────
run_hook '{"agent_type":"code-reviewer","session_id":"s1","last_assistant_message":"Reviewed the diff.\nWrote reports/phase-2-review.md\n\nVERDICT: FAIL"}'
if [ -f "$LOG" ] && grep -q '| @code-reviewer | phase-2 | FAIL | 1 |  | — | reports/phase-2-review.md |' "$LOG"; then
  ok "verdict and artifact derived from last_assistant_message"
else
  bad "verdict/artifact" "row not as expected: $(tail -1 "$LOG" 2>/dev/null)"
fi

# ── 2. loops counts prior runs of the same agent in the same phase ────
run_hook '{"agent_type":"code-reviewer","session_id":"s2","last_assistant_message":"Re-reviewed.\nreports/phase-2-review.md\n\nVERDICT: PASS"}'
if grep -q '| @code-reviewer | phase-2 | PASS | 2 |' "$LOG"; then
  ok "loops increments for a repeat dispatch"
else
  bad "loops" "expected loops=2, got: $(tail -1 "$LOG")"
fi

# ── 3. a different phase restarts the loop count ──────────────────────
run_hook '{"agent_type":"code-reviewer","session_id":"s3","last_assistant_message":"reports/phase-3-review.md\nVERDICT: PASS"}'
if grep -q '| @code-reviewer | phase-3 | PASS | 1 |' "$LOG"; then
  ok "loops is per-phase, not per-agent"
else
  bad "loops per phase" "got: $(tail -1 "$LOG")"
fi

# ── 4. no verdict emitted → empty cell, never an invented PASS ────────
run_hook '{"agent_type":"implementation-agent","session_id":"s4","last_assistant_message":"Implemented the change. Status: DONE. reports/phase-4-report.md"}'
if grep -q '| @implementation-agent | phase-4 | — | 1 |' "$LOG"; then
  ok "missing verdict stays empty rather than guessed"
else
  bad "absent verdict" "got: $(tail -1 "$LOG")"
fi

# ── 5a. task dir resolved from the transcript when AGENT_TASK_ID is unset ──
# Nothing in the fleet exports AGENT_TASK_ID today, so this fallback is the
# path that actually runs in production. Without it the hook is dead code.
TRANSCRIPT="$TMP/transcript.jsonl"
cat > "$TRANSCRIPT" <<EOF
{"type":"user","text":"unrelated"}
{"type":"assistant","text":"wrote $TASK_DIR/reports/phase-9-report.md"}
EOF
printf '%s' "{\"agent_type\":\"test-writer\",\"session_id\":\"s4b\",\"transcript_path\":\"$TRANSCRIPT\",\"last_assistant_message\":\"reports/phase-9-report.md\\nVERDICT: PASS\"}" \
  | AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj "$HOOK" >/dev/null 2>&1 || true
if grep -q '| @test-writer | phase-9 | PASS | 1 |' "$LOG"; then
  ok "task dir resolved from transcript_path without AGENT_TASK_ID"
else
  bad "transcript fallback" "no row written; hook would be dead code in production"
fi

# ── 5. no task id and no transcript → no file written, hook still exits 0 ──
CLEAN="$TMP/clean"; mkdir -p "$CLEAN"
out_rc=0
printf '%s' '{"agent_type":"debugger","session_id":"s5","last_assistant_message":"VERDICT: PASS"}' \
  | AGENT_WORKSPACE_ROOT="$TMP/ws" AGENT_PROJECT=proj CLAUDE_PROJECT_DIR="$CLEAN" \
    "$HOOK" >/dev/null 2>&1 || out_rc=$?
if [ "$out_rc" -eq 0 ] && [ -z "$(find "$CLEAN" -name agents.md 2>/dev/null)" ]; then
  ok "no AGENT_TASK_ID: writes nothing, exits 0"
else
  bad "no task id" "rc=$out_rc or a file was written"
fi

# ── 6. malformed stdin never breaks the hook ──────────────────────────
rc=0
printf 'not json at all' | AGENT_TASK_ID="$TASK_ID" "$HOOK" >/dev/null 2>&1 || rc=$?
[ "$rc" -eq 0 ] && ok "malformed stdin exits 0" || bad "malformed stdin" "rc=$rc"

# ── 7. every emitted row parses under the wsingest ledger rules ───────
# Mirrors parseLedger(): skip header/divider, >=4 cells, cells[3] loops 0-99,
# cells[4] quality 1-5, last cell = artifact.
rows_ok=1
while IFS= read -r line; do
  case "$line" in \|*) ;; *) continue ;; esac
  first=$(printf '%s' "$line" | awk -F'|' '{gsub(/^ +| +$/,"",$2); print tolower($2)}')
  [ "$first" = "agent" ] && continue
  case "$first" in *---*) continue ;; esac
  n=$(printf '%s' "$line" | awk -F'|' '{print NF-2}')
  [ "$n" -ge 7 ] || { echo "   row has $n cells: $line"; rows_ok=0; }
  loops=$(printf '%s' "$line" | awk -F'|' '{gsub(/ /,"",$5); print $5}')
  case "$loops" in ''|*[!0-9]*) echo "   non-numeric loops: $line"; rows_ok=0 ;; esac
done < "$LOG"
[ "$rows_ok" -eq 1 ] && ok "emitted rows satisfy the wsingest parser contract" \
  || bad "parser contract" "see rows above"

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
