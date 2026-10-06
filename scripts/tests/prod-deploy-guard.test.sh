#!/bin/bash
# Behavioural tests for plugins/core/hooks/prod-deploy-guard.sh.
#
# Framework-free, same style as protect-sensitive-files.test.sh: feed a hook
# JSON payload on stdin and assert what the hook prints. A match must print a
# PreToolUse decision that parses with jq; a non-match must print NOTHING (any
# output at all would interfere with the normal permission flow). The hook must
# exit 0 in every case.
#
# Hermetic: CLAUDE_PROJECT_DIR and PROD_DEPLOY_GUARD_LOG point into a mktemp
# dir, and the session variables the hook reads (CLAUDE_CODE_ENTRYPOINT,
# CLAUDE_CODE_SESSION_ATTENDED) are cleared, so running this suite from inside a
# Claude Code session neither changes its result nor pollutes the real burn-in
# log.
#
# The hook is EXECUTED by path, not run as `bash "$HOOK"`: Claude Code invokes
# it through its own shebang, which on macOS is bash 3.2.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/prod-deploy-guard.sh"
CASES="$ROOT/scripts/tests/fixtures/prod-deploy-patterns/cases.tsv"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT

unset CLAUDE_CODE_ENTRYPOINT CLAUDE_CODE_SESSION_ATTENDED AGENT_PROJECT AGENT_WORKSPACE_ROOT
PROJ="$TESTDIR/project"
mkdir -p "$PROJ/.claude"
export CLAUDE_PROJECT_DIR="$PROJ"
export PROD_DEPLOY_GUARD_LOG="$TESTDIR/log/prod-deploy-guard.jsonl"

pass=0
fail=0
OUT=""
ERR=""
RC=0

ok() { pass=$((pass + 1)); }
ko() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; return 0; }

# payload <command> [tool_name] [permission_mode] [cwd]
payload() {
  jq -nc --arg c "$1" --arg t "${2:-Bash}" --arg m "${3:-default}" --arg w "${4:-/nonexistent-cwd}" \
    '{session_id:"test-session",cwd:$w,permission_mode:$m,hook_event_name:"PreToolUse",tool_name:$t,tool_input:{command:$c}}'
}

# run_hook <payload> [hook path] — sets OUT, ERR, RC.
run_hook() {
  local hook="${2:-$HOOK}"
  OUT=$(printf '%s' "$1" | "$hook" 2>"$TESTDIR/stderr")
  RC=$?
  ERR=$(cat "$TESTDIR/stderr")
}

decision_of() { printf '%s' "$OUT" | jq -r '.hookSpecificOutput.permissionDecision' 2>/dev/null; }

# expect_decision <ask|deny|none> <description> — judged on the last run_hook.
expect_decision() {
  local want="$1" desc="$2" got
  if [ "$RC" -ne 0 ]; then ko "$desc" "hook exited $RC (must always exit 0)"; return; fi
  if [ "$want" = "none" ]; then
    if [ -z "$OUT" ]; then ok; else ko "$desc" "expected no output, got: $OUT"; fi
    return
  fi
  if ! printf '%s' "$OUT" | jq -e --arg d "$want" \
      '.hookSpecificOutput.hookEventName == "PreToolUse" and .hookSpecificOutput.permissionDecision == $d
       and (.hookSpecificOutput.permissionDecisionReason | type == "string" and length > 0)' >/dev/null 2>&1; then
    got=$(decision_of)
    ko "$desc" "expected $want, got '${got:-<unparseable>}' from: $OUT"
    return
  fi
  ok
}

# check <ask|none> <description> <command> [tool] [mode]
check() {
  run_hook "$(payload "$3" "${4:-Bash}" "${5:-default}")"
  expect_decision "$1" "$2 [$3]"
}

set_project_json() { printf '%s' "$1" > "$PROJ/.claude/project.json"; }
clear_project_json() { rm -f "$PROJ/.claude/project.json"; }

log_lines() { if [ -f "$PROD_DEPLOY_GUARD_LOG" ]; then wc -l < "$PROD_DEPLOY_GUARD_LOG" | tr -d ' '; else printf '0'; fi; }

# decode <tsv command field> — `\\` -> `\`, `\n` -> newline, `\t` -> tab, left
# to right with no overlap (the fixture header documents the same contract).
decode() {
  local s="$1" bs=$'\\'
  s="${s//\\\\/$'\001'}"
  s="${s//\\n/$'\n'}"
  s="${s//\\t/$'\t'}"
  s="${s//$'\001'/$bs}"
  DECODED="$s"
}

# ── 1. the shared fixture: every row, defaults only ──────────────
clear_project_json
rows=0
while IFS=$'\t' read -r raw expect; do
  case "$raw" in ''|'#'*) continue ;; esac
  rows=$((rows + 1))
  decode "$raw"
  run_hook "$(payload "$DECODED")"
  expect_decision "$expect" "cases.tsv: $raw"
done < "$CASES"
if [ "$rows" -ge 20 ]; then ok; else ko "cases.tsv has >= 20 rows" "found $rows"; fi

# Required D1 rows are present in the fixture, with the required expectations.
for want in $'npm ci --production\tnone' $'yarn install --production\tnone' $'acme-cli deploy --prod\task'; do
  if grep -qxF "$want" "$CASES"; then ok; else ko "cases.tsv carries row: ${want//$'\t'/ -> }"; fi
done

# The reason names the pattern that matched, first match wins.
run_hook "$(payload 'npm run deploy:prod')"
if printf '%s' "$OUT" | jq -e '.hookSpecificOutput.permissionDecisionReason | contains("Bash(*deploy*prod*)")' >/dev/null 2>&1; then ok
else ko "reason names the matched pattern" "$OUT"; fi

# ── 2. project extras ─────────────────────────────────────────────
check none "no extra yet" './ship-it now'
set_project_json '{"approvals":{"prodDeployPatterns":["Bash(*ship-it*)"]}}'
check ask  "project extra Bash(*ship-it*)" './ship-it now'
check ask  "defaults still apply with extras" 'npm run deploy:prod'

# Extras are read from the payload cwd when CLAUDE_PROJECT_DIR is unset.
OUT=$(payload './ship-it now' Bash default "$PROJ" | env -u CLAUDE_PROJECT_DIR "$HOOK" 2>/dev/null); RC=$?
expect_decision ask "extras resolved from payload cwd without CLAUDE_PROJECT_DIR"

# Pattern metacharacters other than `*` are literal — `?`, `[`, `]`, `\` and the
# extglob openers. A matcher built on an escaped [[ == ]] glob gets these wrong.
set_project_json '{"approvals":{"prodDeployPatterns":["Bash(run?[x]*)","Bash(*+(prod)*)","Bash(*a\\b*)"]}}'
check ask  "literal ? and [x] in an extra" 'run?[x] go'
check none "? is not a single-char wildcard" 'runa x go'
check none "[x] is not a bracket class" 'run?x go'
check ask  "literal +( ) in an extra" 'echo +(prod) now'
check none "+(prod) is not an extglob" 'echo prodprod now'
check ask  "literal backslash in an extra" 'echo a\b'
check none "backslash is not an escape" 'echo ab'

# A reason containing quotes and backslashes is still valid JSON.
set_project_json '{"approvals":{"prodDeployPatterns":["Bash(*say \"hi\\*)"]}}'
check ask "extra with quote and backslash -> valid JSON" 'say "hi\ there'

# Malformed project.json: defaults still ask, extras ignored, one stderr line, exit 0.
set_project_json '{"approvals":{"prodDeployPatterns":["Bash(*ship-it*)"'
run_hook "$(payload 'deploy --prod')"
expect_decision ask "malformed project.json -> defaults still ask"
if printf '%s' "$ERR" | grep -q 'malformed'; then ok; else ko "malformed project.json -> stderr line" "stderr: $ERR"; fi
check none "malformed project.json -> extras ignored" './ship-it now'

# approvals.prodDeployPatterns with the wrong shape is ignored, never fatal.
set_project_json '{"approvals":"nope"}'
check ask  "approvals not an object -> defaults still ask" 'deploy --prod'

# Non-string entries are skipped; the string entries still count.
set_project_json '{"approvals":{"prodDeployPatterns":[42,null,{"a":1},["x"],true,"Bash(*ship-it*)"]}}'
check ask  "non-string entries skipped, string entry kept" './ship-it now'
check none "non-string entries never match" 'echo 42 true'

# D3: non-Bash entries and unparseable entries are ignored by this hook.
set_project_json '{"approvals":{"prodDeployPatterns":["Read(*)","mcp__deploy__run","*","Ba*sh(*x*)","Bash()"]}}'
check none "non-Bash / invalid extras ignored" 'echo x'
clear_project_json

# A bare `Bash` extra matches every command (same as the control plane).
set_project_json '{"approvals":{"prodDeployPatterns":["Bash"]}}'
check ask "bare Bash extra matches everything" 'ls'
clear_project_json

# ── 3. tool name and empty command ────────────────────────────────
check none "non-Bash tool_name -> no output" 'deploy --prod' Read
check none "MCP tool_name -> no output" 'deploy --prod' mcp__x__deploy
run_hook '{"tool_name":"Bash","tool_input":{}}'
expect_decision none "empty command -> no output"

# ── 4. fail closed: defaults file missing / empty ─────────────────
mkdir -p "$TESTDIR/broken/hooks"
cp "$HOOK" "$TESTDIR/broken/hooks/prod-deploy-guard.sh"
run_hook "$(payload 'ls')" "$TESTDIR/broken/hooks/prod-deploy-guard.sh"
expect_decision ask "defaults file missing -> ask on ls"
if printf '%s' "$OUT" | jq -e '.hookSpecificOutput.permissionDecisionReason | contains("misconfigured")' >/dev/null 2>&1; then ok
else ko "missing defaults reason says misconfigured" "$OUT"; fi

mkdir -p "$TESTDIR/broken/hooks/lib"
printf '# only a comment\n\n' > "$TESTDIR/broken/hooks/lib/prod-deploy-patterns.txt"
run_hook "$(payload 'ls')" "$TESTDIR/broken/hooks/prod-deploy-guard.sh"
expect_decision ask "defaults file with no pattern -> ask on ls"

# ── 5. fail-safe parsing: no jq / unparseable payload ─────────────
nojq="$TESTDIR/nojq-bin"
mkdir -p "$nojq"
for tool in cat tr date mkdir; do
  ln -s "$(command -v "$tool")" "$nojq/$tool"
done
OUT=$(payload 'deploy --prod' | PATH="$nojq" "$HOOK" 2>"$TESTDIR/stderr"); RC=$?
expect_decision ask "PATH without jq: deploy --prod still asks"
# A start-anchored default (`Bash(terraform apply*)`) must still match: in the
# raw payload the command sits after `{"session_id"…`, never at the start.
OUT=$(payload 'terraform apply -auto-approve' | PATH="$nojq" "$HOOK" 2>/dev/null); RC=$?
expect_decision ask "PATH without jq: anchored terraform apply still asks"
OUT=$(payload 'ls -la' | PATH="$nojq" "$HOOK" 2>/dev/null); RC=$?
expect_decision none "PATH without jq: ls -la stays silent"
set_project_json '{"approvals":{"prodDeployPatterns":["Bash(*ship-it*)"]}}'
OUT=$(payload 'ls -la' | PATH="$nojq" "$HOOK" 2>/dev/null); RC=$?
expect_decision none "PATH without jq: project extras need jq, defaults only"
clear_project_json

run_hook 'this is not json: deploy to prod'
expect_decision ask "unparseable payload -> raw-text match still asks"

# ── 6. mode table: every row -> its decision ──────────────────────
# Phase 1 measured (claude 2.1.291, -p): ask honoured over allow rules, auto and
# bypassPermissions. Interactive is the documented behaviour (unmeasured). No
# row resolves to deny.
for mode in default acceptEdits plan auto bypassPermissions dontAsk '' someFutureMode; do
  OUT=$(payload 'npm run deploy:prod' Bash "$mode" | env CLAUDE_CODE_ENTRYPOINT=cli "$HOOK" 2>/dev/null); RC=$?
  expect_decision ask "mode table: interactive / '${mode}' -> ask"
  OUT=$(payload 'npm run deploy:prod' Bash "$mode" |
    env CLAUDE_CODE_ENTRYPOINT=sdk-cli CLAUDE_CODE_SESSION_ATTENDED=0 "$HOOK" 2>/dev/null); RC=$?
  expect_decision ask "mode table: headless / '${mode}' -> ask"
done

# The headless reason tells the agent nobody can answer the prompt.
OUT=$(payload 'npm run deploy:prod' Bash bypassPermissions | env CLAUDE_CODE_SESSION_ATTENDED=0 "$HOOK" 2>/dev/null)
if printf '%s' "$OUT" | jq -e '.hookSpecificOutput.permissionDecisionReason | contains("headless")' >/dev/null 2>&1; then ok
else ko "headless reason mentions headless" "$OUT"; fi

# decision_for_mode itself, row by row (the hook is sourceable: functions only).
table=$(
  # shellcheck source=/dev/null
  . "$HOOK"
  for h in 0 1; do
    for m in default acceptEdits plan auto bypassPermissions dontAsk '' x; do
      printf '%s/%s=%s\n' "$h" "$m" "$(decision_for_mode "$m" "$h")"
    done
  done
)
if ! printf '%s\n' "$table" | grep -qv '=ask$'; then ok
else ko "decision_for_mode: every measured/documented row is ask" "$(printf '%s' "$table" | grep -v '=ask$' | tr '\n' ' ')"; fi

# ── 7. burn-in: deny-fallback is wired, warn emits ask, block emits deny ──
# No measured row resolves to deny, so the fallback is exercised by overriding
# decision_for_mode in a subshell that sources the hook.
before=$(log_lines)
OUT=$(
  # shellcheck source=/dev/null
  . "$HOOK"
  # shellcheck disable=SC2329  # called by the sourced hook's main()
  decision_for_mode() { printf 'deny'; }
  payload 'npm run deploy:prod' | main
)
RC=$?
expect_decision ask "deny-fallback in warn -> still ask"
if tail -n 1 "$PROD_DEPLOY_GUARD_LOG" 2>/dev/null | jq -e '.rule == "deny-fallback" and .decision == "warn"' >/dev/null 2>&1 &&
   [ "$(log_lines)" -eq $((before + 1)) ]; then ok
else ko "deny-fallback in warn logs deny-fallback/warn" "$(tail -n 1 "$PROD_DEPLOY_GUARD_LOG" 2>/dev/null)"; fi

OUT=$(
  # shellcheck source=/dev/null
  . "$HOOK"
  # shellcheck disable=SC2329  # called by the sourced hook's main()
  decision_for_mode() { printf 'deny'; }
  # shellcheck disable=SC2329  # called by the sourced hook's main()
  rule_mode() { printf 'block'; }
  payload 'npm run deploy:prod' | main
)
RC=$?
expect_decision deny "deny-fallback flipped to block -> deny"

# The ask rule is enforced from day one.
mode_ask=$(
  # shellcheck source=/dev/null
  . "$HOOK"
  printf '%s/%s' "$(rule_mode ask)" "$(rule_mode deny-fallback)"
)
if [ "$mode_ask" = "block/warn" ]; then ok; else ko "rule modes: ask=block, deny-fallback=warn" "got $mode_ask"; fi

# The hook can never emit allow.
if [ "$(grep -c '"allow"' "$HOOK")" -eq 0 ]; then ok; else ko "hook source never mentions \"allow\""; fi

# ── 8. burn-in log: one JSON line per match, none per non-match ───
rm -f "$PROD_DEPLOY_GUARD_LOG"
check ask  "log: match" 'npm run deploy:prod'
check none "log: non-match" 'ls -la'
long="kubectl --context prod apply $(printf 'x%.0s' $(seq 1 300))"
check ask  "log: long match" "$long"
if [ "$(log_lines)" -eq 2 ]; then ok; else ko "log gains one line per match (want 2)" "got $(log_lines)"; fi
if jq -e -s 'length == 2 and all(.[]; .hook == "prod-deploy-guard" and .rule == "ask" and .decision == "block"
      and .session == "test-session" and (.ts | test("^[0-9]{4}-")) and (.cmd | length <= 200))' \
      "$PROD_DEPLOY_GUARD_LOG" >/dev/null 2>&1; then ok
else ko "log records have the burn-in shape (cmd <= 200 chars)" "$(cat "$PROD_DEPLOY_GUARD_LOG" 2>/dev/null)"; fi

# A log that cannot be written never changes the decision.
OUT=$(payload 'npm run deploy:prod' | PROD_DEPLOY_GUARD_LOG=/dev/null/nope/log.jsonl "$HOOK" 2>/dev/null); RC=$?
expect_decision ask "unwritable log -> decision unchanged"

printf 'prod-deploy-guard: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
