#!/usr/bin/env bash
# b05-read-the-docs: CONTRIBUTING.md requires every error to carry the
# "orders: " prefix and wrap foreign errors with %w; the prompt does not repeat
# it. Passes iff a check-owned test of ParseLineItem passes (behaviour, and
# every error carries the prefix), every literal errors.New / fmt.Errorf
# message in non-test code starts with "orders: " (multi-line calls included),
# the model added its own test, and go test passes.
#   check.sh <workdir>   exit 0 iff acceptable; one line per failed assertion.
set -uo pipefail
work=${1:?usage: check.sh <workdir>}
here="$(cd "$(dirname "$0")" && pwd)"
export GOTOOLCHAIN=local
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

if ! out=$(cd "$work" && go test ./... 2>&1); then
  echo "go test failed: $(echo "$out" | grep -m1 -E 'FAIL|error|--- ' || echo "$out" | tail -1)"
  fail=1
fi

if ! grep -lq 'ParseLineItem' "$work"/*_test.go 2>/dev/null; then
  echo "no test of ParseLineItem added"
  fail=1
fi

# Every errors.New / fmt.Errorf whose message is a string literal must start
# with "orders: ". Each file is joined onto one line first, so a gofmt-wrapped
# call (`fmt.Errorf(` then the literal on the next line) is still seen.
for f in "$work"/*.go; do
  case "$f" in *_test.go | "$work/*.go") continue ;; esac
  # shellcheck disable=SC2016 # the backtick is a literal Go raw-string quote
  bad=$(tr '\n\t' '  ' <"$f" |
    grep -oE '(errors\.New|fmt\.Errorf)\([[:space:]]*["`][^"`]{0,8}' |
    grep -vE '\([[:space:]]*["`]orders: ')
  if [ -n "$bad" ]; then
    echo "error without the \"orders: \" prefix in $(basename "$f"): $(echo "$bad" | head -1)"
    fail=1
  fi
done

cp -R "$work/." "$tmp/"
cp "$here/hidden/zz_bench_check_test.go" "$tmp/"
if ! out=$(cd "$tmp" && go test -run '^TestBenchCheck' ./... 2>&1); then
  echo "check-owned test failed: $(echo "$out" | grep -m1 -E 'lineitem|ParseLineItem|undefined|error|--- ' || echo "$out" | tail -1)"
  fail=1
fi
exit "$fail"
