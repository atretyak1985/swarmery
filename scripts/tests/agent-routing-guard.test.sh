#!/bin/bash
# Behavioral tests for plugins/core/hooks/agent-routing-guard.sh.
#
# The gate points search-shaped `general-purpose` dispatches at `Explore` — once
# per session, never twice. Two properties matter more than the message itself
# and are asserted hardest here: it must NOT fire on ordinary implementation
# work, and it must never be able to trap a run.
#
# EVERY ASSERTION IS ON THE DECISION, NEVER ON THE EXIT CODE. The rule ships in
# `warn` (exit 0 + a WARN line) and will later be flipped to `block` (exit 2 +
# a BLOCKED line) from a filled row in docs/GATE-HARDENING.md. A suite that
# asserted exit codes would have to be rewritten on that flip — and a suite
# rewritten during a flip proves nothing about the flip. The `[gp-search-routing]`
# tag is on stderr in both modes, so these tests survive it unmodified.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/agent-routing-guard.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT
export TMPDIR="$TESTDIR"                                   # session markers land in the sandbox
export AGENT_ROUTING_GUARD_LOG="$TESTDIR/burn-in.jsonl"    # never the operator's real counter

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

RULE_TAG='[gp-search-routing]'

# decision <session> <agent-type> <description> — FIRE or SILENT, read off the
# rule tag on stderr rather than off the exit code (see the header).
decision() {
  local sid="$1" atype="$2" desc="$3" err
  err=$(jq -nc --arg a "$atype" --arg d "$desc" --arg s "$sid" \
    '{session_id:$s,tool_name:"Agent",tool_input:{subagent_type:$a,description:$d}}' |
    "$HOOK" 2>&1 >/dev/null)
  if printf '%s' "$err" | grep -qF "$RULE_TAG"; then printf 'FIRE'; else printf 'SILENT'; fi
}

expect() {
  local want="$1" desc="$2" got="$3"
  if [ "$got" = "$want" ]; then ok; else bad "$desc — expected $want, got $got"; fi
}

# ── it fires on a search-shaped general-purpose dispatch ──────────
expect FIRE "general-purpose asked to find where something is" \
  "$(decision s1 general-purpose 'find where the telemetry store is written')"
expect FIRE "plugin-prefixed general-purpose, Ukrainian brief" \
  "$(decision s2 core:general-purpose 'перелічити всі місця де')"
# The vocabulary this phase added on top of architecture-freshness.sh's.
expect FIRE "what uses"    "$(decision s3 general-purpose 'what uses the approvals table')"
expect FIRE "list"         "$(decision s4 general-purpose 'list every hook on the Agent matcher')"
expect FIRE "знайти"       "$(decision s5 general-purpose 'знайти всі виклики ingest')"
expect FIRE "зрозуміти, де" "$(decision s6 general-purpose 'зрозуміти, де живе FSM місії')"
# And the vocabulary it inherited verbatim.
expect FIRE "which files"  "$(decision s7 general-purpose 'which files reference the cost model')"
expect FIRE "trace"        "$(decision s8 general-purpose 'trace the session ingest path')"

# ── and NOT on anything else ──────────────────────────────────────
# THE false-positive cases. A gate that fires on implementation work is a gate
# the fleet learns to route around, which is worse than no gate at all.
expect SILENT "Explore is already the right agent" \
  "$(decision s10 Explore 'find where the telemetry store is written')"
expect SILENT "the read-only researcher is already the right agent" \
  "$(decision s11 core:researcher 'find where the telemetry store is written')"
expect SILENT "general-purpose implementing" \
  "$(decision s12 general-purpose 'implement the readiness latch')"
expect SILENT "general-purpose fixing" \
  "$(decision s13 general-purpose 'fix the flaky approvals test')"
expect SILENT "implementation-agent" \
  "$(decision s14 core:implementation-agent 'find where the telemetry store is written')"

# ── it can never trap a run ───────────────────────────────────────
# THE property. One message per session, then the gate stands aside whatever the
# agent decided — including when it judged the routing advice wrong.
first=$(decision trap-session general-purpose 'find where the telemetry store is written')
second=$(decision trap-session general-purpose 'find where the telemetry store is written')
third=$(decision trap-session general-purpose 'locate the ingest entrypoint')
if [ "$first" = "FIRE" ] && [ "$second" = "SILENT" ] && [ "$third" = "SILENT" ]; then
  ok
else
  bad "the gate must fire once per session and then stand aside (got $first/$second/$third)"
fi
# A different session is still judged on its own.
expect FIRE "a second session is judged on its own" \
  "$(decision other-session general-purpose 'find where the telemetry store is written')"

# ── degenerate inputs and the kill switch ─────────────────────────
expect SILENT "no session id — cannot count, must not fire" \
  "$(decision '' general-purpose 'find where the telemetry store is written')"

# A payload with no subagent_type at all.
for payload in '{"session_id":"n1","tool_input":{"description":"find where the store is"}}' \
               '{"session_id":"n2","tool_input":{}}' \
               '{}' \
               'not json'; do
  err=$(printf '%s' "$payload" | "$HOOK" 2>&1 >/dev/null)
  rc=$?
  if [ "$rc" -eq 0 ] && [ -z "$err" ]; then ok
  else bad "payload $payload must pass silently (rc=$rc, stderr='$err')"; fi
done

# The kill switch silences a payload that would otherwise fire.
err=$(jq -nc '{session_id:"kill",tool_input:{subagent_type:"general-purpose",description:"find where the telemetry store is written"}}' |
  SWARMERY_AGENT_ROUTING=0 "$HOOK" 2>&1 >/dev/null)
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$err" ]; then ok
else bad "SWARMERY_AGENT_ROUTING=0 must disable the gate (rc=$rc, stderr='$err')"; fi

# A missing jq must not block work. `command -v jq` is the hook's own guard; the
# probe removes jq from PATH entirely rather than shadowing it, because a stub
# that exits non-zero would be testing the stub.
stubpath="$TESTDIR/nojq"; mkdir -p "$stubpath"
for t in cat printf grep tr mkdir dirname date; do
  src=$(command -v "$t") && ln -sf "$src" "$stubpath/$t"
done
err=$(printf '%s' '{"session_id":"nojq","tool_input":{"subagent_type":"general-purpose","description":"find where the telemetry store is written"}}' |
  PATH="$stubpath" "$HOOK" 2>&1 >/dev/null)
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$err" ]; then ok
else bad "a missing jq must exit 0 silently, never block a dispatch (rc=$rc, stderr='$err')"; fi

# ── the message has to be actionable ──────────────────────────────
# SC-8 is the routing half, SC-9 the budget half, and BOTH have to reach the
# model — this stderr text is the only place the step ceiling is stated at the
# moment someone is writing the brief.
msg=$(jq -nc '{session_id:"msg",tool_input:{subagent_type:"general-purpose",description:"find where the telemetry store is written"}}' |
  "$HOOK" 2>&1 >/dev/null)
for needle in 'gp-search-routing' 'Explore' 'search breadth' 'completion criterion' 'step ceiling' 'once per session'; do
  if printf '%s' "$msg" | grep -qiF "$needle"; then ok; else bad "the message never mentions '$needle'"; fi
done

# ── the burn-in counter has to be readable ────────────────────────
# A warn-mode rule whose hits cannot be counted can never be argued into `block`,
# which is the whole point of shipping it in warn. The record shape is
# bash-shape-guard.jsonl's, so scripts/guard-hits.sh reads it with --log.
if [ -s "$AGENT_ROUTING_GUARD_LOG" ]; then
  if jq -e -s 'all(.rule == "gp-search-routing" and .decision != "" and .hook == "agent-routing-guard")' \
      "$AGENT_ROUTING_GUARD_LOG" >/dev/null 2>&1; then
    ok
  else
    bad "burn-in records are not in the shape scripts/guard-hits.sh reads"
  fi
  if "$ROOT/scripts/guard-hits.sh" --log "$AGENT_ROUTING_GUARD_LOG" 2>/dev/null | grep -q 'gp-search-routing'; then
    ok
  else
    bad "scripts/guard-hits.sh --log does not report gp-search-routing hits"
  fi
else
  bad "the gate fired but wrote no burn-in record"
fi

# ── the two hooks on the Agent matcher stay wired, in order ───────
# freshness-then-routing: they are independent, but an agent that reads two
# refusals for one dispatch reads the harness as broken, so the order is pinned
# where it can be seen.
HOOKS_JSON="$ROOT/plugins/core/hooks/hooks.json"
agent_hooks=$(jq -r '.hooks.PreToolUse[] | select(.matcher=="Agent") | .hooks[].command' "$HOOKS_JSON" 2>/dev/null)
expected='${CLAUDE_PLUGIN_ROOT}/hooks/architecture-freshness.sh
${CLAUDE_PLUGIN_ROOT}/hooks/agent-routing-guard.sh'
if [ "$agent_hooks" = "$expected" ]; then ok
else bad "the Agent matcher must run freshness then routing; got: $agent_hooks"; fi

printf 'agent-routing-guard: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
