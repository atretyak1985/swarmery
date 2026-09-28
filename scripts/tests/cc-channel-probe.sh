#!/bin/bash
# cc-channel-probe.sh — which configuration channels does the installed `claude`
# CLI honour for a PLUGIN-SHIPPED .mcp.json? Three facts, measured live:
#
#   G1  `pluginConfigs` (read through ${user_config.KEY}) is honoured only from
#       userSettings (<configDir>/settings.json) and flagSettings (--settings);
#       project and local copies are written to disk and silently ignored.
#   G2  an unset ${VAR} survives LITERALLY into the server's argv, and the CLI
#       says so only in its debug log: a WARN `Missing environment variables in
#       plugin MCP config` plus an `mcp-config-invalid` diagnostic. The same run
#       sets the variable in the parent env as the positive control (rule 1).
#   G3  a settings `env` block expands ${VAR} only from the `user` setting source
#       — never from project or local — while a --settings file's env block
#       expands on every --setting-sources value.
#
# Each fact prints one of:
#   pass          every observation matches the baseline
#                 (tools/swarmery/internal/channelprobe/baseline.json).
#   drift         at least one observation was MADE and disagrees with it. The
#                 CLI moved; re-read the plan that rests on the fact.
#   inconclusive  an observation could not be made — no CLI, no detail line, no
#                 debug log, no jq to read the baseline. Never rounded up.
#
# Exit status is 0 when nothing drifted (pass or inconclusive), 1 when any fact
# drifted, 2 on a usage error or a refused scratch/output path — a probe that cannot
# see its evidence must not be mistaken for one that saw the good outcome.
#
# HOW IT OBSERVES, AND WHY IT COSTS NOTHING. The fixture plugin
# (scripts/tests/fixtures/cc-channel-probe/plugin) ships one stdio server whose
# argv carries three markers, and `claude mcp list` prints a server's command and
# args as its detail line. Substitution is therefore readable without a model
# turn, without a login and without spending a token. There is no paid mode: if
# a future CLI stops printing that line, the facts go inconclusive, and adding a
# `-p` observation is a deliberate follow-up, not a fallback this script takes.
#
# IT NEVER TOUCHES THE OPERATOR'S CLAUDE CONFIG. Every run happens in a scratch
# project and a scratch CLAUDE_CONFIG_DIR under `mktemp -d`, deleted on exit; a
# scratch path or a --json output dir that resolves under $HOME/.claude or
# $HOME/.claude-* (compared case-insensitively) is refused with exit 2 before
# anything is created. The two probe variables are removed
# from the child's environment, and nothing the operator's environment holds is
# printed or stored: the markers are this script's own fixed strings, and a
# result records booleans.
#
# Named *.sh rather than *.test.sh on purpose: the CI suite discovers
# scripts/tests/*.test.sh, and this needs a CLI that CI does not have.
#
# Usage:
#   scripts/tests/cc-channel-probe.sh           # measure and print a report
#   scripts/tests/cc-channel-probe.sh --json    # also write the result file
#
# Environment:
#   SWARMERY_CLAUDE_BIN    the CLI to probe (else PATH, then the usual dirs)
#   SWARMERY_PROBE_SCRATCH parent for the scratch dirs (default: $TMPDIR, /tmp)
#   SWARMERY_PROBES_DIR    where --json writes <cliVersion>.json
#                          (default: ~/.swarmery/probes; dir 0700, file 0600)
#
# The human record of the last measurement, and the contract for re-running it
# when the CLI changes: tools/swarmery/docs/claude-cli-config-channels.md.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PLUGIN_DIR="$ROOT/scripts/tests/fixtures/cc-channel-probe/plugin"
BASELINE="$ROOT/tools/swarmery/internal/channelprobe/baseline.json"

WRITE_JSON=0
while [ $# -gt 0 ]; do
  case "$1" in
    --json) WRITE_JSON=1 ;;
    -h | --help)
      sed -n '2,59p' "${BASH_SOURCE[0]}"
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
# Checked lexically AND physically (a symlinked $HOME or scratch path must not
# slip through), case-insensitively (APFS resolves $HOME/.Claude to
# $HOME/.claude, and `pwd -P` keeps the spelling it was given), and BEFORE
# mktemp or mkdir, so a refused path is never created in.
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
# physical <path> — the path with its deepest EXISTING ancestor resolved by
# `pwd -P`, so a not-yet-created dir under a symlink is judged by where it would
# really land. Prints nothing when no ancestor resolves.
physical() {
  local p="$1" rest="" base
  while [ -n "$p" ] && [ ! -d "$p" ]; do
    base="${p##*/}"
    rest="/$base$rest"
    case "$p" in
      */*) p="${p%/*}" ;;
      *) p="." ;; # a relative path's last component: resolve against the cwd
    esac
  done
  [ -n "$p" ] || p="/"
  base="$(cd "$p" 2> /dev/null && pwd -P)" || return 0
  printf '%s%s' "${base%/}" "$rest"
}
# refuse_if_config <path> <what> — exit 2 when <path> lands in $HOME/.claude*.
refuse_if_config() {
  local p="$1" what="$2" phys
  phys="$(physical "$p")"
  if under_claude_config "$p" || { [ -n "$phys" ] && under_claude_config "$phys"; }; then
    printf "cc-channel-probe: REFUSING %s %s — it resolves under the operator's Claude config (\$HOME/.claude*), which this probe never writes to\n" "$what" "$p" >&2
    exit 2
  fi
}

SCRATCH_BASE="${SWARMERY_PROBE_SCRATCH:-${TMPDIR:-/tmp}}"
SCRATCH_BASE="${SCRATCH_BASE%/}"
refuse_if_config "$SCRATCH_BASE" "scratch path"
# The --json output dir is judged up front too: a refused probes dir must never
# be mkdir'd, and a run that cannot keep its result should not be made at all.
PROBES_DIR="${SWARMERY_PROBES_DIR:-$HOME/.swarmery/probes}"
PROBES_DIR="${PROBES_DIR%/}"
if [ "$WRITE_JSON" = 1 ]; then refuse_if_config "$PROBES_DIR" "probes dir (SWARMERY_PROBES_DIR)"; fi
if ! WORK="$(mktemp -d "$SCRATCH_BASE/cc-channel-probe.XXXXXX" 2> /dev/null)"; then
  printf 'cc-channel-probe: cannot create a scratch dir under %s\n' "$SCRATCH_BASE" >&2
  exit 2
fi
# shellcheck disable=SC2329  # invoked indirectly, by the trap below
cleanup() {
  case "$WORK" in
    */cc-channel-probe.*) rm -rf "$WORK" ;;
  esac
}
trap cleanup EXIT
refuse_if_config "$WORK" "scratch path"

# ── resolve the CLI ──────────────────────────────────────────────────────────
# Same order as tools/swarmery/internal/claudebin: the override, PATH, then the
# common install dirs. An override that does not exist is a miss, NOT a reason
# to fall back — it names the binary the operator asked about.
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

CLAUDE=""
CLI_RAW=""
CLI_VERSION=""
WHY_BLIND=""
if CLAUDE="$(resolve_claude)"; then
  CLI_RAW="$("$CLAUDE" --version 2> /dev/null | head -1)"
  CLI_VERSION="$(printf '%s\n' "$CLI_RAW" | sed -n 's/^\([0-9][0-9A-Za-z.+-]*\).*/\1/p')"
  if [ -z "$CLI_VERSION" ]; then
    WHY_BLIND="\`$CLAUDE --version\` printed no version"
  fi
else
  CLAUDE=""
  WHY_BLIND="no claude CLI could be resolved"
fi

CAN_COMPARE=1
WHY_NO_COMPARE=""
if ! command -v jq > /dev/null 2>&1; then
  CAN_COMPARE=0
  WHY_NO_COMPARE="jq not found — the baseline cannot be read"
elif [ ! -r "$BASELINE" ]; then
  CAN_COMPARE=0
  # Repo-relative, so a deep checkout cannot push the note past the result's
  # string limit.
  WHY_NO_COMPARE="baseline not readable at ${BASELINE#"$ROOT"/}"
fi

hr "what is being probed"
say "claude binary  : ${CLAUDE:-(none)}"
say "claude version : ${CLI_RAW:-(unknown)}"
say "fixture plugin : $PLUGIN_DIR"
say "method         : claude mcp list detail line (token-free)"

# ── one observation ──────────────────────────────────────────────────────────
# observe <rung> <setting-sources> <parent-marker> [debug-file]
# Fresh scratch config + project for every call, so no rung leaks into the next.
# Sets DETAIL to the fixture server's `mcp list` line; returns 1 when that line
# is absent (the observation could not be made).
MARK_PREFIX="swarmery-probe-rung-"
PARENT_MARKER="swarmery-probe-parent-marker"
# The three references exactly as the fixture's .mcp.json spells them — what the
# detail line shows when the CLI left one unexpanded.
# shellcheck disable=SC2016  # literal on purpose: these must NOT expand here
LIT_PARENT='${SWARMERY_PROBE_PARENT}'
# shellcheck disable=SC2016
LIT_SETTINGS_ENV='${SWARMERY_PROBE_SETTINGS_ENV}'
# shellcheck disable=SC2016
LIT_USER_CONFIG='${user_config.probe_marker}'
DETAIL=""
payload() {
  printf '{"pluginConfigs":{"probe-pack@inline":{"options":{"probe_marker":"%s%s"}}},"env":{"SWARMERY_PROBE_SETTINGS_ENV":"%s%s"}}\n' \
    "$MARK_PREFIX" "$1" "$MARK_PREFIX" "$1"
}
observe() {
  local rung="$1" sources="$2" parent="$3" debug="${4:-}"
  local cfg="$WORK/cfg" proj="$WORK/proj" out
  local args=(--plugin-dir "$PLUGIN_DIR")
  DETAIL=""
  rm -rf "$cfg" "$proj" "$WORK/flag-settings.json"
  mkdir -p "$cfg" "$proj/.claude" || return 1
  case "$rung" in
    user) payload user > "$cfg/settings.json" ;;
    project) payload project > "$proj/.claude/settings.json" ;;
    local) payload local > "$proj/.claude/settings.local.json" ;;
    flag)
      payload flag > "$WORK/flag-settings.json"
      args+=(--settings "$WORK/flag-settings.json")
      ;;
  esac
  if [ -n "$sources" ]; then args+=(--setting-sources "$sources"); fi
  if [ -n "$debug" ]; then args+=(--debug-file "$debug"); fi
  if [ -n "$parent" ]; then
    out="$(cd "$proj" && env -u SWARMERY_PROBE_SETTINGS_ENV SWARMERY_PROBE_PARENT="$parent" \
      CLAUDE_CONFIG_DIR="$cfg" "$CLAUDE" "${args[@]}" mcp list 2>&1 < /dev/null)"
  else
    out="$(cd "$proj" && env -u SWARMERY_PROBE_PARENT -u SWARMERY_PROBE_SETTINGS_ENV \
      CLAUDE_CONFIG_DIR="$cfg" "$CLAUDE" "${args[@]}" mcp list 2>&1 < /dev/null)"
  fi
  DETAIL="$(printf '%s\n' "$out" | grep -F 'plugin:probe-pack:probe:' | head -1)"
  [ -n "$DETAIL" ]
}

# field <name> — the value of `<name>=` in DETAIL; returns 1 when the field is absent.
field() {
  case "$DETAIL" in
    *" $1="*) ;;
    *) return 1 ;;
  esac
  printf '%s\n' "$DETAIL" | sed -n "s/.* $1=\([^ ]*\).*/\1/p"
}

# Observations live in OBS_<fact>_<key> = true | false | "" (unseen).
set_obs() { printf -v "OBS_$1_$2" '%s' "$3"; }
get_obs() {
  local n="OBS_$1_$2"
  printf '%s' "${!n:-}"
}

# classify <got> <want-marker> <literal> — true when the rung's marker arrived,
# false when the reference stayed literal or empty, "" for anything else (a
# value this probe did not write cannot be read as either answer).
classify() {
  if [ "$1" = "$2" ]; then
    printf 'true'
  elif [ "$1" = "$3" ] || [ -z "$1" ]; then
    printf 'false'
  fi
}

RUNG_KEYS="user project local flag user_under_project_local project_under_project_local local_under_project_local flag_under_project_local"
G2_KEYS="literal_survives warn_missing_env diag_mcp_config_invalid parent_expands parent_expands_under_project_local"

if [ -n "$CLAUDE" ] && [ -z "$WHY_BLIND" ]; then
  # G1 + G3 share runs: every run carries both a pluginConfigs and an env block
  # in exactly one rung, with and without --setting-sources project,local.
  for sources in "" "project,local"; do
    for rung in user project local flag; do
      key="$rung"
      if [ -n "$sources" ]; then key="${rung}_under_project_local"; fi
      if observe "$rung" "$sources" ""; then
        uc="$(field userconfig)" && set_obs G1 "$key" "$(classify "$uc" "$MARK_PREFIX$rung" "$LIT_USER_CONFIG")"
        se="$(field settingsenv)" && set_obs G3 "$key" "$(classify "$se" "$MARK_PREFIX$rung" "$LIT_SETTINGS_ENV")"
      fi
    done
  done

  # G2: the parent variable unset, the debug log captured.
  DEBUG_LOG="$WORK/debug.log"
  if observe none "" "" "$DEBUG_LOG"; then
    pv="$(field parent)"
    if [ "$pv" = "$LIT_PARENT" ]; then
      set_obs G2 literal_survives true
    elif [ -z "$pv" ]; then
      set_obs G2 literal_survives false
    fi
  fi
  if [ -s "$DEBUG_LOG" ]; then
    if grep -qF 'Missing environment variables in plugin MCP config' "$DEBUG_LOG"; then
      set_obs G2 warn_missing_env true
    else
      set_obs G2 warn_missing_env false
    fi
    if grep -qF 'mcp-config-invalid' "$DEBUG_LOG"; then
      set_obs G2 diag_mcp_config_invalid true
    else
      set_obs G2 diag_mcp_config_invalid false
    fi
  fi
  # Positive control for rule 1: the parent environment expands everywhere.
  if observe none "" "$PARENT_MARKER"; then
    pv="$(field parent)" && set_obs G2 parent_expands "$(classify "$pv" "$PARENT_MARKER" "$LIT_PARENT")"
  fi
  if observe none "project,local" "$PARENT_MARKER"; then
    pv="$(field parent)" && set_obs G2 parent_expands_under_project_local "$(classify "$pv" "$PARENT_MARKER" "$LIT_PARENT")"
  fi
fi

# ── verdicts ─────────────────────────────────────────────────────────────────
# drift wins over unseen: an observation that was MADE and disagrees is evidence,
# whatever else could not be seen. Otherwise any unseen observation, or no way
# to read the baseline, is inconclusive.
DRIFTED=0
verdict_for() {
  local fact="$1" keys="$2" k got want unseen=0 drift=0
  for k in $keys; do
    got="$(get_obs "$fact" "$k")"
    if [ -z "$got" ]; then
      unseen=1
      continue
    fi
    [ "$CAN_COMPARE" = 1 ] || continue
    want="$(jq -r --arg f "$fact" --arg k "$k" '.facts[$f].observed[$k] | if . == null then "" else tostring end' "$BASELINE" 2> /dev/null)"
    if [ -z "$want" ]; then
      unseen=1
    elif [ "$got" != "$want" ]; then
      drift=1
    fi
  done
  if [ "$drift" = 1 ]; then
    printf 'drift'
  elif [ "$unseen" = 1 ] || [ "$CAN_COMPARE" = 0 ]; then
    printf 'inconclusive'
  else
    printf 'pass'
  fi
}
note_for() {
  local fact="$1" v="$2"
  if [ -n "$WHY_BLIND" ]; then
    printf '%s' "$WHY_BLIND"
    return
  fi
  if [ "$CAN_COMPARE" = 0 ]; then
    printf '%s' "$WHY_NO_COMPARE"
    return
  fi
  case "$v" in
    drift) printf 'observations disagree with the embedded baseline' ;;
    inconclusive) printf 'at least one observation could not be made' ;;
    *) jq -r --arg f "$fact" '.facts[$f].note // ""' "$BASELINE" 2> /dev/null ;;
  esac
}

report_fact() {
  local fact="$1" title="$2" keys="$3" k got shown
  hr "$fact — $title"
  for k in $keys; do
    got="$(get_obs "$fact" "$k")"
    case "$got" in
      true) shown="yes" ;;
      false) shown="no" ;;
      *) shown="(unseen)" ;;
    esac
    printf '  %-36s : %s\n' "$k" "$shown"
  done
}

report_fact G1 "pluginConfigs tiers (\${user_config.probe_marker} substituted)" "$RUNG_KEYS"
report_fact G2 "unset \${VAR} and the parent environment" "$G2_KEYS"
report_fact G3 "settings env block tiers (\${SWARMERY_PROBE_SETTINGS_ENV} expanded)" "$RUNG_KEYS"

V_G1="$(verdict_for G1 "$RUNG_KEYS")"
V_G2="$(verdict_for G2 "$G2_KEYS")"
V_G3="$(verdict_for G3 "$RUNG_KEYS")"
N_G1="$(note_for G1 "$V_G1")"
N_G2="$(note_for G2 "$V_G2")"
N_G3="$(note_for G3 "$V_G3")"

hr "verdict"
printf 'G1  %-12s  %s\n' "$V_G1" "$N_G1"
printf 'G2  %-12s  %s\n' "$V_G2" "$N_G2"
printf 'G3  %-12s  %s\n' "$V_G3" "$N_G3"
for v in "$V_G1" "$V_G2" "$V_G3"; do
  if [ "$v" = drift ]; then DRIFTED=1; fi
done

# ── the result file ──────────────────────────────────────────────────────────
# Built by hand so --json needs nothing jq-shaped at write time. Every string is
# this script's own (a fixed note, a version, the CLI's path) and is escaped;
# the only per-run data are booleans.
#
# channelprobe.Load refuses any string over 200 BYTES (maxStringBytes in
# tools/swarmery/internal/channelprobe/redact.go), and a note that names a path
# — the CLI's, in "`<path> --version` printed no version" — grows with the
# install dir. clip shortens such a string, one character at a time, to at most
# CLIP_BYTES and appends "...", so no run can write a file Load then rejects.
# The 12-byte slack covers a C locale, where a "character" is a byte and the cut
# can split a multibyte one: Go decodes each stray byte as U+FFFD (3 bytes).
MAX_STR_BYTES=200
CLIP_BYTES=$((MAX_STR_BYTES - 12))
nbytes() { printf '%s' "$1" | LC_ALL=C wc -c | tr -d ' '; }
clip() {
  local s="$1"
  if [ "$(nbytes "$s")" -le "$MAX_STR_BYTES" ]; then
    printf '%s' "$s"
    return
  fi
  s="${s:0:$CLIP_BYTES}"
  while [ "$(nbytes "$s")" -gt "$CLIP_BYTES" ]; do
    s="${s%?}"
  done
  printf '%s...' "$s"
}
json_str() {
  local s
  s="$(clip "$1")"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  printf '"%s"' "$s"
}
json_fact() {
  local fact="$1" keys="$2" verdict="$3" note="$4" k got first=1
  printf '    %s: {\n      "verdict": %s,\n      "observed": {' "$(json_str "$fact")" "$(json_str "$verdict")"
  for k in $keys; do
    got="$(get_obs "$fact" "$k")"
    [ -n "$got" ] || continue
    if [ "$first" = 1 ]; then first=0; else printf ','; fi
    printf '\n        %s: %s' "$(json_str "$k")" "$got"
  done
  if [ "$first" = 0 ]; then printf '\n      '; fi
  printf '},\n      "note": %s\n    }' "$(json_str "$note")"
}

if [ "$WRITE_JSON" = 1 ]; then
  # PROBES_DIR was set, and refused if it lands in $HOME/.claude*, up front.
  name="$CLI_VERSION"
  case "$name" in
    '' | *..* | *[!0-9A-Za-z._-]*) name="unknown" ;;
  esac
  umask 077
  if ! mkdir -p "$PROBES_DIR" || ! chmod 700 "$PROBES_DIR"; then
    printf 'cc-channel-probe: cannot prepare %s\n' "$PROBES_DIR" >&2
    exit 2
  fi
  tmp="$(mktemp "$PROBES_DIR/.result.XXXXXX")" || exit 2
  {
    printf '{\n'
    printf '  "schema": 1,\n'
    printf '  "cliVersion": %s,\n' "$(json_str "$CLI_VERSION")"
    printf '  "cliVersionRaw": %s,\n' "$(json_str "$CLI_RAW")"
    printf '  "cliPath": %s,\n' "$(json_str "$CLAUDE")"
    printf '  "measuredAt": %s,\n' "$(json_str "$(date -u +%Y-%m-%dT%H:%M:%SZ)")"
    printf '  "host": %s,\n' "$(json_str "$(uname -s) $(uname -m)")"
    printf '  "method": %s,\n' "$(json_str "claude mcp list detail line (token-free)")"
    printf '  "facts": {\n'
    json_fact G1 "$RUNG_KEYS" "$V_G1" "$N_G1"
    printf ',\n'
    json_fact G2 "$G2_KEYS" "$V_G2" "$N_G2"
    printf ',\n'
    json_fact G3 "$RUNG_KEYS" "$V_G3" "$N_G3"
    printf '\n  }\n}\n'
  } > "$tmp"
  chmod 600 "$tmp"
  if ! mv -f "$tmp" "$PROBES_DIR/$name.json"; then
    rm -f "$tmp"
    printf 'cc-channel-probe: cannot write %s\n' "$PROBES_DIR/$name.json" >&2
    exit 2
  fi
  hr "result"
  say "written: $PROBES_DIR/$name.json"
fi

exit "$DRIFTED"
