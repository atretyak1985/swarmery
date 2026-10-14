#!/bin/bash
# at-minute-boundary.sh <cmd...> — run a command so that it straddles a wall-clock
# minute: wait until the seconds field reads 58 or 59, print the start time, run
# the command, print the end time, and exit with the command's own status.
# A test that diffs two renders taken a moment apart and is still exposed to the
# clock fails reliably under this wrapper. Not a *.test.sh — a harness, not a suite.
set -uo pipefail

if [ "$#" -eq 0 ]; then
  printf 'usage: %s <cmd...>\n' "$(basename "$0")" >&2
  exit 2
fi

while :; do
  s="$(date +%S)"
  case "$s" in 58|59) break ;; esac
  sleep 0.2
done

printf 'at-minute-boundary: start %s\n' "$(date +%H:%M:%S)"
"$@"
rc=$?
printf 'at-minute-boundary: end %s\n' "$(date +%H:%M:%S)"
exit "$rc"
