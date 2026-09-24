#!/usr/bin/env bash
# Effort sweep (plan step 7.2/7.3) on a Claude subscription: every pass maps the
# suite's `opus-tier` label to `claude -p` at one effort, so a case's own
# `providers:` filter is untouched. The judge also runs through `claude -p`.
#
#   bash evals/sweep/run.sh <out-dir> [efforts...]     # default: low medium high
#
# Writes <out-dir>/<pass>.json (promptfoo results) and prints nothing else;
# summarise with evals/sweep/summarise.py.
set -euo pipefail

out=${1:?usage: run.sh <out-dir> [efforts...]}
shift
efforts=("$@")
[ ${#efforts[@]} -eq 0 ] && efforts=(low medium high)
here=$(cd "$(dirname "$0")" && pwd)
evals=$(dirname "$here")
mkdir -p "$out"

# The agents 7.2 names, plus the 11.4 forecast cases and the 7.3 debugger cases.
# security-auditor is swept at medium/high only; architect has no suite yet.
# SUITES="a b" re-runs a subset (e.g. after fixing a case).
suites_for() {
  if [ -n "${SUITES:-}" ]; then
    printf '%s\n' $SUITES
    return
  fi
  local s=(tech-lead planner code-reviewer implementation-agent forecast debugger)
  [ "$1" != low ] && s+=(security-auditor)
  printf '%s\n' "${s[@]}"
}

write_config() { # <file> <opus-tier model> <effort> <suite...>
  local file=$1 model=$2 effort=$3
  shift 3
  {
    echo "description: effort sweep $model/$effort"
    echo "prompts:"
    echo "  - file://$evals/prompts/agent-chat.json"
    echo "providers:"
    echo "  - id: file://$evals/providers/claude-cli.js"
    echo "    label: opus-tier"
    echo "    config: { model: $model, effort: $effort }"
    echo "  - id: file://$evals/providers/claude-cli.js"
    echo "    label: haiku-tier"
    echo "    config: { model: claude-haiku-4-5-20251001 }"
    echo "defaultTest:"
    echo "  options:"
    echo "    provider:"
    echo "      id: file://$evals/providers/claude-cli.js"
    echo "      config: { model: claude-sonnet-5 }"
    echo "tests:"
    for s in "$@"; do echo "  - file://$evals/agents/$s.yaml"; done
  } >"$file"
}

run_pass() { # <name> <model> <effort> <suite...>
  local name=$1
  # Suites spell vars as ../../plugins/…, resolved against the CONFIG's dir, so
  # the generated config must live here (two levels below the repo root).
  local cfg="$here/$name.gen.yaml"
  write_config "$cfg" "$2" "$3" "${@:4}"
  (cd "$evals" && npx promptfoo eval -c "$cfg" -o "$out/$name.json" --no-cache -j 4 --no-progress-bar >"$out/$name.log" 2>&1) || true
}

for e in "${efforts[@]}"; do
  suites=()
  while IFS= read -r s; do suites+=("$s"); done < <(suites_for "$e")
  run_pass "opus-$e" claude-opus-5-5 "$e" "${suites[@]}"
done
# 7.3: debugger on sonnet/high, against opus/medium from the loop above.
case " ${SUITES:-debugger} " in *" debugger "*) run_pass "debugger-sonnet-high" claude-sonnet-5 high debugger ;; esac
