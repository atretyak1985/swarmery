#!/bin/bash
# Production-deploy guard — PreToolUse hook on the Bash tool.
#
# A command that matches a production-deploy pattern gets a PreToolUse
# `permissionDecision: "ask"`, so the person at the terminal confirms it in the
# native permission dialog — even when an allow rule, auto mode or
# bypassPermissions would otherwise let it run without a prompt. Every other
# command gets no output at all, which leaves the normal permission flow alone.
#
# Patterns: the canonical defaults in lib/prod-deploy-patterns.txt, plus the
# project's own `approvals.prodDeployPatterns` from .claude/project.json. Same
# grammar as the control plane's approval rules: `Tool(argGlob)` or a bare
# `Tool`; `*` matches any run of characters (spaces, `/` and newlines too);
# everything else is literal; anchored at both ends; case-insensitive. Only
# `Bash` patterns apply here — other tools' entries are ignored by this hook.
#
# FAIL-SAFE DIRECTION: this hook never emits `allow`, and every failure leans
# towards asking:
#   - defaults file missing  → a broken install: ask on EVERY Bash call;
#   - project.json malformed → one stderr line, the defaults still apply;
#   - jq not on PATH         → match against the raw payload text, which still
#                              contains the command; every glob is unanchored
#                              (`*<glob>*`) there, so it over-matches on purpose.
#
# MODE TABLE (decision_for_mode). Measured with claude 2.1.291 in headless `-p`
# (scripts/tests/fixtures/prod-deploy-ask/matrix.json): `ask` is honoured over
# allow rules, auto and bypassPermissions, and in a headless run it resolves to
# a denial because nobody is there to answer. No mode was measured where `ask`
# fails to block, so no row resolves to `deny` today. Interactive sessions are
# not measured yet (the i-* legs are pending-manual); they get `ask`, which is
# the documented behaviour.
#
# BURN-IN (docs/GATE-HARDENING.md). Two rules, each with its own mode:
#   ask            block — enforced from day one; a false positive costs one
#                  local prompt.
#   deny-fallback  warn  — a mode row that resolves to `deny` logs
#                  `deny-fallback/warn` and still emits `ask`. Raise it to
#                  `block` only from a filled GATE-HARDENING row: a false deny
#                  breaks headless plan runs.
# Every match is logged to prod-deploy-guard.jsonl; logging can never change
# the decision.
#
# Runs under the hook's own shebang — /bin/bash 3.2 on macOS — so no bash-4
# builtins (`mapfile`, `${x,,}`) anywhere in this file.

set -uo pipefail

rule_mode() {
  case "$1" in
    ask)           printf 'block' ;;
    # No mode row resolves to deny yet; see the MODE TABLE note above.
    deny-fallback) printf 'warn' ;;
    # An unknown rule id is a bug in this file, not a licence to deny.
    *)             printf 'warn' ;;
  esac
}

LOG_BASENAME="prod-deploy-guard.jsonl"
LOG_CMD_MAX=200
PATTERN_RE='^([A-Za-z][A-Za-z0-9_-]*)(\((.+)\))?$'

HOOK_DIR="${BASH_SOURCE[0]%/*}"
[ "$HOOK_DIR" = "${BASH_SOURCE[0]}" ] && HOOK_DIR="."
DEFAULTS_FILE="$HOOK_DIR/lib/prod-deploy-patterns.txt"

have_jq=0
command -v jq >/dev/null 2>&1 && have_jq=1
raw_payload=0

# Payload fields, filled by main().
input=""
tool_name=""
command_text=""
session_id=""
hook_cwd=""
permission_mode=""

# lower <string> — ASCII-and-locale lower-casing that works on bash 3.2.
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

# trim <string>
trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

# json_str <string> — the string as a JSON string literal, without jq.
json_str() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  # Any other control character has no place in a reason or a log line.
  s=$(printf '%s' "$s" | tr -d '\000-\010\013\014\016-\037')
  printf '"%s"' "$s"
}

# glob_match <glob> <string> — a line-for-line port of globMatch in the control
# plane's approvals/rules.go: split on `*`, the first part is a prefix, middle
# parts are found left to right, the last part is a suffix of what remains.
# Every literal part is QUOTED inside the bash patterns, so `?`, `[`, `\` and
# the extglob openers `+( @( !( *( ?(` are plain text — escaping only some of
# them and handing the rest to `[[ == ]]` would not be the same matcher.
glob_match() {
  local pat="$1" s="$2" head rest mids mid last
  case "$pat" in
    *'*'*) ;;
    *) [ "$s" = "$pat" ]; return ;;
  esac
  head="${pat%%\**}"
  rest="${pat#*\*}"
  [[ "$s" == "$head"* ]] || return 1
  s="${s:${#head}}"
  last="${rest##*\*}"
  case "$rest" in
    *'*'*)
      mids="${rest%\**}"
      while :; do
        mid="${mids%%\**}"
        if [ -n "$mid" ]; then
          [[ "$s" == *"$mid"* ]] || return 1
          s="${s#*"$mid"}"
        fi
        case "$mids" in
          *'*'*) mids="${mids#*\*}" ;;
          *) break ;;
        esac
      done
      ;;
  esac
  [ -z "$last" ] || [[ "$s" == *"$last" ]]
}

# pattern_matches <pattern> <lower-cased arg> — Bash patterns only (D3). An
# unparseable pattern never matches, the same as the control plane skipping it.
pattern_matches() {
  local p tool inner
  p=$(trim "$1")
  [[ "$p" =~ $PATTERN_RE ]] || return 1
  tool="${BASH_REMATCH[1]}"
  inner="${BASH_REMATCH[3]}"
  [ "$tool" = "Bash" ] || return 1
  [ -z "${BASH_REMATCH[2]}" ] && return 0
  # Against the raw payload the command sits mid-string (after `{"session_id"…`),
  # so an anchored glob like `terraform apply*` would never match. Unanchor it:
  # `*<inner>*` is strictly broader, which is the direction this path must err.
  if [ "$raw_payload" -eq 1 ]; then
    glob_match "*$(lower "$inner")*" "$2"
    return
  fi
  glob_match "$(lower "$inner")" "$2"
}

# is_headless — measured in phase 1: every `-p` hook saw
# CLAUDE_CODE_ENTRYPOINT=sdk-cli and CLAUDE_CODE_SESSION_ATTENDED=0.
is_headless() {
  [ "${CLAUDE_CODE_SESSION_ATTENDED:-}" = "0" ] || [ "${CLAUDE_CODE_ENTRYPOINT:-}" = "sdk-cli" ]
}

# decision_for_mode <permission_mode> <headless 0|1> — ask | deny.
# Rows are phase 1's mode table; `deny` would mean "ask was measured as not
# blocking here", and no such row exists yet.
decision_for_mode() {
  local mode="$1" headless="$2"
  if [ "$headless" = "1" ]; then
    case "$mode" in
      # Measured, -p: ask honoured over allow rules and bypass; resolves to a denial.
      default|bypassPermissions|auto) printf 'ask' ;;
      # Not measured headless (acceptEdits, plan, dontAsk, …): ask.
      *) printf 'ask' ;;
    esac
  else
    case "$mode" in
      # Interactive: the documented behaviour; the i-* legs are pending-manual.
      default|acceptEdits|plan|auto|bypassPermissions|dontAsk) printf 'ask' ;;
      # Unknown or missing mode: ask.
      *) printf 'ask' ;;
    esac
  fi
}

# guard_log_file — same resolution as bash-shape-guard.sh: explicit override →
# the project's workspace metrics dir → $CLAUDE_PROJECT_DIR → a temp dir.
guard_log_file() {
  if [ -n "${PROD_DEPLOY_GUARD_LOG:-}" ]; then
    printf '%s' "$PROD_DEPLOY_GUARD_LOG"
  elif [ -n "${AGENT_PROJECT:-}" ]; then
    printf '%s/%s/workspace/metrics/%s' \
      "${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}" "$AGENT_PROJECT" "$LOG_BASENAME"
  elif [ -n "${CLAUDE_PROJECT_DIR:-}" ]; then
    printf '%s/.claude-workspace/metrics/%s' "${CLAUDE_PROJECT_DIR%/}" "$LOG_BASENAME"
  else
    printf '%s/swarmery-guard/%s' "${TMPDIR:-/tmp}" "$LOG_BASENAME"
  fi
}

# log_decision <rule> <warn|block> <pattern> — one JSON line per match. Built
# without jq (a missing jq must not lose the record), and every failure is
# swallowed: telemetry never changes the decision.
log_decision() {
  local rule="$1" decision="$2" pattern="$3" logfile dir
  logfile=$(guard_log_file)
  dir="${logfile%/*}"
  [ "$dir" = "$logfile" ] || mkdir -p "$dir" 2>/dev/null || return 0
  {
    printf '{"ts":%s,"hook":"prod-deploy-guard","rule":%s,"decision":%s,"pattern":%s,"session":%s,"cwd":%s,"cmd":%s}\n' \
      "$(json_str "$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)")" \
      "$(json_str "$rule")" "$(json_str "$decision")" "$(json_str "$pattern")" \
      "$(json_str "$session_id")" "$(json_str "$hook_cwd")" \
      "$(json_str "${command_text:0:$LOG_CMD_MAX}")"
  } >> "$logfile" 2>/dev/null || true
  return 0
}

# emit <ask|deny> <reason> — the only output this hook ever produces.
emit() {
  printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":%s,"permissionDecisionReason":%s}}\n' \
    "$(json_str "$1")" "$(json_str "$2")"
}

# decide <matched-pattern> — pick ask or deny for this session, log, emit.
decide() {
  local pattern="$1" headless=0 wanted rule mode decision reason
  is_headless && headless=1
  wanted=$(decision_for_mode "$permission_mode" "$headless")
  if [ "$wanted" = "deny" ]; then
    rule="deny-fallback"
  else
    rule="ask"
  fi
  mode=$(rule_mode "$rule")
  decision="ask"
  [ "$rule" = "deny-fallback" ] && [ "$mode" = "block" ] && decision="deny"
  log_decision "$rule" "$mode" "$pattern"

  if [ "$decision" = "deny" ]; then
    reason="Production deploy (matched $pattern) refused: this session cannot show a confirmation prompt. Run it from an interactive terminal."
  elif [ "$headless" = "1" ]; then
    reason="Production deploy (matched $pattern) — this needs a person to confirm it in an interactive terminal; a headless run cannot answer the prompt."
  else
    reason="Production deploy (matched $pattern) — confirm it here in this terminal."
  fi
  emit "$decision" "$reason"
}

# read_patterns <file> — one pattern per line on stdout; comments and blanks dropped.
read_patterns() {
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    line=$(trim "$line")
    [ -z "$line" ] && continue
    case "$line" in '#'*) continue ;; esac
    printf '%s\n' "$line"
  done < "$1"
}

# project_patterns — the project's approvals.prodDeployPatterns, strings only.
# A pattern spanning lines cannot parse as a rule pattern, so it is dropped
# here rather than split into two.
project_patterns() {
  local root file out
  root="${CLAUDE_PROJECT_DIR:-$hook_cwd}"
  [ -n "$root" ] || return 0
  file="${root%/}/.claude/project.json"
  [ -f "$file" ] || return 0
  if [ "$have_jq" -ne 1 ]; then
    printf 'prod-deploy-guard: jq not found; %s approvals.prodDeployPatterns ignored, defaults only\n' "$file" >&2
    return 0
  fi
  if ! out=$(jq -r '.approvals.prodDeployPatterns[]? | strings | select(test("\n") | not)' "$file" 2>/dev/null); then
    printf 'prod-deploy-guard: %s is unreadable or malformed; approvals.prodDeployPatterns ignored, defaults only\n' "$file" >&2
    return 0
  fi
  [ -n "$out" ] && printf '%s\n' "$out"
  return 0
}

main() {
  input=$(cat)

  # A payload jq cannot parse is handled like a missing jq, never as "no command".
  if [ "$have_jq" -eq 1 ] && tool_name=$(printf '%s' "$input" | jq -r '.tool_name // empty' 2>/dev/null); then
    command_text=$(printf '%s' "$input" | jq -r '.tool_input.command // empty' 2>/dev/null)
    session_id=$(printf '%s' "$input" | jq -r '.session_id // empty' 2>/dev/null)
    hook_cwd=$(printf '%s' "$input" | jq -r '.cwd // empty' 2>/dev/null)
    permission_mode=$(printf '%s' "$input" | jq -r '.permission_mode // empty' 2>/dev/null)
    # A payload that names another tool is not this hook's business. A missing
    # tool_name is treated as Bash: the hooks.json matcher already filtered.
    [ -n "$tool_name" ] && [ "$tool_name" != "Bash" ] && return 0
    [ -z "$command_text" ] && return 0
  else
    # No jq: the raw payload stands in for the command. Substring globs still
    # hit the JSON-escaped command; the extra fields can only add matches.
    printf 'prod-deploy-guard: jq unavailable or payload unparseable; matching against the raw hook payload\n' >&2
    tool_name=""
    raw_payload=1
    command_text="$input"
    [ -z "$command_text" ] && return 0
  fi

  # A defaults file that is absent, unreadable or holds no pattern at all is a
  # broken install — and a guard with nothing to match would silently allow.
  local defaults="" arg pattern
  [ -r "$DEFAULTS_FILE" ] && defaults=$(read_patterns "$DEFAULTS_FILE")
  if [ -z "$defaults" ]; then
    log_decision "ask" "$(rule_mode ask)" "(default pattern list missing)"
    emit "ask" "prod-deploy guard misconfigured: default pattern list missing ($DEFAULTS_FILE). Every Bash call needs confirmation until the core plugin is reinstalled."
    return 0
  fi

  arg=$(lower "$command_text")

  while IFS= read -r pattern; do
    if pattern_matches "$pattern" "$arg"; then
      decide "$pattern"
      return 0
    fi
  done < <(printf '%s\n' "$defaults"; project_patterns)

  return 0
}

# Run when executed; when sourced (the test suite does, to reach the mode table
# and the burn-in switch), only define the functions.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main
  exit 0
fi
