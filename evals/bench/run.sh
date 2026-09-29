#!/usr/bin/env bash
# Frozen bench: run each task's fixed prompt through `claude -p` on a fresh
# copy of its fixture repo, grade the result with the task's deterministic
# check.sh, and write one JSON result file.
#
#   bash evals/bench/run.sh <out.json> <model> <effort> [task-id...]
#
# Spends tokens: every task is a real agentic session. See evals/bench/README.md.
#
# Environment:
#   BENCH_CLAUDE   binary to run instead of `claude` (selftest.sh passes a stub)
#   BENCH_TIMEOUT  wall-clock seconds per task (default 900); on expiry the task
#                  is recorded as pass:false, timedOut:true
#   BENCH_KEEP=1   keep each task's workdir and print its path
# Exported to the binary: BENCH_TASK_DIR (absolute path of the task being run).
# Exported to check.sh:   BENCH_FIXTURE_SHA (the workdir's fixture commit).
#
# Output: {model, effort, startedAt, claudeVersion,
#          tasks:[{id, pass, error, timedOut, exitCode, turns, costUsd, durationMs, checkTail}]}
# error:true means the run itself broke (non-zero exit, no parseable result,
# is_error, or a non-success subtype): the check is skipped, pass is false, and
# the task is NOT a model failure — compare.py refuses to compare such runs.
# turns/costUsd/durationMs come from the result's num_turns / total_cost_usd /
# duration_ms and are null when the run produced no parseable result.
set -euo pipefail

usage() {
  echo "usage: run.sh <out.json> <model> <effort> [task-id...]" >&2
  exit 2
}
[ $# -ge 3 ] || usage
out=$1 model=$2 effort=$3
shift 3

here=$(cd "$(dirname "$0")" && pwd)
claude_bin=${BENCH_CLAUDE:-claude}
limit=${BENCH_TIMEOUT:-900}
case "$limit" in '' | *[!0-9]*) echo "BENCH_TIMEOUT must be whole seconds" >&2; exit 2 ;; esac
allowed='Read,Edit,Write,Glob,Grep,Bash(go test:*),Bash(go build:*),Bash(bash:*),Bash(git status:*),Bash(git diff:*)'

for tool in jq git; do
  command -v "$tool" >/dev/null || { echo "run.sh: $tool is required" >&2; exit 2; }
done
resolved=$(command -v "$claude_bin" || true)
if [ -z "$resolved" ] || [ ! -x "$resolved" ]; then
  echo "run.sh: '$claude_bin' is not an executable (set BENCH_CLAUDE or install claude)" >&2
  exit 2
fi

ids=("$@")
if [ ${#ids[@]} -eq 0 ]; then
  while IFS= read -r d; do ids+=("$(basename "$d")"); done < <(find "$here/tasks" -mindepth 1 -maxdepth 1 -type d | sort)
fi
for id in "${ids[@]}"; do
  [ -f "$here/tasks/$id/prompt.md" ] || { echo "run.sh: unknown task $id" >&2; exit 2; }
done

if ! claude_version=$("$claude_bin" --version 2>&1 </dev/null); then
  echo "run.sh: '$claude_bin --version' failed: $claude_version" >&2
  exit 2
fi
claude_version=$(printf '%s\n' "$claude_version" | head -1)

scratch=$(mktemp -d "${TMPDIR:-/tmp}/bench.XXXXXX")
child_pgid=
cleanup() { [ "${BENCH_KEEP:-}" = 1 ] || rm -rf "$scratch"; }
# kill_group: TERM the running session's whole process group, KILL after a
# grace second. The session runs in its own group (set -m below), so this also
# reaches the tool processes it spawned. macOS has no `timeout`.
kill_group() {
  [ -n "$child_pgid" ] || return 0
  kill -TERM -- "-$child_pgid" 2>/dev/null || true
  sleep 1
  kill -KILL -- "-$child_pgid" 2>/dev/null || true
}
on_signal() {
  kill_group
  exit 130
}
trap cleanup EXIT
trap on_signal INT TERM

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
results="$scratch/results.jsonl"
: >"$results"

# run_limited <workdir> <task-dir> <stdout> <stderr> <prompt>: run the binary
# in the workdir under the wall-clock limit. Sets RUN_RC (exit status) and
# RUN_TIMED_OUT (true|false).
run_limited() {
  local work=$1 task=$2 stdout=$3 stderr=$4 prompt=$5 pid start
  RUN_RC=0
  RUN_TIMED_OUT=false
  set -m # the background job gets its own process group (pgid == pid)
  (
    cd "$work"
    BENCH_TASK_DIR=$task
    export BENCH_TASK_DIR
    exec "$claude_bin" -p "$prompt" --model "$model" --effort "$effort" \
      --output-format json --permission-mode acceptEdits --allowedTools "$allowed" \
      --setting-sources project,local --strict-mcp-config --no-session-persistence \
      </dev/null >"$stdout" 2>"$stderr"
  ) &
  pid=$!
  set +m
  child_pgid=$pid
  start=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    if [ $((SECONDS - start)) -ge "$limit" ]; then
      RUN_TIMED_OUT=true
      kill_group
      break
    fi
    sleep 0.2
  done
  wait "$pid" 2>/dev/null || RUN_RC=$?
  child_pgid=
}

write_out() {
  jq -s --arg model "$model" --arg effort "$effort" --arg startedAt "$started_at" \
    --arg claudeVersion "$claude_version" \
    '{model: $model, effort: $effort, startedAt: $startedAt, claudeVersion: $claudeVersion, tasks: .}' \
    "$results" >"$out.tmp"
  mv "$out.tmp" "$out"
}

for id in "${ids[@]}"; do
  task="$here/tasks/$id"
  work="$scratch/$id"
  mkdir -p "$work"
  cp -R "$task/repo/." "$work/"
  git -C "$work" init -q
  git -C "$work" add -A
  git -C "$work" -c user.name=bench -c user.email=bench@localhost -c commit.gpgsign=false \
    -c core.hooksPath=/dev/null commit -qm fixture
  fixture_sha=$(git -C "$work" rev-parse HEAD)
  # The saved reply lives in the workdir but must never count as an edit.
  printf '.bench-reply.txt\n' >>"$work/.git/info/exclude"

  res="$scratch/$id.result.json"
  errf="$scratch/$id.stderr"
  run_limited "$work" "$task" "$res" "$errf" "$(cat "$task/prompt.md")"

  meta='{"turns":null,"costUsd":null,"durationMs":null}'
  reply=
  error=false
  why=
  if jq -e 'type == "object"' "$res" >/dev/null 2>&1; then
    meta=$(jq -c '{turns: (.num_turns // null), costUsd: (.total_cost_usd // null), durationMs: (.duration_ms // null)}' "$res")
    reply=$(jq -r '.result // ""' "$res")
    if jq -e '.is_error == true or ((.subtype // "success") != "success")' "$res" >/dev/null; then
      error=true
      why="session error: subtype=$(jq -r '.subtype // "?"' "$res") $(jq -r '.result // "" | .[0:200]' "$res")"
    fi
  elif [ "$RUN_TIMED_OUT" = false ]; then
    error=true
    why="no parseable result on stdout"
  fi
  if [ "$RUN_TIMED_OUT" = false ] && [ "$RUN_RC" -ne 0 ] && [ "$error" = false ]; then
    error=true
    why="exited $RUN_RC"
  fi
  if [ -n "$reply" ]; then printf '%s\n' "$reply"; fi >"$work/.bench-reply.txt"

  pass=false
  exit_code=$RUN_RC
  if [ "$RUN_TIMED_OUT" = true ]; then
    exit_code=null
    tail_text="timed out after ${limit}s"
  elif [ "$error" = true ]; then
    tail_text=$(printf '%s (exit %s)\n%s\n' "$why" "$RUN_RC" "$(tail -3 "$errf" 2>/dev/null)" | tail -5)
  else
    if check_out=$(BENCH_FIXTURE_SHA=$fixture_sha bash "$task/check.sh" "$work" 2>&1); then pass=true; fi
    tail_text=$(printf '%s\n' "$check_out" | tail -5)
  fi

  jq -nc --arg id "$id" --argjson pass "$pass" --argjson error "$error" \
    --argjson timedOut "$RUN_TIMED_OUT" --argjson exitCode "$exit_code" \
    --argjson meta "$meta" --arg checkTail "$tail_text" \
    '{id: $id, pass: $pass, error: $error, timedOut: $timedOut, exitCode: $exitCode} + $meta + {checkTail: $checkTail}' >>"$results"
  write_out

  verdict=FAIL
  [ "$pass" = true ] && verdict=PASS
  [ "$RUN_TIMED_OUT" = true ] && verdict=TIMEOUT
  [ "$error" = true ] && verdict=ERROR
  printf '%-28s %-7s %s\n' "$id" "$verdict" "$(jq -r '"turns=\(.turns) cost=\(.costUsd) ms=\(.durationMs)"' <<<"$meta")" >&2
  if [ "${BENCH_KEEP:-}" = 1 ]; then echo "  workdir: $work" >&2; fi
done
write_out
