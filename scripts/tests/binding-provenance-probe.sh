#!/bin/bash
# binding-provenance-probe.sh — does a binding file that arrived from SOMEONE
# ELSE still choose this machine's Claude account, and still unlock that
# account's variable store?
#
# The binding lives in <project>/.claude/settings.local.json and is a
# machine-local tier by design: two engineers on one repo legitimately run
# different accounts, so the choice never belongs in a commit. Nothing used to
# enforce that. This script builds the leak's real shape on disk and reports what
# the binary under test does with it:
#
#   --case clone     a repository that COMMITS the binding, then `git clone`d.
#                    The file is tracked, so the provenance gate (Lock 1) must
#                    ignore it: the default account and zero store names.
#   --case tarball   the same tree extracted WITHOUT its .git. There is no
#                    repository to ask, so Lock 1 honours it — the measured
#                    residual that the store anchor (Lock 2) closes later. This
#                    case is expected to show the binding taking effect; that is
#                    a recorded number, not a failing test.
#
# Named *.sh and NOT *.test.sh on purpose: the CI suite discovers
# scripts/tests/*.test.sh, and this probe needs a built binary plus a live
# per-account store, neither of which exists in CI.
#
# WHAT IT NEVER DOES. It never prints a variable's value, and it never reads,
# copies or greps a store file. Every number below is a COUNT or a verdict word,
# so the output is safe to paste into a plan document or a pull request.
#
# Usage:
#   scripts/tests/binding-provenance-probe.sh \
#       --case clone|tarball --account <key> --estate <key> \
#       --prefix <NAME_PREFIX> --dir <parent> [--swarmery <bin>] [--keep]
#
#   --account   the account key the fixture's binding names. Validated with the
#               rules of claudeacct.ValidKey, so the probe never measures a key
#               the binary would reject anyway.
#   --estate    the estate key written alongside it, so the fixture matches the
#               shape a real project commits. Validated the same way.
#   --prefix    the NAME prefix counted in the child environment (letters, digits
#               and _ only). An argument and never a literal, so no project's
#               variable naming is baked in.
#   --dir       an empty parent directory for the fixture. Refused when it sits
#               inside a git work tree, or under a Claude Code config dir.
#   --swarmery  the binary under test. Defaults to the installed one, so the
#               same command measures before and after an install.
#   --keep      leave the fixture behind for inspection.
set -uo pipefail

# Every git command below — the work-tree refusal included — must describe the
# directory it is pointed at. An inherited GIT_DIR (a hook context, say) would
# make the refusal fail open and could add the fixture to another repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_CEILING_DIRECTORIES

case_name=""
account=""
estate=""
prefix=""
parent=""
swarmery_bin="$HOME/.swarmery/bin/swarmery"
keep=0

die() { printf 'binding-provenance-probe: %s\n' "$1" >&2; exit "${2:-1}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --case)     case_name="${2:-}"; shift 2 ;;
    --account)  account="${2:-}"; shift 2 ;;
    --estate)   estate="${2:-}"; shift 2 ;;
    --prefix)   prefix="${2:-}"; shift 2 ;;
    --dir)      parent="${2:-}"; shift 2 ;;
    --swarmery) swarmery_bin="${2:-}"; shift 2 ;;
    --keep)     keep=1; shift ;;
    -h|--help)  sed -n '1,46p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *)          die "unknown argument: $1" ;;
  esac
done

case "$case_name" in
  clone|tarball) ;;
  *) die "--case must be clone or tarball" ;;
esac
[ -n "$account" ] || die "--account is required"
[ -n "$estate" ]  || die "--estate is required"
[ -n "$prefix" ]  || die "--prefix is required"
[ -n "$parent" ]  || die "--dir is required"

# valid_key mirrors claudeacct.ValidKey: not empty, not . or .., no leading dot,
# no / or \, no .., no whitespace or control characters.
valid_key() {
  case "$1" in
    ''|.|..|.*|*/*|*\\*|*..*|*[[:space:][:cntrl:]]*) return 1 ;;
  esac
}
valid_key "$account" || die "--account is not a valid account key: $account"
valid_key "$estate"  || die "--estate is not a valid key: $estate"
case "$prefix" in
  *[!A-Za-z0-9_]*) die "--prefix must be letters, digits and _ only: $prefix" ;;
esac

# json_string quotes a validated key for the fixture's JSON. valid_key already
# refuses \ and control characters, so " is the one character left to escape.
json_string() { printf '"%s"' "${1//\"/\\\"}"; }

# ── refusals ────────────────────────────────────────────────────────────────
# Both are about where the fixture would LAND, so they run before anything is
# created — and they must hold for a --dir that does not exist yet, which is why
# the checks walk to the nearest existing ancestor instead of stat-ing --dir.
#
# A fixture inside a git work tree would commit a binding into a real repository
# (this repo included) on the next careless `git add -A`. A fixture under a
# Claude Code config dir would put a settings file where the CLI reads one.
existing="$parent"
while [ ! -d "$existing" ]; do
  next=$(dirname "$existing")
  [ "$next" = "$existing" ] && break
  existing="$next"
done
existing_abs=$(cd "$existing" 2>/dev/null && pwd) || die "--dir has no reachable ancestor: $parent" 2

case "$existing_abs/" in
  "$HOME/.claude"*/ | "$HOME"/.claude*/*) die "--dir is under a Claude Code config dir: $parent" 2 ;;
esac
if git -C "$existing_abs" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  die "--dir is inside a git work tree: $parent" 2
fi

[ -x "$swarmery_bin" ] || die "not an executable binary: $swarmery_bin"

mkdir -p "$parent" || die "cannot create $parent"
parent_abs=$(cd "$parent" && pwd)
src="$parent_abs/src"
[ -e "$src" ] && die "fixture already exists: $src (remove it, or pick another --dir)"

cleanup() { [ "$keep" -eq 1 ] || rm -rf "$parent_abs/src" "$parent_abs/clone" "$parent_abs/tarball"; }
trap cleanup EXIT

# ── the fixture ─────────────────────────────────────────────────────────────
# A repository that COMMITS the binding. `git add -f` is not optional: a global
# core.excludesfile that ignores .claude/settings.local.json is common, and
# without -f the commit would silently be empty and the probe would measure the
# untracked case while reporting the clone one.
mkdir -p "$src/.claude" || die "cannot create $src/.claude"
printf '{\n  "swarmery": {\n    "claudeAccount": %s,\n    "estate": %s\n  }\n}\n' \
  "$(json_string "$account")" "$(json_string "$estate")" > "$src/.claude/settings.local.json"

git -C "$src" init -q . >/dev/null 2>&1 || die "git init failed in $src"
git -C "$src" add -f -- .claude/settings.local.json >/dev/null 2>&1 \
  || die "git add failed in $src"
git -C "$src" \
  -c user.name='binding provenance probe' \
  -c user.email='probe@example.invalid' \
  -c commit.gpgsign=false \
  commit -qm 'commit a machine-local binding (the leak under test)' >/dev/null 2>&1 \
  || die "git commit failed in $src"

if ! git -C "$src" ls-files --error-unmatch -- .claude/settings.local.json >/dev/null 2>&1; then
  die "the fixture's binding is not tracked — the clone case cannot be measured"
fi

case "$case_name" in
  clone)
    git -C "$parent_abs" clone -q src clone >/dev/null 2>&1 || die "git clone failed"
    subject="$parent_abs/clone"
    ;;
  tarball)
    # The extracted-archive shape: the whole tree WITHOUT its .git, so there is
    # no repository for the gate to ask.
    mkdir -p "$parent_abs/tarball" || die "cannot create the tarball dir"
    ( cd "$src" && tar --exclude ./.git -cf - . ) \
      | ( cd "$parent_abs/tarball" && tar -xf - ) \
      || die "tar extraction failed"
    [ -d "$parent_abs/tarball/.git" ] && die "the tarball case still carries a .git"
    subject="$parent_abs/tarball"
    ;;
esac

[ -f "$subject/.claude/settings.local.json" ] || die "no binding file in $subject"

# ── measurement ─────────────────────────────────────────────────────────────
# `env -u CLAUDE_CONFIG_DIR` everywhere: an ignored binding means UNBOUND, and an
# unbound spawn is a passthrough — so a CLAUDE_CONFIG_DIR inherited from the
# operator's own shell would survive the gate and read as a leak that is not one.
probe_env=(env -u CLAUDE_CONFIG_DIR)

printf 'case=%s\n' "$case_name"

# prefixCount — how many variables whose NAME starts with the prefix the child
# received. A count, never a name, never a value. `grep -c` exits 1 on zero
# matches, so the fallback keeps `set -o pipefail` from turning "clean" into a
# blank field. The prefix travels as a positional argument, never spliced into
# the script text the child shell parses.
# shellcheck disable=SC2016  # $1 is the CHILD shell's positional parameter.
prefix_count=$("${probe_env[@]}" "$swarmery_bin" account exec --path "$subject" -- \
  sh -c 'env | grep -c "^$1"' _ "$prefix" 2>/dev/null) || prefix_count=""
case "$prefix_count" in ''|*[!0-9]*) prefix_count=0 ;; esac
printf 'prefixCount=%s\n' "$prefix_count"

# configDir — whether the child got a CLAUDE_CONFIG_DIR at all. The variable's
# PRESENCE is the whole answer; its value is never printed.
# shellcheck disable=SC2016  # ${CLAUDE_CONFIG_DIR+x} must be expanded by the
# CHILD shell the account exec launches — that child's environment IS the
# measurement. Expanding it here would report this script's own environment.
config_state=$("${probe_env[@]}" "$swarmery_bin" account exec --path "$subject" -- \
  sh -c 'if [ -n "${CLAUDE_CONFIG_DIR+x}" ]; then echo set; else echo unset; fi' 2>/dev/null) \
  || config_state=""
case "$config_state" in set|unset) ;; *) config_state="unknown" ;; esac
printf 'configDir=%s\n' "$config_state"

which_out=$("${probe_env[@]}" "$swarmery_bin" account which --path "$subject" 2>/dev/null) || which_out=""
which_err=$("${probe_env[@]}" "$swarmery_bin" account which --path "$subject" 2>&1 >/dev/null) || true

field() { printf '%s\n' "$which_out" | sed -n "s/^$1:[[:space:]]*//p" | head -1; }
printf 'whichAccount=%s\n' "$(field account)"
printf 'whichSource=%s\n' "$(field source)"
printf 'whichStderrBytes=%s\n' "$(printf '%s' "$which_err" | wc -c | tr -d ' ')"

# The reason the operator is shown. On this binary it arrives as a warn-once log
# line on stderr; a later phase adds an `ignored:` line on stdout, and both
# shapes are counted here so the same probe measures both.
ignored=$( { printf '%s\n' "$which_err" | grep -c 'IGNORING binding' || true; } )
ignored_stdout=$( { printf '%s\n' "$which_out" | grep -c '^ignored:' || true; } )
printf 'whichIgnoredLines=%s\n' "$((ignored + ignored_stdout))"

not_admitted=$( { printf '%s\n%s\n' "$which_out" "$which_err" | grep -c 'not admitted by' || true; } )
printf 'whichNotAdmittedLines=%s\n' "$not_admitted"

if [ "$keep" -eq 1 ]; then
  printf 'fixtureKept=%s\n' "$parent_abs"
fi
