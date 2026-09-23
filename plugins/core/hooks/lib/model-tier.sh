#!/bin/bash
# model-tier.sh — compare two Claude model names by FAMILY and GENERATION.
# Sourced, never executed. No subprocesses: post-tool-observe.sh sources this on
# every tool call.
#
# WHY IT EXISTS. Three hooks asked "did we end up on a weaker model than we asked
# for?" and all three answered it with a string compare. The requested side is an
# ALIAS (`opus`, `sonnet`) and the observed side is an ID (`claude-opus-5-5`), so
# `opus != claude-opus-5-5` was true on essentially every Agent dispatch and the
# activity log filled with ModelFallback events for runs that got exactly the
# model they asked for. A signal that fires on the happy path is not a signal.
#
# THE RULE. A fallback is a move to a WEAKER model, and weaker has two axes:
#   - a lower family tier (opus → sonnet → haiku), and
#   - within one family, an OLDER generation (opus 5.5 → opus 4.1).
# Anything else — the same tier, a newer generation, a rename, an unrecognised
# name on either side — is silence. Unknown is not evidence of a downgrade, and
# this file's entire purpose is to stop claiming it is.
#
# bash 3.2 ONLY (/bin/bash on macOS): no ${v,,}, no declare -A, no mapfile.
# scripts/tests/portable-shell.test.sh enforces that.

# model_tier_current_gen <family> — the generation of that family's CURRENT
# release, scaled the way model_rank scales one (5.5 → 55).
#
# Needed because an alias carries no generation: `opus` means "whatever opus is
# today", and without a value for that, a move onto a RETIRED opus generation
# cannot be told from a move onto the current one. A table is the only honest
# way to hold it (and this comment names no retired id on purpose — the one
# scripts/validate-agent-refs.sh forbids under plugins/**) — the
# alternative is to treat every alias as satisfied by any generation, which is
# the bug one door down from the one this file fixes.
#
# It goes stale by design, and one test keeps it honest:
# scripts/tests/model-tier.test.sh asserts the opus entry against the newest opus
# row in tools/swarmery/config/pricing.json, which is the file the cost layer
# already forces someone to update on a model cutover. Override per machine with
# SWARMERY_MODEL_CURRENT_GENS="opus=55 sonnet=50" if you are ahead of the table.
model_tier_current_gen() {
  local family="$1" pair
  for pair in ${SWARMERY_MODEL_CURRENT_GENS:-}; do
    case "$pair" in
      "$family"=*) printf '%s' "${pair#*=}"; return 0 ;;
    esac
  done
  case "$family" in
    opus)   printf '55' ;;
    fable)  printf '50' ;;
    mythos) printf '50' ;;
    sonnet) printf '50' ;;
    haiku)  printf '45' ;;
    *)      printf '0'  ;;
  esac
}

# model_rank <name> — sets MODEL_FAMILY, MODEL_TIER, MODEL_GEN, MODEL_GEN_ASSUMED.
#
#   MODEL_TIER        0 when the family is unrecognised; higher is stronger.
#   MODEL_GEN         major*10 + minor (5.5 → 55, 5 → 50, 4-1 → 41); 0 unknown.
#   MODEL_GEN_ASSUMED 1 when MODEL_GEN came from the alias table, not the name.
#
# Accepts every spelling seen in the wild: bare aliases (`opus`), ids
# (`claude-opus-5-5`), date-suffixed ids (`claude-haiku-4-5-20251001`), the fast
# SKU (`claude-opus-5-5-fast`), the context-window marker (`claude-opus-5-5[1m]`
# — the same bracket that is a longest-prefix hazard in config/pricing.json), a
# vendor path (`anthropic/claude-opus-5-5`) and the pre-5 word order
# (`claude-3-5-sonnet-20241022`).
model_rank() {
  MODEL_FAMILY=""
  MODEL_TIER=0
  MODEL_GEN=0
  MODEL_GEN_ASSUMED=0
  local m="$1"
  [ -n "$m" ] || return 0

  m="${m%%[*}"      # drop a trailing context-window marker
  m="${m##*/}"      # drop a vendor path prefix
  m="${m#claude-}"

  case "$m" in
    *opus*)   MODEL_FAMILY="opus";   MODEL_TIER=3 ;;
    *fable*)  MODEL_FAMILY="fable";  MODEL_TIER=3 ;;
    *mythos*) MODEL_FAMILY="mythos"; MODEL_TIER=3 ;;
    *sonnet*) MODEL_FAMILY="sonnet"; MODEL_TIER=2 ;;
    *haiku*)  MODEL_FAMILY="haiku";  MODEL_TIER=1 ;;
    *) return 0 ;;
  esac

  # A DATE IS NOT A VERSION, and this is the half of the rule the regex alone
  # cannot express. A dated opus 4 id — the retired spelling this file may not
  # write out, since scripts/validate-agent-refs.sh forbids it under plugins/**:
  # the family word, `-4-`, then an 8-digit date — matched the first branch with
  # major=4 and the DATE as its minor, scaling to a number in the millions. So a
  # genuine fallback onto a dated model compared as NEWER than the model it fell
  # back from, and logged nothing. internal/modelid's scale() rejects the same shapes by LENGTH: a
  # major is at most two digits and positive, a minor is exactly one. The two
  # implementations must agree id for id; that is the whole point of the pair.
  #
  # Both spellings are tried in modelid's order — digits AFTER the family word
  # (`opus-5-5`), then, only if that yields nothing usable, digits BEFORE it
  # (`3-5-sonnet`). The fallthrough is load-bearing: `claude-3-5-sonnet-20241022`
  # matches the first pattern on its DATE, which is rejected, and the real
  # version is in the second.
  local major="" minor="" pattern
  for pattern in "$MODEL_FAMILY-([0-9]+)(-([0-9]+))?" "([0-9]+)(-([0-9]+))?-$MODEL_FAMILY"; do
    [[ "$m" =~ $pattern ]] || continue
    major="${BASH_REMATCH[1]}"
    minor="${BASH_REMATCH[3]}"
    # `10#` forces base 10: a zero-padded field is a number, not octal.
    [ "${#major}" -le 2 ] && [ "$((10#$major))" -gt 0 ] || major=""
    [ "${#minor}" -eq 1 ] || minor=""
    [ -n "$major" ] && break
    minor=""
  done

  if [ -n "$major" ]; then
    MODEL_GEN=$(( 10#$major * 10 + 10#${minor:-0} ))
  else
    MODEL_GEN="$(model_tier_current_gen "$MODEL_FAMILY")"
    MODEL_GEN_ASSUMED=1
  fi
  return 0
}

# model_is_downgrade <requested> <observed> — exit 0 when observed is WEAKER.
#
# Every ambiguous case answers "no". A hook that logs on doubt trains its reader
# to ignore it, and the one downstream consumer (the routing report) counts these
# events as if each were a real fallback.
model_is_downgrade() {
  local req_tier req_gen obs_tier obs_gen
  model_rank "$1"; req_tier="$MODEL_TIER"; req_gen="$MODEL_GEN"
  model_rank "$2"; obs_tier="$MODEL_TIER"; obs_gen="$MODEL_GEN"

  [ "$req_tier" -gt 0 ] && [ "$obs_tier" -gt 0 ] || return 1
  [ "$obs_tier" -lt "$req_tier" ] && return 0
  [ "$obs_tier" -gt "$req_tier" ] && return 1
  [ "$req_gen" -gt 0 ] && [ "$obs_gen" -gt 0 ] && [ "$obs_gen" -lt "$req_gen" ] && return 0
  return 1
}

# model_is_upgrade <from> <to> — exit 0 only when `to` is positively STRONGER.
#
# NOT the negation of model_is_downgrade, and the pair is not redundant: both
# answer "no" whenever a name is unrecognised, a generation is unknown, or
# `from` is empty. That shared silence is the point — the two callers need
# OPPOSITE defaults on doubt, and a single function can only have one.
#
#   post-tool-observe must not CLAIM a fallback it cannot prove  → asks _downgrade
#   pre-model-switch must not BLOCK a switch it cannot prove is
#   an operator moving up onto something unvalidated              → asks _upgrade
#
# Asking `! model_is_downgrade` for the second question is what stranded
# sessions: a missing `from_model`, an id neither side knows, or an unreadable
# copy of this very file all answered "not a downgrade", and the gate read that
# as licence to block.
model_is_upgrade() {
  local from_tier from_gen to_tier to_gen
  model_rank "$1"; from_tier="$MODEL_TIER"; from_gen="$MODEL_GEN"
  model_rank "$2"; to_tier="$MODEL_TIER"; to_gen="$MODEL_GEN"

  [ "$from_tier" -gt 0 ] && [ "$to_tier" -gt 0 ] || return 1
  [ "$to_tier" -gt "$from_tier" ] && return 0
  [ "$to_tier" -lt "$from_tier" ] && return 1
  [ "$from_gen" -gt 0 ] && [ "$to_gen" -gt 0 ] && [ "$to_gen" -gt "$from_gen" ] && return 0
  return 1
}
