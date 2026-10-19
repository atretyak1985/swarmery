#!/bin/bash
# run-suites.sh [dir]
#
# Runs every <dir>/*.test.sh with bash, in glob order, and reports each one:
#
#   run-suites: PASS <name>
#   run-suites: FAIL <name> (exit N)
#   run-suites: <p> passed, <f> failed
#
# Exits 1 if any suite failed, 0 otherwise. <dir> defaults to the directory
# this script lives in (scripts/tests). Discovery matches CI's "Shell tests"
# step in .github/workflows/ci.yml, so a green run here is the same bar as the
# CI loop, behind one file-backed command. Bash 3.2-compatible (macOS).
#
# Deliberately NOT named *.test.sh: CI runs every scripts/tests/*.test.sh as a
# suite, and this is a tool. Its suite is run-suites.test.sh.
set -u

dir="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
if [ ! -d "$dir" ]; then
  printf 'run-suites: not a directory: %s\n' "$dir" >&2
  exit 2
fi

passed=0
failed=0
for t in "$dir"/*.test.sh; do
  # An unmatched glob stays literal under bash 3.2 (no nullglob by default).
  [ -e "$t" ] || continue
  name="${t##*/}"
  bash "$t"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    passed=$((passed + 1))
    printf 'run-suites: PASS %s\n' "$name"
  else
    failed=$((failed + 1))
    printf 'run-suites: FAIL %s (exit %d)\n' "$name" "$rc"
  fi
done

printf 'run-suites: %d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
