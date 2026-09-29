#!/usr/bin/env bash
# Proves every bench check discriminates, without spending a token: run.sh is
# driven by stub `claude` binaries instead of a model, and the checks are fed
# hand-made workdirs for the edge cases a model might hit.
#
#   bash evals/bench/selftest.sh
#
# run.sh with stubs:
# - stub-reference applies the task's reference.patch (an empty patch means "the
#   right answer is no change" and replies "NO-OP: already fixed")  -> 5/5 pass
# - stub-noop changes nothing and replies "done"                     -> 0/5 pass
#   (b04 fails too: its reply lacks the explicit no-op claim, by design)
# - stub-hang never returns; BENCH_TIMEOUT=1 must record pass:false,
#   timedOut:true, error:false (a timeout is the model's, not infra's)
# - stub-error exits 1 with is_error:true -> error:true, pass:false, check skipped
# - a missing BENCH_CLAUDE aborts run.sh before any task runs
# checks on hand-made workdirs (edge cases):
# - b01 correct fix + an added regression test         -> pass
# - b01 no fix, fixture test rewritten to pass         -> fail
# - b02 reference script + comments that mention jq    -> pass
# - b02 reference script + a real jq invocation        -> fail
# - b04 edit committed, then "already fixed" claimed   -> fail
# - b05 own digit validation, no wrapped error         -> pass
# - b05 gofmt-wrapped unprefixed fmt.Errorf(           -> fail
# Exits non-zero naming each failed assertion.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/bench-selftest.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
status=0
bad() {
  echo "  FAIL: $*"
  status=1
}

# Every stub answers --version without touching anything, and refuses to run
# outside run.sh (no BENCH_TASK_DIR) so it can never patch the caller's cwd.
# shellcheck disable=SC2016 # literal stub source; expands when the stub runs
stub_head='#!/usr/bin/env bash
set -euo pipefail
case " $* " in *" --version "*) echo "stub 0.0.0"; exit 0 ;; esac
: "${BENCH_TASK_DIR:?stub must be run by run.sh}"'

cat >"$tmp/stub-reference" <<EOF
$stub_head
patch="\$BENCH_TASK_DIR/reference.patch"
if [ -s "\$patch" ]; then
  git apply "\$patch"
  reply="done"
else
  reply="NO-OP: already fixed"
fi
jq -nc --arg r "\$reply" '{type:"result",subtype:"success",is_error:false,num_turns:1,duration_ms:5,total_cost_usd:0,result:\$r}'
EOF

cat >"$tmp/stub-noop" <<EOF
$stub_head
jq -nc '{type:"result",subtype:"success",is_error:false,num_turns:1,duration_ms:5,total_cost_usd:0,result:"done"}'
EOF

cat >"$tmp/stub-hang" <<EOF
$stub_head
sleep 60
EOF

cat >"$tmp/stub-error" <<EOF
$stub_head
jq -nc '{type:"result",subtype:"error_during_execution",is_error:true,num_turns:0,duration_ms:5,total_cost_usd:0,result:"API Error: 529 overloaded"}'
exit 1
EOF
chmod +x "$tmp"/stub-*

all=()
while IFS= read -r d; do all+=("$(basename "$d")"); done < <(find "$here/tasks" -mindepth 1 -maxdepth 1 -type d | sort)
n=${#all[@]}

run_with() { # <stub> <out.json> [task-id...]
  local stub=$1 out=$2
  shift 2
  BENCH_CLAUDE="$tmp/$stub" bash "$here/run.sh" "$out" stub-model stub-effort "$@" 2>"$out.log" || {
    echo "run.sh failed under $stub:"
    cat "$out.log"
    exit 1
  }
}

echo "== run.sh with stubs"
run_with stub-reference "$tmp/ref.json"
passed=$(jq '[.tasks[] | select(.pass)] | length' "$tmp/ref.json")
echo "reference stub: $passed/$n pass"
if [ "$passed" -ne "$n" ]; then
  bad "should pass but failed: $(jq -r '[.tasks[] | select(.pass | not) | .id] | join(" ")' "$tmp/ref.json")"
  jq -r '.tasks[] | select(.pass | not) | "    \(.id): \(.checkTail)"' "$tmp/ref.json"
fi

run_with stub-noop "$tmp/noop.json"
passed=$(jq '[.tasks[] | select(.pass)] | length' "$tmp/noop.json")
echo "no-op stub:     $passed/$n pass"
[ "$passed" -eq 0 ] || bad "should fail but passed: $(jq -r '[.tasks[] | select(.pass) | .id] | join(" ")' "$tmp/noop.json")"
[ "$(jq '[.tasks[] | select(.error)] | length' "$tmp/noop.json")" -eq 0 ] || bad "no-op runs were recorded as errors"

BENCH_TIMEOUT=1 run_with stub-hang "$tmp/hang.json" "${all[0]}"
if jq -e '.tasks | length == 1 and .[0].timedOut == true and .[0].pass == false and .[0].error == false' "$tmp/hang.json" >/dev/null; then
  echo "hang stub:      ${all[0]} timed out and failed, as required"
else
  bad "timeout not enforced: $(jq -c '.tasks' "$tmp/hang.json")"
fi

run_with stub-error "$tmp/err.json" "${all[0]}"
if jq -e '.tasks | length == 1 and .[0].error == true and .[0].pass == false and .[0].timedOut == false and .[0].exitCode == 1' "$tmp/err.json" >/dev/null; then
  echo "error stub:     ${all[0]} recorded as error (exit 1), not as a model fail"
else
  bad "infra error not recorded as error: $(jq -c '.tasks' "$tmp/err.json")"
fi

if BENCH_CLAUDE="$tmp/no-such-binary" bash "$here/run.sh" "$tmp/missing.json" m e >/dev/null 2>&1; then
  bad "run.sh accepted a missing BENCH_CLAUDE"
elif [ -e "$tmp/missing.json" ]; then
  bad "run.sh wrote results despite a missing BENCH_CLAUDE"
else
  echo "missing binary: run.sh aborted before any task"
fi

want_keys='["id","pass","error","timedOut","exitCode","turns","costUsd","durationMs","checkTail"]'
if jq -e --argjson ids "$(printf '%s\n' "${all[@]}" | jq -R . | jq -s .)" --argjson keys "$want_keys" \
  '(.tasks | map(.id)) == $ids and ([.tasks[] | keys_unsorted == $keys] | all)' "$tmp/ref.json" >/dev/null; then
  echo "result shape:   ok ($want_keys)"
else
  bad "result JSON shape is wrong: $(jq -c '.tasks[0] | keys_unsorted' "$tmp/ref.json")"
fi

echo "== checks on edge-case workdirs"
# mkwork <task-id>: a committed fixture workdir like run.sh builds; prints its path.
mkwork() {
  local w
  w=$(mktemp -d "$tmp/work.XXXXXX")
  cp -R "$here/tasks/$1/repo/." "$w/"
  git -C "$w" init -q
  git -C "$w" add -A
  git -C "$w" -c user.name=bench -c user.email=bench@localhost -c commit.gpgsign=false \
    -c core.hooksPath=/dev/null commit -qm fixture
  printf '.bench-reply.txt\n' >>"$w/.git/info/exclude"
  echo "$w"
}

# expect <pass|fail> <label> <task-id> <workdir> [output-regex]
expect() {
  local want=$1 label=$2 task=$3 w=$4 re=${5:-} out got=fail
  if out=$(BENCH_FIXTURE_SHA=$(git -C "$w" rev-list --max-parents=0 HEAD) bash "$here/tasks/$task/check.sh" "$w" 2>&1); then
    got=pass
  fi
  if [ "$got" != "$want" ]; then
    bad "$label: expected $want, got $got: $(echo "$out" | head -2 | tr '\n' ' ')"
  elif [ -n "$re" ] && ! echo "$out" | grep -Eq "$re"; then
    bad "$label: failed, but not for the expected reason ($re): $out"
  else
    echo "  ok  $label -> $got"
  fi
}

w=$(mkwork b01-bugfix)
git -C "$w" apply "$here/tasks/b01-bugfix/reference.patch"
cat >"$w/total_regression_test.go" <<'EOF'
package orders

import "testing"

func TestTotalCountsLastLine(t *testing.T) {
	if got := Total([]Line{{"A-100", 1, 100}, {"B-200", 1, 1}}); got != 101 {
		t.Fatalf("Total = %d, want 101", got)
	}
}
EOF
expect pass "b01 correct fix + added regression test" b01-bugfix "$w"

w=$(mkwork b01-bugfix)
cat >"$w/total_test.go" <<'EOF'
package orders

import "testing"

func TestTotal(t *testing.T) {}
EOF
expect fail "b01 no fix, fixture test gutted" b01-bugfix "$w" 'go test'

w=$(mkwork b02-feature)
git -C "$w" apply "$here/tasks/b02-feature/reference.patch"
printf '# JSON is built by hand: no jq here.\ntrue # jq is not needed\n' >>"$w/inventory.sh"
expect pass "b02 reference + comments mentioning jq" b02-feature "$w"

w=$(mkwork b02-feature)
git -C "$w" apply "$here/tasks/b02-feature/reference.patch"
printf 'command -v jq >/dev/null\n' >>"$w/inventory.sh"
expect fail "b02 reference + a real jq invocation" b02-feature "$w" 'depends on jq'

w=$(mkwork b04-stale-premise)
printf '\n// Clamp documented.\n' >>"$w/discount.go"
git -C "$w" -c user.name=m -c user.email=m@localhost -c commit.gpgsign=false -c core.hooksPath=/dev/null \
  commit -qam "clamp discount"
echo "The clamp was already there; I only tidied it." >"$w/.bench-reply.txt"
expect fail "b04 commit-then-claim (clean status)" b04-stale-premise "$w" 'HEAD moved'

w=$(mkwork b05-read-the-docs)
cat >"$w/lineitem.go" <<'EOF'
package orders

import (
	"errors"
	"strings"
)

// LineItem is one SKU on an order, bought Qty times.
type LineItem struct {
	SKU string
	Qty int
}

// ParseLineItem parses "<sku>:<qty>".
func ParseLineItem(s string) (LineItem, error) {
	sku, qty, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || sku == "" {
		return LineItem{}, errors.New("orders: line item must be <sku>:<qty>")
	}
	n := 0
	for _, r := range qty {
		if r < '0' || r > '9' {
			return LineItem{}, errors.New("orders: quantity must be digits")
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return LineItem{}, errors.New(
			"orders: quantity must be positive")
	}
	return LineItem{SKU: sku, Qty: n}, nil
}
EOF
cat >"$w/lineitem_test.go" <<'EOF'
package orders

import "testing"

func TestParseLineItem(t *testing.T) {
	if got, err := ParseLineItem("A-100:3"); err != nil || got != (LineItem{"A-100", 3}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := ParseLineItem("A-100:x"); err == nil {
		t.Fatal("want an error")
	}
}
EOF
expect pass "b05 own validation, no wrapped error" b05-read-the-docs "$w"

w=$(mkwork b05-read-the-docs)
git -C "$w" apply "$here/tasks/b05-read-the-docs/reference.patch"
cat >"$w/extra.go" <<'EOF'
package orders

import "fmt"

func describe(err error) error {
	return fmt.Errorf(
		"lookup failed: %w",
		err,
	)
}
EOF
expect fail "b05 gofmt-wrapped unprefixed fmt.Errorf(" b05-read-the-docs "$w" 'prefix in extra.go'

if [ "$status" -eq 0 ]; then echo "selftest: OK"; else echo "selftest: FAILED"; fi
exit "$status"
