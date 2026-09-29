#!/usr/bin/env bash
# b01-bugfix: go test passes against the fixture's own tests. The check runs in
# a scratch copy of the workdir where every *_test.go the fixture does not have
# is deleted and every fixture test is restored byte for byte, so adding a
# regression test is harmless and editing or deleting a test cannot help.
#   check.sh <workdir>   exit 0 iff acceptable; one line per failed assertion.
set -uo pipefail
work=${1:?usage: check.sh <workdir>}
fixture="$(cd "$(dirname "$0")" && pwd)/repo"
export GOTOOLCHAIN=local
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cp -R "$work/." "$tmp/"
rm -rf "$tmp/.git"
find "$tmp" -name '*_test.go' -type f -delete
(cd "$fixture" && find . -name '*_test.go' -type f) | while IFS= read -r f; do
  mkdir -p "$tmp/$(dirname "$f")"
  cp "$fixture/$f" "$tmp/$f"
done

if ! out=$(cd "$tmp" && go test ./... 2>&1); then
  echo "go test (fixture tests restored) failed: $(echo "$out" | grep -m1 -E 'FAIL|error|--- ' || echo "$out" | tail -1)"
  exit 1
fi
exit 0
