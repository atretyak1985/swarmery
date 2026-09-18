#!/bin/bash
# agent-routing-guard.sh — PreToolUse hook on the Agent tool.
#
# Search-shaped work dispatched to `general-purpose` is the single most expensive
# shape in the fleet. In the retro window 2026-09-01 → 2026-09-14 the
# `general-purpose` agent took $155.03 of $215.48 total agent spend, with a p95
# runtime of 16 484 s (4 h 35 m) and an error rate stuck at 46 %. `Explore` is
# the read-only search agent for exactly this work: it reads excerpts rather than
# whole files, so it answers "where is / which files / what uses" for a fraction
# of the cost.
#
# WHY A GATE AND NOT MORE PROSE. Text alone is what has already failed.
# plugins/core/agents/tech-lead.md has said "Пошук по кодовій базі ширший за
# один відомий файл — це `Explore` або `@researcher`" for the whole window, and
# the spend happened anyway. A rules file is read at session start and forgotten
# by dispatch time; this hook arrives AT dispatch time, which is also the moment
# someone is writing the brief — the only moment at which the step-ceiling rule
# can still change what gets written.
#
# `general-purpose` has no editable definition file in this marketplace, so its
# prompt cannot be amended. Its DISPATCH, however, is fully inspectable: the
# PreToolUse payload carries `.tool_input.subagent_type` and
# `.tool_input.description`, which is what architecture-freshness.sh — the other
# hook on this matcher — already reads. So the discipline can be a gate.
#
# WHAT FIRES. `subagent_type` (plugin prefix stripped) is `general-purpose` AND
# the description is search-shaped. The vocabulary is architecture-freshness.sh's
# `looks_like_research()` verbatim, extended with this retro's own phrasing
# (list / what uses / перелічити / знайти / зрозуміти де). One vocabulary for
# both hooks is one thing to maintain; two would drift.
#
# ONCE PER SESSION, AND ONLY ONCE. Same contract as architecture-freshness.sh and
# for the same reason: a gate that can fire repeatedly can deadlock a run, while
# one that fires once costs at most a turn. If the dispatch genuinely is not a
# search, the agent dispatches again and this hook stands aside.
#
# Kill switch: SWARMERY_AGENT_ROUTING=0.
# Contract: the decision goes to STDERR (only stderr reaches the model); every
# non-matching path exits 0 silently.
#
# WARN-MODE BURN-IN (gate-hardening rule 1). New rule, no burn-in data, so it
# ships in `warn`: the message still reaches the model, the exit code does not
# refuse the dispatch, and every decision is counted. See docs/GATE-HARDENING.md —
# the flip is argued from counted hits and a false-positive review, never from a
# date.

# ── enforcement, per rule ─────────────────────────────────────────
# One line per rule, same spelling as bash-shape-guard.sh: `warn` logs and
# allows, `block` logs and refuses. Raise it ONLY from a filled row in
# docs/GATE-HARDENING.md.
rule_mode() {
  case "$1" in
    gp-search-routing) printf 'warn' ;;
    # An unknown rule id is a bug in this file, not a licence to block.
    *)                 printf 'warn' ;;
  esac
}
ENFORCE_FROM="2026-10-15"   # review deadline; see docs/GATE-HARDENING.md

# Burn-in log: one JSON record per decision, read with
# `scripts/guard-hits.sh --log <path>`.
LOG_BASENAME="agent-routing-guard.jsonl"
LOG_DESC_MAX=200

set -uo pipefail

# Drain stdin FIRST, before any early exit. A hook that exits without reading
# its payload leaves the writer holding a closed pipe: the caller takes SIGPIPE
# and, under `pipefail`, that failure becomes the pipeline's exit status. The
# kill switch is supposed to be invisible, so it must not be able to fail the
# very dispatch it is standing aside from.
input=$(cat)

[ "${SWARMERY_AGENT_ROUTING:-1}" = "0" ] && exit 0

# A missing tool must never block work.
command -v jq >/dev/null 2>&1 || exit 0

agent_type=$(printf '%s' "$input" | jq -r '.tool_input.subagent_type // .tool_input.type // empty' 2>/dev/null)
description=$(printf '%s' "$input" | jq -r '.tool_input.description // empty' 2>/dev/null)
session_id=$(printf '%s' "$input" | jq -r '.session_id // empty' 2>/dev/null)
hook_cwd=$(printf '%s' "$input" | jq -r '.cwd // empty' 2>/dev/null)

# Nothing to judge — a payload without an agent type, or without a brief, is not
# this hook's business.
[ -z "$agent_type" ] && exit 0
[ -z "$description" ] && exit 0

# ── guard_log_file / log_decision / refuse ────────────────────────
# Deliberately the same shapes as bash-shape-guard.sh, so scripts/guard-hits.sh
# reads this log unchanged (`--log <path>`) and the operator learns one format.

# A hook that hard-codes a log path stops working the moment it is installed
# anywhere else: explicit override (tests, operators) → the project's workspace
# metrics dir → $CLAUDE_PROJECT_DIR → a temp dir. The write always lands.
guard_log_file() {
  if [ -n "${AGENT_ROUTING_GUARD_LOG:-}" ]; then
    printf '%s' "$AGENT_ROUTING_GUARD_LOG"
  elif [ -n "${AGENT_PROJECT:-}" ]; then
    printf '%s/%s/workspace/metrics/%s' \
      "${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}" "$AGENT_PROJECT" "$LOG_BASENAME"
  elif [ -n "${CLAUDE_PROJECT_DIR:-}" ]; then
    printf '%s/.claude-workspace/metrics/%s' "${CLAUDE_PROJECT_DIR%/}" "$LOG_BASENAME"
  else
    printf '%s/swarmery-guard/%s' "${TMPDIR:-/tmp}" "$LOG_BASENAME"
  fi
}

# log_decision <rule-id> <warn|block> — append one record, and never let that
# append change what the hook does. Telemetry is strictly secondary to the
# allow-or-refuse contract: a read-only directory or a full disk must leave the
# exit code and the stderr text byte-identical.
log_decision() {
  local rule="$1" decision="$2" logfile dir
  logfile=$(guard_log_file)
  dir=$(dirname "$logfile")
  mkdir -p "$dir" 2>/dev/null || return 0
  jq -cn \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg rule "$rule" \
    --arg decision "$decision" \
    --arg session "$session_id" \
    --arg cwd "$hook_cwd" \
    --arg agent "$agent_type" \
    --arg desc "${description:0:$LOG_DESC_MAX}" \
    '{ts:$ts,hook:"agent-routing-guard",rule:$rule,decision:$decision,session:$session,cwd:$cwd,agent:$agent,desc:$desc}' \
    >> "$logfile" 2>/dev/null || true
  return 0
}

# refuse <rule-id> <headline> [extra lines…] — emit the decision and leave.
# In warn mode this still exits 0; the text is identical either way, so the
# burn-in log shows exactly what enforcement would have said.
refuse() {
  local rule="$1"; shift
  local mode exit_code
  mode=$(rule_mode "$rule")
  if [ "$mode" = "block" ]; then exit_code=2; else exit_code=0; fi
  log_decision "$rule" "$mode"
  if [ "$exit_code" -eq 0 ]; then
    printf '⚠️  WARN (enforce from %s): [%s] %s\n' "$ENFORCE_FROM" "$rule" "$1" >&2
  else
    printf '🚫 BLOCKED: [%s] %s\n' "$rule" "$1" >&2
  fi
  shift
  local line
  for line in "$@"; do
    printf '%s\n' "$line" >&2
  done
  exit "$exit_code"
}

# ── is this a general-purpose dispatch? ───────────────────────────
# Matched with any plugin prefix stripped (`core:general-purpose` →
# `general-purpose`), because the same agent is spawned under both spellings.
is_general_purpose() {
  case "${1##*:}" in
    general-purpose) return 0 ;;
  esac
  return 1
}

# ── is the brief search-shaped? ───────────────────────────────────
# architecture-freshness.sh:85-89's vocabulary verbatim, plus this retro's own
# phrasing. Keep the two in step: a second, drifting vocabulary is a second
# thing to maintain and a second thing to be wrong.
looks_like_research() {
  printf '%s' "$1" | grep -Eqi '(^|[[:space:]])(explore|investigate|survey|audit|map|locate|trace|list|find (out|where|every|all)|search (for|across)|where (is|are|does)|which files|what uses|understand|перелічити|знайти|зрозуміти,?([[:space:]]+де)?)([[:space:]]|$)'
}

is_general_purpose "$agent_type" || exit 0
looks_like_research "$description" || exit 0

# ── say it, once ──────────────────────────────────────────────────
# The marker is what makes this a one-turn cost rather than a trap. Written
# BEFORE the message, so even a session that ignores it proceeds on its next
# attempt. No session id means no way to tell a first dispatch from a tenth, and
# a gate that cannot tell must not fire at all.
[ -n "$session_id" ] || exit 0
safe_session=$(printf '%s' "$session_id" | tr -c 'A-Za-z0-9_.-' '_')
marker="${TMPDIR:-/tmp}/swarmery-agent-routing/${safe_session}"
[ -f "$marker" ] && exit 0
mkdir -p "$(dirname "$marker")" 2>/dev/null || exit 0
: > "$marker" 2>/dev/null || exit 0

refuse "gp-search-routing" \
  "this looks like a search, dispatched to general-purpose." \
  "" \
  "\`Explore\` is the read-only search agent: it reads excerpts rather than whole" \
  "files, so it answers \"where is / which files / what uses\" for a fraction of the" \
  "cost. Re-dispatch with subagent_type \"Explore\" and state the search breadth" \
  "(\"medium\" or \"very thorough\")." \
  "" \
  "If this genuinely is not a search, dispatch general-purpose again — this gate" \
  "fires once per session and will not stop you twice. When you do, the brief must" \
  "carry both: an explicit completion criterion (\"done when X exists / X answers Y\")" \
  "and a step ceiling (\"at most N tool calls; if you hit it, report what you have" \
  "and stop\"). In the last retro window general-purpose ran to a p95 of 4 h 35 m" \
  "with a 46 % error rate — an unbounded brief is what that looks like from inside."
