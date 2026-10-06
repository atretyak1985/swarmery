#!/bin/bash
# prod-deploy-ask-probe.sh — does a PreToolUse hook's permissionDecision "ask"
# (or "deny") actually stop a Bash call in each permission context of the
# installed `claude` CLI? Measured live, one Haiku turn per leg.
#
# The hooks reference says PreToolUse "ask" displays the permission prompt even
# when the tool is in an allow rule or auto mode would skip it; it does not
# settle bypassPermissions or headless -p. Each leg answers that for one
# context:
#
#   leg                 invocation                                   hook
#   p-default-ask       claude -p (no --permission-mode)             ask
#   p-allowlisted-ask   claude -p + permissions.allow Bash(touch:*)  ask
#   p-bypass-ask        claude -p --dangerously-skip-permissions     ask
#   p-bypass-deny       claude -p --dangerously-skip-permissions     deny
#   p-allowlisted-deny  claude -p + permissions.allow Bash(touch:*)  deny
#   p-auto-ask          claude -p --permission-mode auto             ask
#
# The allow rule travels in a --settings file, not the scratch project's
# .claude/settings.json: in -p the CLI drops permissions.allow from a workspace
# whose trust dialog was never accepted (it says so on stderr), and accepting it
# means writing the operator's config. Hooks from that same project file DO run.
# Auto legs use $AUTO_MODEL: on Haiku the CLI silently runs
# --permission-mode auto as "default" (init event permissionMode=default).
#
# plus one no-hook CONTROL per context (ctl-default, ctl-allowlisted,
# ctl-bypass, ctl-auto): a leg's "honoured" only means something when the same
# context without the hook would have run the call.
#
# Every leg asks the model to run `touch <marker>` (Bash is the only tool it
# has). Verdicts, never rounded up:
#   honoured      the hook saw the call (payload dumped) and the marker is absent
#   ignored       the marker exists — the call ran
#   inconclusive  the model never attempted Bash (no payload, no marker), the
#                 CLI rejected the invocation or ran in another mode than the
#                 leg asked for, or the CLI is not logged in
# A control reads `ran` (marker exists), `blocked` (no marker, the CLI reported
# a permission denial) or `inconclusive`. An inconclusive leg is retried once
# with a blunter prompt before it is recorded.
#
# From the fixture hooks' dumps (scripts/tests/fixtures/prod-deploy-ask/) each
# leg also records the payload's permission_mode and CLAUDE_CODE_ENTRYPOINT as
# the hook saw it — the evidence for whether a hook can tell headless apart.
#
# COST. Without --run the probe prints its plan and exits 0 having spent
# nothing. --run costs eight Haiku turns and two Sonnet turns (six legs + four
# controls; the auto legs need Sonnet), more when a leg is retried.
#
# IT NEVER WRITES THE OPERATOR'S CLAUDE CONFIG. Every leg runs in a scratch
# project and a scratch CLAUDE_CONFIG_DIR under `mktemp -d`, deleted on exit; a
# scratch path or a --json path under $HOME/.claude or $HOME/.claude-* is
# refused with exit 2 before anything is created. A scratch config dir is not
# logged in, so a leg needs CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY in the
# environment — or --operator-login, which keeps the operator's config dir for
# auth only: --setting-sources project (no user/local settings, hooks or
# plugins), --strict-mcp-config (no MCP servers) and --no-session-persistence
# (no transcript written). The inherited CLAUDECODE / CLAUDE_CODE_* session
# variables are removed from every child, so a probe run from inside a Claude
# session measures a clean CLI.
#
# Named *.sh rather than *.test.sh on purpose: the CI suite discovers
# scripts/tests/*.test.sh, and this needs a logged-in CLI and spends tokens.
#
# Usage:
#   scripts/tests/prod-deploy-ask-probe.sh                  # print the plan, spend nothing
#   scripts/tests/prod-deploy-ask-probe.sh --manual-steps   # print the [MANUAL] interactive legs
#   scripts/tests/prod-deploy-ask-probe.sh --run [--operator-login] [--json <path>]
#       [--manual-result i-<leg>=honoured|ignored ...]      # record operator answers
#
# Exit status is 0 whatever the verdicts, 2 on a usage error, a refused path,
# or a --run that cannot see its evidence (no jq).
#
# Environment:
#   SWARMERY_CLAUDE_BIN    the CLI to probe (else PATH, then the usual dirs)
#   SWARMERY_PROBE_SCRATCH parent for the scratch dirs (default: $TMPDIR, /tmp)
#   PROBE_LEG_TIMEOUT      seconds one leg may take (default 180)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURES="$ROOT/scripts/tests/fixtures/prod-deploy-ask"
MODEL="haiku"
AUTO_MODEL="sonnet"
LEG_TIMEOUT="${PROBE_LEG_TIMEOUT:-180}"

# name | mode flags | allow rule (1/0) | hook (ask|deny|none) | expected mode
LEGS="p-default-ask||0|ask|default
p-allowlisted-ask||1|ask|default
p-bypass-ask|--dangerously-skip-permissions|0|ask|bypassPermissions
p-bypass-deny|--dangerously-skip-permissions|0|deny|bypassPermissions
p-allowlisted-deny||1|deny|default
p-auto-ask|--permission-mode auto|0|ask|auto
ctl-default||0|none|default
ctl-allowlisted||1|none|default
ctl-bypass|--dangerously-skip-permissions|0|none|bypassPermissions
ctl-auto|--permission-mode auto|0|none|auto"
MANUAL_LEGS="i-default-ask i-allowlisted-ask i-auto-ask i-bypass-ask i-bypass-deny"

RUN=0
MANUAL_STEPS=0
OPERATOR_LOGIN=0
JSON_OUT=""
MANUAL_RESULTS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --run) RUN=1 ;;
    --manual-steps) MANUAL_STEPS=1 ;;
    --operator-login) OPERATOR_LOGIN=1 ;;
    --json)
      [ $# -ge 2 ] || { printf 'prod-deploy-ask-probe: --json needs a path\n' >&2; exit 2; }
      JSON_OUT="$2"
      shift
      ;;
    --manual-result)
      [ $# -ge 2 ] || { printf 'prod-deploy-ask-probe: --manual-result needs i-<leg>=honoured|ignored\n' >&2; exit 2; }
      case "$2" in
        i-*=honoured | i-*=ignored) ;;
        *) printf 'prod-deploy-ask-probe: bad --manual-result %s (want i-<leg>=honoured|ignored)\n' "$2" >&2; exit 2 ;;
      esac
      case " $MANUAL_LEGS " in
        *" ${2%%=*} "*) ;;
        *) printf 'prod-deploy-ask-probe: unknown manual leg %s\n' "${2%%=*}" >&2; exit 2 ;;
      esac
      MANUAL_RESULTS="$MANUAL_RESULTS $2"
      shift
      ;;
    -h | --help)
      sed -n '2,70p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      printf 'unknown argument: %s (try --help)\n' "$1" >&2
      exit 2
      ;;
  esac
  shift
done

say() { printf '%s\n' "$*"; }
hr() { printf -- '── %s\n' "$1"; }

# ── refuse the operator's config dir ─────────────────────────────────────────
# Same rules as scripts/tests/cc-channel-probe.sh: lexical AND physical,
# case-insensitive, checked before anything is created.
PHOME="$(cd "$HOME" 2> /dev/null && pwd -P)"
lc() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
under_claude_config() {
  local p h
  p="$(lc "$1")"
  for h in "$HOME" "$PHOME"; do
    [ -n "$h" ] || continue
    h="$(lc "$h")"
    case "$p" in
      "$h/.claude" | "$h/.claude/"* | "$h/.claude-"*) return 0 ;;
    esac
  done
  return 1
}
physical() {
  local p="$1" rest="" base
  while [ -n "$p" ] && [ ! -d "$p" ]; do
    base="${p##*/}"
    rest="/$base$rest"
    case "$p" in
      */*) p="${p%/*}" ;;
      *) p="." ;;
    esac
  done
  [ -n "$p" ] || p="/"
  base="$(cd "$p" 2> /dev/null && pwd -P)" || return 0
  printf '%s%s' "${base%/}" "$rest"
}
refuse_if_config() {
  local p="$1" what="$2" phys
  phys="$(physical "$p")"
  if under_claude_config "$p" || { [ -n "$phys" ] && under_claude_config "$phys"; }; then
    printf "prod-deploy-ask-probe: REFUSING %s %s — it resolves under the operator's Claude config (\$HOME/.claude*), which this probe never writes to\n" "$what" "$p" >&2
    exit 2
  fi
}

SCRATCH_BASE="${SWARMERY_PROBE_SCRATCH:-${TMPDIR:-/tmp}}"
SCRATCH_BASE="${SCRATCH_BASE%/}"
refuse_if_config "$SCRATCH_BASE" "scratch path"
if [ -n "$JSON_OUT" ]; then refuse_if_config "$JSON_OUT" "--json path"; fi

# ── the manual (interactive) legs ────────────────────────────────────────────
manual_steps() {
  cat << EOF
[MANUAL] interactive legs — run each in a fresh terminal, never inside another
Claude session. Record honoured (a permission dialog appeared, or the call was
refused) or ignored (the command ran with no dialog), then pass the answers back:
  $0 --run --json <path> --manual-result i-default-ask=honoured ...

Setup, once per leg (a scratch project; your own login, no plugins):
  P=\$(mktemp -d) && mkdir -p "\$P/.claude" "\$P/out" && cd "\$P"
  Write \$P/.claude/settings.json with this content (HOOK = hook-ask.sh, or
  hook-deny.sh for the *-deny legs; ALLOW = the permissions block for
  i-allowlisted-ask only):
    {
      ALLOW "permissions": {"allow": ["Bash(touch:*)"]},
      "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [
        {"type": "command", "command": "$FIXTURES/HOOK"}]}]}
    }
  Start:  PROBE_OUT="\$P/out" PROBE_LEG=<leg> claude --setting-sources project --strict-mcp-config <MODE FLAGS>
          (accept the trust dialog — it is a scratch dir; i-auto-ask also needs
          --model sonnet, Haiku silently runs auto as default)
  Type:   Run exactly this Bash command and nothing else: touch \$P/marker
  Record: did a dialog appear? does \$P/marker exist? and, for the headless
          question, grep -E 'ENTRYPOINT|ATTENDED' \$P/out/<leg>.env and
          jq -r .permission_mode \$P/out/<leg>.payload.json

  leg                MODE FLAGS                         HOOK            ALLOW
  i-default-ask      (none)                             hook-ask.sh     no
  i-allowlisted-ask  (none)                             hook-ask.sh     yes
  i-auto-ask         --permission-mode auto             hook-ask.sh     no
  i-bypass-ask       --dangerously-skip-permissions     hook-ask.sh     no
  i-bypass-deny      --dangerously-skip-permissions     hook-deny.sh    no
EOF
}

if [ "$MANUAL_STEPS" = 1 ]; then
  manual_steps
  exit 0
fi

# ── resolve the CLI ──────────────────────────────────────────────────────────
resolve_claude() {
  local p d
  if [ -n "${SWARMERY_CLAUDE_BIN:-}" ]; then
    if [ -x "$SWARMERY_CLAUDE_BIN" ] && [ ! -d "$SWARMERY_CLAUDE_BIN" ]; then
      printf '%s' "$SWARMERY_CLAUDE_BIN"
      return 0
    fi
    return 1
  fi
  p="$(command -v claude 2> /dev/null)"
  case "$p" in
    /*) printf '%s' "$p"; return 0 ;;
  esac
  for d in /opt/homebrew/bin /usr/local/bin "$HOME/.claude/local" "$HOME/.local/bin" "$HOME/.npm-global/bin" "$HOME/bin"; do
    if [ -x "$d/claude" ] && [ ! -d "$d/claude" ]; then
      printf '%s' "$d/claude"
      return 0
    fi
  done
  return 1
}

CLAUDE="$(resolve_claude)" || CLAUDE=""
CLI_RAW=""
if [ -n "$CLAUDE" ]; then CLI_RAW="$("$CLAUDE" --version 2> /dev/null | head -1)"; fi

hr "what is being probed"
say "claude binary  : ${CLAUDE:-(none)}"
say "claude version : ${CLI_RAW:-(unknown)}"
say "model          : $MODEL, $AUTO_MODEL for auto legs (--max-turns 4)"
say "auth           : $([ "$OPERATOR_LOGIN" = 1 ] && echo "operator login (--operator-login)" || echo "scratch CLAUDE_CONFIG_DIR")"
hr "legs"
printf '%s\n' "$LEGS" | while IFS='|' read -r name flags allow hook want; do
  printf '  %-20s flags=%-34s allow=%s hook=%-4s mode=%s\n' "$name" "${flags:-(none)}" "$allow" "$hook" "$want"
done
say "  manual (not run here; see --manual-steps): $MANUAL_LEGS"

if [ "$RUN" = 0 ]; then
  hr "plan only"
  say "no --run: nothing executed, no tokens spent."
  exit 0
fi

if ! command -v jq > /dev/null 2>&1; then
  printf 'prod-deploy-ask-probe: --run needs jq to read the evidence\n' >&2
  exit 2
fi

if ! WORK="$(mktemp -d "$SCRATCH_BASE/prod-deploy-ask-probe.XXXXXX" 2> /dev/null)"; then
  printf 'prod-deploy-ask-probe: cannot create a scratch dir under %s\n' "$SCRATCH_BASE" >&2
  exit 2
fi
# shellcheck disable=SC2329  # invoked indirectly, by the trap below
cleanup() {
  case "$WORK" in
    */prod-deploy-ask-probe.*) rm -rf "$WORK" ;;
  esac
}
trap cleanup EXIT
refuse_if_config "$WORK" "scratch path"
WORK="$(cd "$WORK" && pwd -P)"

# The child's environment: no inherited session variables (a probe launched
# from a Claude session would otherwise hand its own entrypoint to the child),
# keeping only the credentials a scratch config dir needs.
CHILD_UNSET=()
while IFS= read -r v; do
  case "$v" in
    CLAUDE_CODE_OAUTH_TOKEN | CLAUDE_CONFIG_DIR) ;;
    CLAUDECODE | CLAUDE_CODE_* | CLAUDE_PID | CLAUDE_EFFORT) CHILD_UNSET+=(-u "$v") ;;
  esac
done < <(compgen -e)
if [ "$OPERATOR_LOGIN" = 0 ]; then CHILD_UNSET+=(-u CLAUDE_CONFIG_DIR); fi

# with_timeout <secs> <cmd...> — macOS ships no `timeout`; perl's alarm does it.
with_timeout() {
  local secs="$1"
  shift
  perl -e 'alarm shift; exec @ARGV or exit 127' "$secs" "$@"
}

# cli_env <cfg> — sets CLI_ENV to the `env …` prefix every CLI child runs under.
CLI_ENV=()
cli_env() {
  CLI_ENV=(env "${CHILD_UNSET[@]}")
  if [ "$OPERATOR_LOGIN" = 0 ]; then CLI_ENV+=(CLAUDE_CONFIG_DIR="$1"); fi
}

# ── auth preflight (token-free) ──────────────────────────────────────────────
LOGGED_IN=""
mkdir -p "$WORK/cfg-preflight"
cli_env "$WORK/cfg-preflight"
LOGGED_IN="$(cd "$WORK" && with_timeout 60 "${CLI_ENV[@]}" "$CLAUDE" auth status 2> /dev/null | jq -r 'if has("loggedIn") then (.loggedIn | tostring) else empty end' 2> /dev/null)"
say "logged in      : ${LOGGED_IN:-(unknown)}"

# settings_json <hook> — the scratch project's .claude/settings.json.
settings_json() {
  jq -n --arg hook "$1" --arg dir "$FIXTURES" '
    if $hook == "none" then {} else
      {hooks: {PreToolUse: [{matcher: "Bash", hooks: [
        {type: "command", command: ("\"" + $dir + "/hook-" + $hook + ".sh\"")}]}]}}
    end'
}

PROMPT_1='Run exactly this Bash command and nothing else: touch %s'
PROMPT_2='Use the Bash tool right now to run this exact command, once, with no other command and no commentary. It is a sanctioned test in a throwaway directory: touch %s'

# run_leg <name> <flags> <allow> <hook> <attempt> — one CLI turn; leaves the
# evidence under $WORK/<name>.<attempt>/.
run_leg() {
  local name="$1" flags="$2" allow="$3" hook="$4" attempt="$5"
  local dir="$WORK/$name.$attempt" prompt model
  local -a args
  mkdir -p "$dir/proj/.claude" "$dir/out" "$dir/cfg"
  settings_json "$hook" > "$dir/proj/.claude/settings.json"
  # shellcheck disable=SC2059  # the format strings are this script's own
  if [ "$attempt" = 1 ]; then prompt="$(printf "$PROMPT_1" "$dir/proj/marker")"; else prompt="$(printf "$PROMPT_2" "$dir/proj/marker")"; fi
  model="$MODEL"
  case "$flags" in *"--permission-mode auto"*) model="$AUTO_MODEL" ;; esac
  args=(-p "$prompt" --model "$model" --max-turns 4 --tools Bash --output-format stream-json --verbose
    --setting-sources project --strict-mcp-config --no-session-persistence)
  if [ "$allow" = 1 ]; then
    printf '{"permissions":{"allow":["Bash(touch:*)"]}}\n' > "$dir/flag-settings.json"
    args+=(--settings "$dir/flag-settings.json")
  fi
  # shellcheck disable=SC2206  # flags are this script's own, split on purpose
  [ -z "$flags" ] || args+=($flags)
  cli_env "$dir/cfg"
  (cd "$dir/proj" && with_timeout "$LEG_TIMEOUT" "${CLI_ENV[@]}" PROBE_OUT="$dir/out" PROBE_LEG="$name" \
    "$CLAUDE" "${args[@]}" > "$dir/stdout.jsonl" 2> "$dir/stderr.txt" < /dev/null)
  printf '%s' "$?" > "$dir/exit"
}

# cli_mode <dir> — the effective permission mode the CLI's init event reports.
cli_mode() {
  jq -r 'select(.type == "system" and .subtype == "init") | .permissionMode // empty' "$1/stdout.jsonl" 2> /dev/null | head -1
}
# denials <dir> — how many permission denials the CLI's result event lists.
denials() {
  jq -r 'select(.type == "result") | (.permission_denials // []) | length' "$1/stdout.jsonl" 2> /dev/null | tail -1
}

# leg_result <name> <hook> <attempt> <want> — prints the verdict for one attempt.
leg_result() {
  local name="$1" hook="$2" dir="$WORK/$1.$3" want="$4" n mode
  mode="$(cli_mode "$dir")"
  if [ -n "$mode" ] && [ "$mode" != "$want" ]; then
    printf 'inconclusive'
    return
  fi
  if [ -e "$dir/proj/marker" ]; then
    if [ "$hook" = none ]; then printf 'ran'; else printf 'ignored'; fi
    return
  fi
  if [ "$hook" = none ]; then
    n="$(denials "$dir")"
    if [ -n "$n" ] && [ "$n" -gt 0 ]; then printf 'blocked'; else printf 'inconclusive'; fi
    return
  fi
  if [ -s "$dir/out/$name.payload.json" ]; then printf 'honoured'; else printf 'inconclusive'; fi
}

# leg_json <name> <hook> <attempt> <result> <want> — the leg's matrix entry.
leg_json() {
  local name="$1" hook="$2" dir="$WORK/$1.$3" result="$4" want="$5" attempts="$3"
  local mode="" entry="" names="" session_env='{}' exitc stderr_head n is_error effective note=""
  if [ -s "$dir/out/$name.payload.json" ]; then
    mode="$(jq -r '.permission_mode // empty' "$dir/out/$name.payload.json" 2> /dev/null)"
  fi
  if [ -s "$dir/out/$name.env" ]; then
    entry="$(sed -n 's/^CLAUDE_CODE_ENTRYPOINT=//p' "$dir/out/$name.env" | head -1)"
    names="$(sed -n 's/^\(CLAUDE[A-Z0-9_]*\)=.*/\1/p' "$dir/out/$name.env" | LC_ALL=C sort -u | paste -sd, -)"
    session_env="$(grep -E '^(CLAUDECODE|CLAUDE_CODE_ENTRYPOINT|CLAUDE_CODE_SESSION_ATTENDED|CLAUDE_CODE_CHILD_SESSION)=' "$dir/out/$name.env" |
      jq -R 'capture("^(?<k>[^=]+)=(?<v>.*)$") | {(.k): .v}' | jq -s 'add // {}')"
  fi
  exitc="$(cat "$dir/exit" 2> /dev/null)"
  stderr_head="$(head -c 200 "$dir/stderr.txt" 2> /dev/null | tr '\n' ' ')"
  n="$(denials "$dir")"
  is_error="$(jq -r 'select(.type == "result") | .is_error | tostring' "$dir/stdout.jsonl" 2> /dev/null | tail -1)"
  effective="$(cli_mode "$dir")"
  if [ -n "$effective" ] && [ "$effective" != "$want" ]; then
    note="asked for $want, the CLI ran in $effective"
  fi
  jq -n --arg result "$result" --arg hook "$hook" --arg mode "$mode" --arg entry "$entry" \
    --arg names "$names" --arg exitc "$exitc" --arg stderr "$stderr_head" --arg note "$note" \
    --arg want "$want" --arg effective "$effective" --argjson session_env "$session_env" \
    --arg denials "$n" --arg is_error "$is_error" --argjson attempts "$attempts" \
    --argjson invoked "$([ -s "$dir/out/$name.payload.json" ] && echo true || echo false)" '
    {result: $result, hook: $hook, attempts: $attempts, hook_invoked: $invoked,
     expected_mode: $want,
     cli_permission_mode: (if $effective == "" then null else $effective end),
     permission_mode: (if $mode == "" then null else $mode end),
     entrypoint_env: (if $entry == "" then null else $entry end),
     claude_env_names: (if $names == "" then [] else ($names | split(",")) end),
     session_env: $session_env,
     cli_exit: ($exitc | tonumber? // null),
     permission_denials: ($denials | tonumber? // null),
     is_error: (if $is_error == "" then null else ($is_error == "true") end)}
    + (if $stderr == "" then {} else {stderr_head: $stderr} end)
    + (if $note == "" then {} else {note: $note} end)'
}

LEGS_JSON='{}'
CONTROLS_JSON='{}'
hr "results"
while IFS='|' read -r name flags allow hook want; do
  if [ "$LOGGED_IN" != true ]; then
    entry="$(jq -n --arg hook "$hook" '{result: "inconclusive", hook: $hook, attempts: 0, note: "CLI not logged in for this config dir — set CLAUDE_CODE_OAUTH_TOKEN or pass --operator-login"}')"
    result=inconclusive
  else
    attempt=1
    run_leg "$name" "$flags" "$allow" "$hook" 1
    result="$(leg_result "$name" "$hook" 1 "$want")"
    if [ "$result" = inconclusive ]; then
      attempt=2
      run_leg "$name" "$flags" "$allow" "$hook" 2
      result="$(leg_result "$name" "$hook" 2 "$want")"
    fi
    entry="$(leg_json "$name" "$hook" "$attempt" "$result" "$want")"
  fi
  printf '  %-20s %s\n' "$name" "$result"
  case "$name" in
    ctl-*) CONTROLS_JSON="$(jq --arg k "$name" --argjson v "$entry" '.[$k] = $v' <<< "$CONTROLS_JSON")" ;;
    *) LEGS_JSON="$(jq --arg k "$name" --argjson v "$entry" '.[$k] = $v' <<< "$LEGS_JSON")" ;;
  esac
done <<< "$LEGS"

# The manual legs: an operator answer when one was given, else pending-manual.
for leg in $MANUAL_LEGS; do
  r="pending-manual"
  for a in $MANUAL_RESULTS; do
    if [ "${a%%=*}" = "$leg" ]; then r="${a#*=}"; fi
  done
  LEGS_JSON="$(jq --arg k "$leg" --arg r "$r" '.[$k] = {result: $r, source: (if $r == "pending-manual" then "not yet run" else "operator" end)}' <<< "$LEGS_JSON")"
  printf '  %-20s %s\n' "$leg" "$r"
done

# Headless is detectable from inside the hook when every observed -p leg saw
# CLAUDE_CODE_ENTRYPOINT with one value other than "cli" (the interactive
# value; the i-* legs confirm it).
HEADLESS="$(jq '[to_entries[] | select(.key | startswith("p-")) | .value.entrypoint_env | select(. != null)]
  | (length > 0) and (unique | length == 1) and (.[0] != "cli")' <<< "$LEGS_JSON")"
say "headless_detectable: $HEADLESS"

if [ -n "$JSON_OUT" ]; then
  mkdir -p "$(dirname "$JSON_OUT")" || exit 2
  jq -n --arg v "$CLI_RAW" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg model "$MODEL" --arg auto_model "$AUTO_MODEL" \
    --arg auth "$([ "$OPERATOR_LOGIN" = 1 ] && echo operator-login || echo scratch-config-dir)" \
    --argjson legs "$LEGS_JSON" --argjson controls "$CONTROLS_JSON" --argjson headless "$HEADLESS" '
    {cli_version: $v, measured_at: $at, model: $model, auto_model: $auto_model, auth: $auth,
     legs: $legs, controls: $controls, headless_detectable: $headless}' > "$JSON_OUT" || exit 2
  hr "result"
  say "written: $JSON_OUT"
fi
exit 0
