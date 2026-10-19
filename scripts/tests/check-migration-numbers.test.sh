#!/usr/bin/env bash
# check-migration-numbers.test.sh — the PR-time store migration numbering gate.
#
# Runs tools/swarmery/scripts/check-migration-numbers.sh against small fixture
# git repositories: a `main` branch holding migrations 0001 and 0003 (a gap, as
# the real tree has), and a `feature` branch on top of it with one change each.
#
# Offline and framework-free; bash 3.2-compatible. Run with
# `bash scripts/tests/check-migration-numbers.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHECK="${ROOT}/tools/swarmery/scripts/check-migration-numbers.sh"
MIG="tools/swarmery/internal/store/migrations"

pass=0
fail=0
ok()  { pass=$((pass + 1)); printf '  ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL  %s\n        %s\n' "$1" "$2"; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# g <dir> <git args…>: git in <dir>, isolated from the caller's hooks and signing.
g() {
  local dir="$1"
  shift
  git -C "$dir" -c user.name=test -c user.email=test@example.com \
    -c commit.gpgsign=false -c core.hooksPath=/dev/null "$@" >/dev/null 2>&1
}

# repo <dir> <file>…: a repo whose `main` holds 0001_init.sql, 0003_c.sql and
# a README, then a `feature` branch that adds each <file> under the migrations
# directory (a file name without a slash) or at the repo root (README edit).
repo() {
  local dir="$1" f
  shift
  mkdir -p "${dir}/${MIG}"
  g "$dir" init -q
  g "$dir" symbolic-ref HEAD refs/heads/main
  printf 'CREATE TABLE a (x);\n' >"${dir}/${MIG}/0001_init.sql"
  printf 'CREATE TABLE c (x);\n' >"${dir}/${MIG}/0003_c.sql"
  printf 'readme\n' >"${dir}/README.md"
  g "$dir" add -A
  g "$dir" commit -q -m main
  g "$dir" checkout -q -b feature
  for f in "$@"; do
    if [ "$f" = README.md ]; then
      printf 'changed\n' >>"${dir}/README.md"
    else
      printf -- '-- %s\n' "$f" >"${dir}/${MIG}/${f}"
    fi
  done
  g "$dir" add -A
  g "$dir" commit -q -m feature
}

# expect <desc> <want exit> <cwd> <needle> [<check args>…]
expect() {
  local desc="$1" want="$2" cwd="$3" needle="$4" out rc
  shift 4
  out="$(cd "$cwd" && bash "$CHECK" "$@" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then bad "$desc" "exit $rc, want $want: $out"; return; fi
  case "$out" in *"$needle"*) ;; *) bad "$desc" "output lacks [$needle]: $out"; return ;; esac
  ok "$desc"
}

echo "check-migration-numbers"

repo "${TMP}/above" 0004_new.sql
expect "an added migration above main's highest passes" 0 "${TMP}/above" \
  "✓ migrations: 1 added, all above 0003" --base main

repo "${TMP}/equal" 0003_mine.sql
expect "an added migration equal to main's highest fails" 1 "${TMP}/equal" \
  "✗ 0003_mine.sql: number 0003 ≤ main's highest 0003" --base main

repo "${TMP}/below" 0002_late.sql
expect "an added migration below main's highest fails" 1 "${TMP}/below" \
  "✗ 0002_late.sql: number 0002 ≤ main's highest 0003" --base main

repo "${TMP}/dup" 0004_a.sql 0004_b.sql
expect "a number that appears twice in HEAD fails" 1 "${TMP}/dup" \
  "✗ number 0004 appears twice in HEAD: 0004_a.sql, 0004_b.sql" --base main

repo "${TMP}/none" README.md
expect "no migrations added passes" 0 "${TMP}/none" \
  "✓ migrations: 0 added, all above 0003" --base main

# CI runs it from tools/swarmery (the workflow's default working directory).
expect "it resolves git paths from the repo top, not the cwd" 0 "${TMP}/above/tools/swarmery" \
  "✓ migrations: 1 added, all above 0003" --base main

expect "an unresolvable base exits 2" 2 "${TMP}/above" \
  "cannot resolve base 'nope'" --base nope

expect "a usage error exits 2" 2 "${TMP}/above" "usage:" --bogus

printf 'check-migration-numbers: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
