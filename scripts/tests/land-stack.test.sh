#!/usr/bin/env bash
# Test plugins/core/bin/land-stack.sh: it prints a bottom-up landing sequence
# (merge, then wait for .merged, PR by PR), front-loads retargets with
# --retarget, never touches the network unless asked with `--repo -`, and
# exits 2 on a usage error. Bash 3.2-compatible (macOS).
set -u

here="$(cd "$(dirname "$0")/../.." && pwd)"
script="$here/plugins/core/bin/land-stack.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail=0
check() { # <description> <condition-exit-code>
  if [ "$2" -ne 0 ]; then
    printf 'FAIL: %s\n' "$1"
    fail=1
  fi
}

# A fake gh on PATH: records every call, answers `gh repo view` only.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_CALLS"
if [ "$1 $2" = "repo view" ]; then
  echo "owner/from-gh"
  exit 0
fi
exit 9
GH
chmod +x "$tmp/bin/gh"
export GH_CALLS="$tmp/gh-calls"
: >"$GH_CALLS"
PATH="$tmp/bin:$PATH"
export PATH

# line_of <file> <fixed string>: 1-based line number of the first match, or 0.
line_of() {
  local n
  n="$(grep -nF -- "$2" "$1" | head -1 | cut -d: -f1)"
  printf '%s' "${n:-0}"
}

# ── default mode: ordering and the wait after every merge ───────────────────
out="$tmp/plain.txt"
bash "$script" --repo owner/orders 11 12 13 >"$out"
rc=$?
check "plain run exits 0 (got $rc)" "$rc"

m11="$(line_of "$out" "gh pr merge 11 --repo owner/orders --squash --delete-branch")"
m12="$(line_of "$out" "gh pr merge 12 --repo owner/orders --squash --delete-branch")"
m13="$(line_of "$out" "gh pr merge 13 --repo owner/orders --squash --delete-branch")"
[ "$m11" -gt 0 ] && [ "$m12" -gt "$m11" ] && [ "$m13" -gt "$m12" ]
check "merge lines print bottom-up 11 < 12 < 13 (got $m11 $m12 $m13)" $?

for pr in 11 12 13; do
  m="$(line_of "$out" "gh pr merge $pr ")"
  want="until [ \"\$(gh api repos/owner/orders/pulls/$pr --jq .merged)\" = true ]; do sleep 5; done"
  got="$(sed -n "$((m + 1))p" "$out")"
  [ "$got" = "$want" ]
  check "the line after merge $pr is its wait (got: $got)" $?

  b="$(line_of "$out" "repos/owner/orders/pulls/$pr --jq .base.ref")"
  [ "$b" -gt 0 ] && [ "$b" -lt "$m" ]
  check "base check for $pr precedes its merge" $?
  grep -qF -- "[ \"\$(gh api repos/owner/orders/pulls/$pr --jq .base.ref)\" = \"main\" ] || gh pr edit $pr --repo owner/orders --base main" "$out"
  check "base check for $pr retargets to main" $?
done

# Each later merge waits for the previous one: wait(11) < merge(12).
w11="$(line_of "$out" "pulls/11 --jq .merged")"
[ "$w11" -gt 0 ] && [ "$w11" -lt "$m12" ]
check "merge 12 comes after the wait for 11" $?

[ "$(grep -c '^gh pr merge ' "$out")" -eq 3 ]
check "exactly three merge lines" $?
[ "$(grep -c '^until ' "$out")" -eq 3 ]
check "exactly three wait lines" $?
! grep -q '^gh pr edit ' "$out"
check "no up-front retarget lines without --retarget" $?

[ ! -s "$GH_CALLS" ]
check "default mode never calls gh (calls: $(tr '\n' ';' <"$GH_CALLS"))" $?

# ── --retarget: edits for every stacked PR, before the first merge ──────────
out="$tmp/retarget.txt"
bash "$script" --repo owner/orders --base trunk --retarget 21 22 23 >"$out"
check "--retarget run exits 0" $?
first_merge="$(line_of "$out" "gh pr merge 21 ")"
e22="$(line_of "$out" "gh pr edit 22 --repo owner/orders --base trunk")"
e23="$(line_of "$out" "gh pr edit 23 --repo owner/orders --base trunk")"
[ "$e22" -gt 0 ] && [ "$e23" -gt "$e22" ] && [ "$e23" -lt "$first_merge" ]
check "--retarget prints edits for 22 and 23 before merging 21 (got $e22 $e23 $first_merge)" $?
[ "$(grep -c '^gh pr edit ' "$out")" -eq 2 ]
check "--retarget skips the bottom PR, which already sits on the base" $?
grep -qF -- '--jq .base.ref)" = "trunk" ]' "$out"
check "--base flows into the base checks" $?

# ── --merge-flags passthrough ───────────────────────────────────────────────
out="$tmp/flags.txt"
bash "$script" --repo owner/orders --merge-flags "--rebase" 5 >"$out"
check "--merge-flags run exits 0" $?
grep -qxF -- "gh pr merge 5 --repo owner/orders --rebase" "$out"
check "--merge-flags replaces the default merge flags" $?

# ── --repo - resolves through gh repo view (the only network mode) ──────────
: >"$GH_CALLS"
out="$tmp/dash.txt"
bash "$script" --repo - 7 >"$out"
check "--repo - exits 0" $?
grep -qF -- "gh pr merge 7 --repo owner/from-gh " "$out"
check "--repo - uses the repository gh reports" $?
grep -q '^repo view' "$GH_CALLS"
check "--repo - asked gh repo view" $?

# ── usage errors exit 2 ─────────────────────────────────────────────────────
usage_case() { # <description> <args…>
  local desc="$1"
  shift
  bash "$script" "$@" >/dev/null 2>"$tmp/err"
  local rc=$?
  [ "$rc" -eq 2 ]
  check "usage error exits 2: $desc (got $rc)" $?
  grep -q '^usage: land-stack.sh' "$tmp/err"
  check "usage error prints usage to stderr: $desc" $?
}
usage_case "no arguments"
usage_case "no PRs" --repo owner/orders
usage_case "no --repo" 11 12
usage_case "non-numeric PR" --repo owner/orders 11 abc
usage_case "PR given twice" --repo owner/orders 11 11
usage_case "unknown option" --repo owner/orders --force 11
usage_case "--repo without a value" --repo
usage_case "repo not owner/name" --repo orders 11
usage_case "shell metacharacter in repo" --repo 'owner/x;rm' 11
usage_case "shell metacharacter in merge flags" --repo owner/orders --merge-flags '--squash; echo' 11

bash "$script" --help >/dev/null
check "--help exits 0" $?

if [ "$fail" -ne 0 ]; then
  echo "land-stack.test.sh: FAIL"
  exit 1
fi
echo "land-stack.test.sh: PASS"
