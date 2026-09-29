#!/usr/bin/env bash
# b03-refactor-constraint: no .go file exceeds 150 lines, the package was
# actually split, the exported API (`go doc -all`) matches the stored golden
# byte for byte, and go test passes against the fixture's own tests (run in a
# scratch copy with extra *_test.go removed and fixture tests restored, so an
# added test is harmless and an edited one cannot help).
#   check.sh <workdir>   exit 0 iff acceptable; one line per failed assertion.
set -uo pipefail
work=${1:?usage: check.sh <workdir>}
here="$(cd "$(dirname "$0")" && pwd)"
fixture="$here/repo"
export GOTOOLCHAIN=local
max=150
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

n=0
while IFS= read -r f; do
  [ -n "$f" ] || continue
  lines=$(wc -l <"$work/$f" | tr -d ' ')
  [ "$lines" -le "$max" ] || { echo "$f has $lines lines (max $max)"; fail=1; }
  case "$f" in *_test.go) ;; *) n=$((n + 1)) ;; esac
done < <(cd "$work" && find . -name '*.go' -not -path './.git/*' | sort)
[ "$n" -ge 2 ] || { echo "expected the package split into at least 2 files, found $n"; fail=1; }

cp -R "$work/." "$tmp/"
rm -rf "$tmp/.git"
find "$tmp" -name '*_test.go' -type f -delete
(cd "$fixture" && find . -name '*_test.go' -type f) | while IFS= read -r f; do
  mkdir -p "$tmp/$(dirname "$f")"
  cp "$fixture/$f" "$tmp/$f"
done
if ! out=$(cd "$tmp" && go test ./... 2>&1); then
  echo "go test (fixture tests restored) failed: $(echo "$out" | grep -m1 -E 'FAIL|error|--- ' || echo "$out" | tail -1)"
  fail=1
fi

if ! api=$(cd "$work" && go doc -all . 2>&1); then
  echo "go doc failed: $(echo "$api" | tail -1)"
  fail=1
elif [ "$api" != "$(cat "$here/api.golden")" ]; then
  echo "exported API or doc comments changed: $(diff <(printf '%s\n' "$api") "$here/api.golden" | grep -m1 '^[<>]')"
  fail=1
fi
exit "$fail"
