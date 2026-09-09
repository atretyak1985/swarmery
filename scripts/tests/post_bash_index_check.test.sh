#!/bin/bash
# Behavioral tests for plugins/core/hooks/post_bash_index_check.sh.
#
# Framework-free (portable, no bats dependency): each case builds a throwaway
# git repo + graphify-out/graph.json, points CLAUDE_PROJECT_DIR at it, feeds a
# PostToolUse hook payload on stdin, and asserts (a) the hook exits 0, (b) its
# stdout is valid JSON, (c) the nudge is present/absent as expected, and (d) the
# per-session marker was created (or not). Run locally with
# `bash scripts/tests/post_bash_index_check.test.sh`; wired into CI alongside the
# shell-syntax/shellcheck gates and the protect-sensitive-files behavioral tests.
set -uo pipefail

HOOK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/plugins/core/hooks/post_bash_index_check.sh"

pass=0
fail=0

fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

# valid_json <string> — succeeds (exit 0) iff the string parses as JSON.
valid_json() { printf '%s' "$1" | python3 -c 'import sys,json; json.load(sys.stdin)' 2>/dev/null; }

# make_repo <dir> <graph-built-commit|SKIP> — build a git repo; when the second
# arg is a SHA, drop a graphify-out/graph.json recording it as built_at_commit;
# when "SKIP", omit the graph entirely (no-graph case).
make_repo() {
  local dir="$1" built="$2"
  mkdir -p "$dir"
  git -C "$dir" init -q
  git -C "$dir" config user.email t@t.t
  git -C "$dir" config user.name t
  echo seed > "$dir/seed.txt"
  git -C "$dir" add -A
  git -C "$dir" commit -qm seed
  if [ "$built" != "SKIP" ]; then
    mkdir -p "$dir/graphify-out"
    printf '{"built_at_commit": "%s", "nodes": []}\n' "$built" > "$dir/graphify-out/graph.json"
  fi
}

# run_hook <dir> <session_id> <stdin-json> — invoke the hook with CLAUDE_PROJECT_DIR
# set. Sets globals OUT (stdout) and RC (exit code). Capturing via globals (not a
# command-substitution return) is deliberate: $? from inside a $(...) subshell
# never reaches the caller, and `set -u` would then trip on an unset RC.
# ($2 session_id is documentary — the id the hook acts on comes from the stdin JSON.)
RC=0
OUT=""
run_hook() {
  local dir="$1" stdin="$3"
  OUT=$(printf '%s' "$stdin" | CLAUDE_PROJECT_DIR="$dir" "$HOOK")
  RC=$?
}

TMP=$(mktemp -d)
SID="test-$$-$RANDOM"
MARKER="/tmp/graphify-stale-nudge-$SID"
cleanup() { rm -rf "$TMP"; rm -f "$MARKER" "/tmp/graphify-stale-nudge-$SID-fresh" "/tmp/graphify-stale-nudge-$SID-mal"; }
trap cleanup EXIT

# ── Case 1: stale graph + no marker → nudge emitted + marker created ──────────
rm -f "$MARKER"
STALE_REPO="$TMP/stale"
make_repo "$STALE_REPO" "0000000000000000000000000000000000000000"
run_hook "$STALE_REPO" "$SID" "{\"session_id\":\"$SID\",\"tool_name\":\"Bash\"}"
if [ "$RC" -eq 0 ] && valid_json "$OUT" && [[ "$OUT" == *systemMessage* ]] && [ -f "$MARKER" ]; then
  ok_case
else
  fail_case "case1 stale+no-marker: expected nudge+marker (rc=$RC, marker=$( [ -f "$MARKER" ] && echo yes || echo no ), out=$OUT)"
fi

# ── Case 2: stale graph + marker already present → silent pass-through ────────
# (marker was created by case 1; assert the second call is silent and non-nudging)
run_hook "$STALE_REPO" "$SID" "{\"session_id\":\"$SID\",\"tool_name\":\"Bash\"}"
if [ "$RC" -eq 0 ] && valid_json "$OUT" && [[ "$OUT" != *systemMessage* ]]; then
  ok_case
else
  fail_case "case2 stale+marker: expected silent pass-through (rc=$RC, out=$OUT)"
fi

# ── Case 3: fresh graph → silent (no nudge regardless of marker) ──────────────
FRESH_REPO="$TMP/fresh"
FRESH_SID="$SID-fresh"
make_repo "$FRESH_REPO" "PLACEHOLDER"
HEAD_SHA=$(git -C "$FRESH_REPO" rev-parse HEAD)
printf '{"built_at_commit": "%s", "nodes": []}\n' "$HEAD_SHA" > "$FRESH_REPO/graphify-out/graph.json"
run_hook "$FRESH_REPO" "$FRESH_SID" "{\"session_id\":\"$FRESH_SID\",\"tool_name\":\"Bash\"}"
if [ "$RC" -eq 0 ] && valid_json "$OUT" && [[ "$OUT" != *systemMessage* ]] && [ ! -f "/tmp/graphify-stale-nudge-$FRESH_SID" ]; then
  ok_case
else
  fail_case "case3 fresh: expected silent, no marker (rc=$RC, out=$OUT)"
fi

# ── Case 4: no graph.json → silent ───────────────────────────────────────────
NOGRAPH_REPO="$TMP/nograph"
make_repo "$NOGRAPH_REPO" "SKIP"
run_hook "$NOGRAPH_REPO" "$SID" "{\"session_id\":\"$SID\",\"tool_name\":\"Bash\"}"
if [ "$RC" -eq 0 ] && valid_json "$OUT" && [[ "$OUT" != *systemMessage* ]]; then
  ok_case
else
  fail_case "case4 no-graph: expected silent (rc=$RC, out=$OUT)"
fi

# ── Case 5: malformed stdin → valid JSON out, exit 0 ─────────────────────────
# Missing/unparseable session_id → fail open (behave as today): on a stale graph
# it still nudges, but must never crash and must emit valid JSON.
MAL_SID="$SID-mal"  # only used to name a stale repo; stdin carries no parseable id
run_hook "$STALE_REPO" "$MAL_SID" 'this is not json {{{'
if [ "$RC" -eq 0 ] && valid_json "$OUT"; then
  ok_case
else
  fail_case "case5 malformed-stdin: expected valid JSON + exit 0 (rc=$RC, out=$OUT)"
fi

printf 'post_bash_index_check: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
