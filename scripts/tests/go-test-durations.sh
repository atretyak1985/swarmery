#!/bin/bash
# go-test-durations.sh <go-test-json-file> [N=40]
#
# Prints the N slowest top-level tests from a `go test -json` capture, one per
# line as `<seconds>\t<Test>`, slowest first, then a summary line:
#
#   go-test-durations: total <sum>s over <count> tests; top-N share <pct>%
#
# Only finished top-level tests count: events with Action "pass" or "fail" and
# a Test name without "/" (subtests are already inside their parent's Elapsed).
# Lines that are not JSON (a build error printed raw by an older toolchain) are
# skipped rather than aborting the report.
#
# Built to pick a package timeout from measurement instead of guessing, and to
# find the outliers whose time is a sleep or a poll rather than real work:
#
#   go test -json -count=1 ./internal/api/ > api.json
#   bash scripts/tests/go-test-durations.sh api.json 40
#
# Deliberately NOT named *.test.sh: CI runs every scripts/tests/*.test.sh as a
# suite, and this is a tool.
set -euo pipefail

usage() {
  printf 'usage: %s <go-test-json-file> [N=40]\n' "${0##*/}" >&2
  exit 2
}

[ $# -ge 1 ] && [ $# -le 2 ] || usage
file="$1"
n="${2:-40}"
case "$n" in '' | *[!0-9]* | 0) usage ;; esac
[ -r "$file" ] || {
  printf 'go-test-durations: cannot read %s\n' "$file" >&2
  exit 2
}
command -v jq >/dev/null 2>&1 || {
  printf 'go-test-durations: jq is required\n' >&2
  exit 2
}

jq -R -r -n --argjson n "$n" '
  [ inputs
    | fromjson?
    | select(type == "object")
    | select(.Action == "pass" or .Action == "fail")
    | select((.Test // "") != "" and ((.Test | contains("/")) | not))
    | {test: .Test, secs: (.Elapsed // 0)} ]
  | sort_by(-.secs) as $all
  | ($all | map(.secs) | add // 0) as $total
  | ($all[0:$n]) as $top
  | ($top | map(.secs) | add // 0) as $topsum
  | ($top[] | "\(.secs)\t\(.test)"),
    "go-test-durations: total \($total * 100 | round / 100)s over \($all | length) tests; top-\($n) share \(if $total > 0 then ($topsum / $total * 1000 | round / 10) else 0 end)%"
' <"$file"
