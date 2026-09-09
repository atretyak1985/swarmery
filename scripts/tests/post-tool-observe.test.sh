#!/bin/bash
# Behavioral tests for plugins/core/hooks/post-tool-observe.sh — the merged
# unmatched-PostToolUse hook (activity log + session budget in one process).
#
# Same framework-free style as the other hook suites: feed a PostToolUse payload
# on stdin and assert what came back. Every case also asserts the hook's hard
# contract — valid JSON, exit 0 — because observation and budget advice must
# never fail a tool call mid-flight.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/post-tool-observe.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT
# Markers land under TMPDIR; point it at the sandbox so a real session's marker
# can neither be read nor written by this suite. The activity log is likewise
# sandboxed so the suite never appends to a real session's shared file.
export TMPDIR="$TESTDIR"
export SWARMERY_SESSION_FILE="$TESTDIR/session.jsonl"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

# transcript <tokens…> — a JSONL transcript whose LAST usage-bearing line sums to
# the given context size, with earlier smaller turns before it.
transcript() {
  local total="$1" file
  file="$TESTDIR/t-$RANDOM.jsonl"
  printf '{"message":{"usage":{"input_tokens":10,"cache_read_input_tokens":10,"cache_creation_input_tokens":0}}}\n' > "$file"
  printf '{"message":{"role":"user"}}\n' >> "$file"
  printf '{"message":{"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}\n' \
    "$((total / 2))" "$((total - total / 2 - 1))" 1 >> "$file"
  printf '%s' "$file"
}

# run_hook <session-id> <transcript-path> — echoes the hook's stdout; asserts the
# contract on the way through.
run_hook() {
  local sid="$1" tr="$2" out rc
  out=$(jq -nc --arg s "$sid" --arg t "$tr" '{session_id:$s,transcript_path:$t,tool_name:"Bash"}' | "$HOOK" 2>/dev/null)
  rc=$?
  if [ "$rc" -ne 0 ]; then
    bad "hook exited $rc — it must never fail a tool call"
  elif ! printf '%s' "$out" | jq -e '.continue == true' >/dev/null 2>&1; then
    bad "hook emitted invalid JSON or continue!=true: $out"
  else
    ok
  fi
  printf '%s' "$out"
}

# has_message <json> — does the response carry a systemMessage?
has_message() { printf '%s' "$1" | jq -e 'has("systemMessage")' >/dev/null 2>&1; }

# ── under the budget: silence ─────────────────────────────────────
small=$(transcript 50000)
out=$(run_hook "sess-small" "$small")
if has_message "$out"; then bad "a 50k-token session must not be warned"; else ok; fi

# ── over the budget: the instruction, once ────────────────────────
big=$(transcript 400000)
out=$(run_hook "sess-big" "$big")
if has_message "$out"; then ok; else bad "a 400k-token session must be told to close"; fi

msg=$(printf '%s' "$out" | jq -r '.systemMessage // ""')
# The criterion is explicit about naming BOTH required actions.
printf '%s' "$msg" | grep -qi 'close' && ok || bad "the instruction does not say to CLOSE the session"
printf '%s' "$msg" | grep -qi 'report' && ok || bad "the instruction does not ask for a report"
printf '%s' "$msg" | grep -qi 'start a new session' && ok || bad "the instruction does not say to start a new session"
printf '%s' "$msg" | grep -qi 'summary' && ok || bad "the instruction does not ask for a state summary"
printf '%s' "$msg" | grep -qi 'routine' && ok || bad "the instruction does not offer the routine alternative"

# ── once per crossing, not once per turn ──────────────────────────
# A warning on every subsequent tool call is a context tax that trains the reader
# to skip it — and it would grow the very context it complains about.
repeats=0
for _ in 1 2 3; do
  again=$(run_hook "sess-big" "$big")
  has_message "$again" && repeats=$((repeats + 1))
done
if [ "$repeats" -eq 0 ]; then ok; else bad "the instruction repeated $repeats time(s) after the first crossing"; fi

# A DIFFERENT session over the budget still gets told.
out=$(run_hook "sess-other" "$big")
if has_message "$out"; then ok; else bad "a second fat session was silenced by the first session's marker"; fi

# ── the threshold is configurable ─────────────────────────────────
out=$(jq -nc --arg t "$small" '{session_id:"sess-cfg",transcript_path:$t}' |
  SWARMERY_SESSION_BUDGET_TOKENS=10000 "$HOOK" 2>/dev/null)
if has_message "$out"; then ok; else bad "SWARMERY_SESSION_BUDGET_TOKENS did not lower the threshold"; fi

# ── degenerate payloads fail open and silent ──────────────────────
for payload in '{}' '{"session_id":"x"}' '{"session_id":"x","transcript_path":"/nope/missing.jsonl"}' 'not json'; do
  out=$(printf '%s' "$payload" | "$HOOK" 2>/dev/null); rc=$?
  if [ "$rc" -eq 0 ] && printf '%s' "$out" | jq -e '.continue == true' >/dev/null 2>&1 && ! has_message "$out"; then
    ok
  else
    bad "payload $payload → rc=$rc out=$out (want a silent, valid pass-through)"
  fi
done

# A transcript with no usage records at all (a session that has not run a model
# turn yet) must not be read as zero-and-warned or as a crash.
empty="$TESTDIR/empty.jsonl"
printf '{"message":{"role":"user"}}\n' > "$empty"
out=$(run_hook "sess-empty" "$empty")
if has_message "$out"; then bad "a transcript with no usage records was warned"; else ok; fi

# ── the threshold must not contradict the advisor ─────────────────
# The hook warns BEFORE the advisor's fat-session line, never after: an
# instruction that arrives when the session is already flagged arrives after the
# money is spent. This is the one cross-language invariant of the pair, so it is
# asserted from the sources rather than trusted to a comment.
hook_default=$(grep -oE 'SWARMERY_SESSION_BUDGET_TOKENS:-[0-9]+' "$HOOK" | grep -oE '[0-9]+$')
r9=$(grep -oE 'R9ContextTokens = [0-9_]+' "$ROOT/tools/swarmery/internal/advisor/rules.go" | grep -oE '[0-9_]+$' | tr -d '_')
if [ -n "$hook_default" ] && [ -n "$r9" ] && [ "$hook_default" -le "$r9" ]; then
  ok
else
  bad "the budget default ($hook_default) must be at or below the advisor's R9ContextTokens ($r9)"
fi

# ── activity log: one compact line per tool call ──────────────────
rm -f "$SWARMERY_SESSION_FILE"
out=$(jq -nc '{tool_name:"Bash",tool_input:{command:"ls -la"},session_id:"sess-log"}' | "$HOOK" 2>/dev/null)
if [ -f "$SWARMERY_SESSION_FILE" ] \
   && jq -e 'select(.tool=="Bash" and .cmd=="ls -la")' "$SWARMERY_SESSION_FILE" >/dev/null 2>&1; then
  ok
else
  bad "a Bash tool call did not append a {tool,cmd} line to the activity log"
fi

# An Agent dispatch whose observed model differs from the requested one logs a
# ModelFallback event — the routing/cost report's raw signal.
rm -f "$SWARMERY_SESSION_FILE"
jq -nc '{tool_name:"Agent",tool_input:{model:"opus"},tool_response:{model:"sonnet"},session_id:"sess-fb"}' | "$HOOK" >/dev/null 2>&1
if jq -e 'select(.tool=="ModelFallback")' "$SWARMERY_SESSION_FILE" >/dev/null 2>&1; then
  ok
else
  bad "a requested-vs-observed model mismatch did not log a ModelFallback event"
fi

printf 'post-tool-observe: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
