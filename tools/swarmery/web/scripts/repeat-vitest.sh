#!/bin/bash
# repeat-vitest.sh <N> <test-file...>
#
# Runs the given vitest files N times in a row and stops at the first failing
# run. Built to shake out timing flakes: pair it with scripts/tests/under-load.sh
# to repeat a suite while every CPU is busy.
#
#   repeat-vitest.sh 30 src/pages/Memory.test.tsx
#
# Test paths are relative to tools/swarmery/web (the script cd's there first).
# The i18n catalogs are compiled once up front, as `npm test` does.
# Prints `repeat-vitest: N/N green` on success, or
# `repeat-vitest: failed on run k/N` and exits with vitest's code on failure.
set -u

usage() {
  printf 'usage: %s <N> <test-file...>\n' "${0##*/}" >&2
  exit 2
}

[ $# -ge 2 ] || usage
n="$1"
shift
case "$n" in '' | *[!0-9]* | 0) usage ;; esac

web_dir="$(cd "$(dirname "$0")/.." && pwd)" || exit 1
cd "$web_dir" || exit 1

npm run --silent i18n:compile >/dev/null || {
  printf 'repeat-vitest: i18n:compile failed\n' >&2
  exit 1
}

k=1
while [ "$k" -le "$n" ]; do
  npx vitest run "$@"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    printf 'repeat-vitest: failed on run %s/%s\n' "$k" "$n"
    exit "$rc"
  fi
  k=$((k + 1))
done
printf 'repeat-vitest: %s/%s green\n' "$n" "$n"
