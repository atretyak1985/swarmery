#!/usr/bin/env bash
# b02-feature: `inventory.sh --json` prints the expected array (compared as
# canonical JSON), combines with --low in either order, prints [] for an empty
# file, does not use jq, and the pre-existing modes print byte-identical output
# to the fixture's script on a check-owned input.
#   check.sh <workdir>   exit 0 iff acceptable; one line per failed assertion.
set -uo pipefail
work=${1:?usage: check.sh <workdir>}
fixture="$(cd "$(dirname "$0")" && pwd)/repo"
script="$work/inventory.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

[ -f "$script" ] || { echo "inventory.sh missing"; exit 1; }

cat >"$tmp/items.csv" <<'EOF'
# check-owned input
K-1,7,dock-a
K-2,1,dock-b

K-3,25,shelf-9
EOF
: >"$tmp/empty.csv"
printf '# only a comment\n' >"$tmp/comment.csv"

# Existing modes: identical output to the fixture's own script.
for args in "" "--total" "--low 5" "--low 30"; do
  # shellcheck disable=SC2086 # args is a deliberate word list
  want=$(bash "$fixture/inventory.sh" $args "$tmp/items.csv" 2>&1)
  # shellcheck disable=SC2086
  got=$(bash "$script" $args "$tmp/items.csv" 2>&1)
  [ "$want" = "$got" ] || { echo "mode '${args:-table}' changed output"; fail=1; }
done

json_is() { # <label> <expected-json> <args...>
  local label=$1 want=$2 got
  shift 2
  if ! got=$(bash "$script" "$@" 2>/dev/null); then
    echo "$label: exited non-zero"
    fail=1
    return
  fi
  if ! got=$(printf '%s' "$got" | jq -cS . 2>/dev/null) || [ -z "$got" ]; then
    echo "$label: output is not JSON"
    fail=1
    return
  fi
  want=$(printf '%s' "$want" | jq -cS .)
  [ "$got" = "$want" ] || { echo "$label: got $got want $want"; fail=1; }
}

all='[{"sku":"K-1","qty":7,"location":"dock-a"},{"sku":"K-2","qty":1,"location":"dock-b"},{"sku":"K-3","qty":25,"location":"shelf-9"}]'
low='[{"sku":"K-2","qty":1,"location":"dock-b"}]'
json_is "--json" "$all" --json "$tmp/items.csv"
json_is "--json --low 5" "$low" --json --low 5 "$tmp/items.csv"
json_is "--low 5 --json" "$low" --low 5 --json "$tmp/items.csv"
json_is "--json on empty file" '[]' --json "$tmp/empty.csv"
json_is "--json on comment-only file" '[]' --json "$tmp/comment.csv"

# A jq invocation outside comments: strip whole-line and trailing `#` comments
# first, then look for jq as a command word (after start, a pipe, ;, &, (, a
# backtick, a quote or whitespace, followed by whitespace or end of line).
jq_use=$(sed -e 's/^[[:space:]]*#.*$//' -e 's/[[:space:]]#.*$//' "$script" |
  grep -nE "(^|[|;&(\`\"'[:space:]])jq([[:space:]]|\$)")
if [ -n "$jq_use" ]; then
  echo "inventory.sh depends on jq: $(echo "$jq_use" | head -1)"
  fail=1
fi
exit "$fail"
