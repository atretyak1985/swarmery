#!/usr/bin/env bash
# land-stack.sh [--repo <owner/name>|-] [--base <branch>] [--retarget]
#               [--merge-flags "<flags>"] <pr> [<pr> …]
#
# Print — never run — the command sequence that lands a stack of pull requests
# into the default branch, bottom-up and one at a time. PRs are given bottom-up:
# the first one is based on the default branch, each later one on the PR before it.
#
# Why it exists: a stacked PR keeps its feature-branch base until that base is
# merged AND GitHub has retargeted it. Merging the stack in parallel, or before
# the retarget lands, merges a PR into its feature base instead of the default
# branch, and the work never reaches it. Every merge line printed here is
# therefore followed by a wait until GitHub reports it merged, and every PR is
# preceded by a base check that retargets it when its base is not <base>.
#
# Options:
#   --repo <owner/name>  the repository (required). `-` resolves it with
#                        `gh repo view` — the only mode that touches the network.
#   --base <branch>      the default branch the stack lands on (default: main).
#   --retarget           also print `gh pr edit <pr> --base <base>` for every
#                        stacked PR (all but the first) before the first merge.
#   --merge-flags "<f>"  flags for each `gh pr merge` line
#                        (default: "--squash --delete-branch").
#
# The merges stay ASK-gated: this script prints the sequence for the operator,
# who runs it line by line. Bash 3.2-compatible (macOS).
#
# Exit: 0 = printed, 1 = `--repo -` could not resolve the repository,
#       2 = usage error.

set -eu

usage() {
  cat <<'USAGE'
usage: land-stack.sh [--repo <owner/name>|-] [--base <branch>] [--retarget]
                     [--merge-flags "<flags>"] <pr> [<pr> ...]
  PRs bottom-up; prints the landing sequence, never runs it.
USAGE
}

die_usage() {
  printf 'land-stack: %s\n' "$1" >&2
  usage >&2
  exit 2
}

repo=""
base="main"
retarget=0
merge_flags="--squash --delete-branch"
prs=""
count=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --repo)
      [ "$#" -ge 2 ] || die_usage "--repo needs a value"
      repo="$2"
      shift 2
      ;;
    --base)
      [ "$#" -ge 2 ] || die_usage "--base needs a value"
      base="$2"
      shift 2
      ;;
    --retarget)
      retarget=1
      shift
      ;;
    --merge-flags)
      [ "$#" -ge 2 ] || die_usage "--merge-flags needs a value"
      merge_flags="$2"
      shift 2
      ;;
    --)
      shift
      break
      ;;
    -*)
      die_usage "unknown option: $1"
      ;;
    *)
      break
      ;;
  esac
done

for pr in "$@"; do
  case "$pr" in
    '' | *[!0-9]* | 0*) die_usage "not a PR number: '$pr'" ;;
  esac
  case " $prs " in
    *" $pr "*) die_usage "PR $pr given twice" ;;
  esac
  prs="${prs:+$prs }$pr"
  count=$((count + 1))
done

[ "$count" -gt 0 ] || die_usage "no PRs given"
[ -n "$repo" ] || die_usage "--repo is required (pass <owner/name>, or - to ask gh)"

if [ "$repo" = "-" ]; then
  if ! repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"; then
    printf 'land-stack: gh repo view could not resolve the repository\n' >&2
    exit 1
  fi
fi

# Every value below is printed into shell commands the operator runs, so each
# one is held to a charset that cannot break out of its word.
case "$repo" in
  */*/* | /* | */ | *[!A-Za-z0-9._/-]*) die_usage "repo must look like <owner/name>: '$repo'" ;;
  */*) ;;
  *) die_usage "repo must look like <owner/name>: '$repo'" ;;
esac
case "$base" in
  '' | -* | *[!A-Za-z0-9._/-]*) die_usage "not a branch name: '$base'" ;;
esac
case "$merge_flags" in
  *[!A-Za-z0-9\ =._/-]*) die_usage "merge flags may hold only letters, digits, space and = . _ / -" ;;
esac

printf '# Land %d stacked PR(s) into %s on %s, bottom-up.\n' "$count" "$base" "$repo"
printf '# Run one line at a time; do not start a merge before the previous wait returns.\n'

if [ "$retarget" -eq 1 ] && [ "$count" -gt 1 ]; then
  printf '\n# Retarget every stacked PR to %s before the first merge.\n' "$base"
  first=1
  for pr in $prs; do
    if [ "$first" -eq 1 ]; then
      first=0
      continue
    fi
    printf 'gh pr edit %s --repo %s --base %s\n' "$pr" "$repo" "$base"
  done
fi

# The printed lines carry a literal "$(gh api …)" for the operator's shell to
# expand; this script never expands it.
# shellcheck disable=SC2016
for pr in $prs; do
  printf '\n# PR %s\n' "$pr"
  printf '[ "$(gh api repos/%s/pulls/%s --jq .base.ref)" = "%s" ] || gh pr edit %s --repo %s --base %s\n' \
    "$repo" "$pr" "$base" "$pr" "$repo" "$base"
  printf 'gh pr merge %s --repo %s %s\n' "$pr" "$repo" "$merge_flags"
  printf 'until [ "$(gh api repos/%s/pulls/%s --jq .merged)" = true ]; do sleep 5; done\n' "$repo" "$pr"
done
