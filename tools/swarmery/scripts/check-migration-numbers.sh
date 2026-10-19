#!/usr/bin/env bash
# check-migration-numbers.sh — refuse a branch whose new store migrations do not
# sit above the base branch's highest migration number.
#
# Usage: check-migration-numbers.sh [--base <ref>]      (default: origin/main)
#
# store.Migrate (internal/store/migrate.go) refuses to start when two files claim
# one version number — but only at runtime, after the branch has landed. While a
# stack of branches is open, main keeps taking new migrations, and a number that
# was free when the branch was cut is taken by the time it lands. This check
# moves that failure to PR time:
#
#   1. Every migration ADDED on this branch (git diff --diff-filter=A from the
#      merge-base with <base> to HEAD, renames split into delete+add so a
#      renumbered file is checked too) must carry a number strictly above the
#      highest number in <base>'s migrations directory.
#   2. No number may appear twice in HEAD's migrations directory.
#   3. An added .sql file without an NNNN_ prefix is refused (store.Migrate would
#      refuse to start on it).
#
# It compares against <base> even for a PR into a feat/** branch: a stacked
# branch lands on main eventually, so main's numbering is the one that counts.
#
# Only *.sql files count, as in store.Migrate. Numbers are compared as decimals
# (0118 is 118, never octal). Git paths resolve from the repository top
# (git rev-parse --show-toplevel), so it runs from any cwd inside the checkout.
# Needs the full history of HEAD and <base> (CI checks out with fetch-depth: 0).
# Bash 3.2-compatible (macOS): no mapfile, no associative arrays.
#
# Exit codes: 0 all numbers fine, 1 a number is taken or duplicated,
# 2 usage error, not a git checkout, unresolvable base, or no merge-base.

set -uo pipefail

mig_dir="tools/swarmery/internal/store/migrations"

usage() {
  echo "usage: check-migration-numbers.sh [--base <ref>]   (default: origin/main)"
}

die() {
  printf 'check-migration-numbers: %s\n' "$1" >&2
  exit 2
}

base="origin/main"
while [ $# -gt 0 ]; do
  case "$1" in
    --base)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        usage >&2
        exit 2
      fi
      base="$2"
      shift 2
      ;;
    --base=*)
      base="${1#--base=}"
      if [ -z "$base" ]; then
        usage >&2
        exit 2
      fi
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git work tree"

if ! git -C "$repo_root" rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
  die "cannot resolve base '$base' to a commit (fetch it first: git fetch --no-tags origin main)"
fi

merge_base="$(git -C "$repo_root" merge-base "$base" HEAD 2>/dev/null)" ||
  die "no merge-base between '$base' and HEAD (a shallow clone? check out with fetch-depth: 0)"

# The label in messages: origin/main → main.
base_label="${base#origin/}"

# version_of <file name>: print the decimal version of NNNN_name.sql, or
# nothing when the name has no numeric prefix.
version_of() {
  local name="${1##*/}" prefix
  prefix="${name%%_*}"
  case "$prefix" in
    "$name" | "" | *[!0-9]*) return 0 ;;
  esac
  printf '%d\n' "$((10#$prefix))"
}

# sql_files <tree-ish>: the *.sql paths in <tree-ish>'s migrations directory.
sql_files() {
  git -C "$repo_root" ls-tree --name-only "$1" "$mig_dir/" | grep -E '\.sql$'
}

# --- base max ----------------------------------------------------------------
base_list="$(git -C "$repo_root" ls-tree --name-only "$base" "$mig_dir/")" ||
  die "cannot list $mig_dir in '$base'"
base_max=0
while IFS= read -r path; do
  case "$path" in *.sql) ;; *) continue ;; esac
  v="$(version_of "$path")"
  [ -z "$v" ] && continue
  if [ "$v" -gt "$base_max" ]; then
    base_max="$v"
  fi
done <<<"$base_list"
base_max_fmt="$(printf '%04d' "$base_max")"

failed=0

# --- 1+3. added migrations above base max --------------------------------------
added="$(git -C "$repo_root" diff --no-renames --diff-filter=A --name-only "$merge_base" HEAD -- "$mig_dir/")" ||
  die "git diff $merge_base..HEAD failed"
added_count=0
while IFS= read -r path; do
  case "$path" in *.sql) ;; *) continue ;; esac
  added_count=$((added_count + 1))
  file="${path##*/}"
  v="$(version_of "$path")"
  if [ -z "$v" ]; then
    printf '✗ %s: no NNNN_ version prefix — store.Migrate refuses it\n' "$file"
    failed=1
    continue
  fi
  if [ "$v" -le "$base_max" ]; then
    printf "✗ %s: number %04d ≤ %s's highest %s — renumber when rebasing for landing\n" \
      "$file" "$v" "$base_label" "$base_max_fmt"
    failed=1
  fi
done <<<"$added"

# --- 2. duplicates in HEAD -------------------------------------------------------
head_versions="$(sql_files HEAD | while IFS= read -r path; do
  v="$(version_of "$path")"
  [ -n "$v" ] && printf '%s %s\n' "$v" "${path##*/}"
done)"
dups="$(printf '%s\n' "$head_versions" | awk 'NF { print $1 }' | sort -n | uniq -d)"
while IFS= read -r v; do
  [ -z "$v" ] && continue
  files="$(printf '%s\n' "$head_versions" | awk -v n="$v" '$1 == n { print $2 }' | paste -sd, - | sed 's/,/, /g')"
  printf '✗ number %04d appears twice in HEAD: %s\n' "$v" "$files"
  failed=1
done <<<"$dups"

if [ "$failed" -ne 0 ]; then
  exit 1
fi
printf '✓ migrations: %d added, all above %s\n' "$added_count" "$base_max_fmt"
