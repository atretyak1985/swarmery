#!/bin/bash
# project-scope.sh — is a path inside this project? Sourced, never executed.
#
# WHY IT EXISTS. Several hooks walk up from a file the session touched to the
# nearest project marker (.git, package.json, pyproject.toml) and then run that
# project's own tooling: git, the local prettier/eslint, `npm run typecheck`, a
# .venv's black. A session can read or edit a file in a folder the operator did
# not write — a downloaded archive, a third-party checkout next to the project —
# and that folder's tooling and git config belong to whoever wrote it. So the
# hooks act only inside the scope this file defines, and never walk above it.
#
# THE SCOPE is a set of roots, each resolved physically (cd -P / pwd -P; stock
# macOS has no `realpath`/`readlink -f`):
#   - $CLAUDE_PROJECT_DIR;
#   - every repos[] entry of $CLAUDE_PROJECT_DIR/.claude/project.json that is an
#     existing directory (absolute, or relative to the project dir), which is how
#     a multi-repo workspace declares checkouts that live beside the project.
# A root that is "/" or $HOME (or an ancestor of $HOME) is dropped: it would
# make every file on the machine, or in the user's home, "in the project".
# No CLAUDE_PROJECT_DIR means no scope at all — the hooks then do nothing.
#
# A path with a ".." segment is never in scope: it is judged by where the
# write lands, and a textual ".." can climb out of a root before it is resolved.
#
# bash 3.2 ONLY (/bin/bash on macOS): no ${v,,}, no declare -A, no mapfile.
# Needs jq only to read repos[]; without jq the scope is the project dir alone.
# Covered by scripts/tests/hooks-project-scope.test.sh.

# ps_physical <path> — <path> made absolute (against $PWD), its existing
# directory prefix resolved through every symlink, the part that does not exist
# yet appended as written. Returns 1 for an empty path or one with a ".." segment.
ps_physical() {
  local p="$1" dir rest phys
  [ -n "$p" ] || return 1
  case "$p" in
    /*) ;;
    *) p="${PWD%/}/$p" ;;
  esac
  case "/$p/" in
    */../*) return 1 ;;
  esac
  dir="$p"
  while [ ! -d "$dir" ]; do dir=$(dirname "$dir"); done
  rest="${p#"${dir%/}"}"
  phys=$(cd -P "$dir" 2>/dev/null && pwd -P) || return 1
  printf '%s%s\n' "${phys%/}" "$rest"
}

# _ps_too_wide <physical-dir> — true when <dir> is "/" or $HOME or an ancestor
# of $HOME: a scope root that wide is no scope at all.
_ps_too_wide() {
  local d="${1%/}" home
  [ -n "$d" ] || return 0
  home=$(cd -P "${HOME:-/}" 2>/dev/null && pwd -P) || home="${HOME:-}"
  home="${home%/}"
  [ -n "$home" ] || return 1
  case "$home/" in
    "$d"/*) return 0 ;;
  esac
  return 1
}

# ps_roots — every scope root, physical, one per line.
ps_roots() {
  local proj pj entry cand phys
  [ -n "${CLAUDE_PROJECT_DIR:-}" ] || return 0
  proj=$(cd -P "$CLAUDE_PROJECT_DIR" 2>/dev/null && pwd -P) || return 0
  _ps_too_wide "$proj" || printf '%s\n' "$proj"
  pj="$proj/.claude/project.json"
  if [ ! -f "$pj" ] || ! command -v jq >/dev/null 2>&1; then
    return 0
  fi
  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    case "$entry" in
      /*) cand="$entry" ;;
      *) cand="$proj/$entry" ;;
    esac
    phys=$(cd -P "$cand" 2>/dev/null && pwd -P) || continue
    _ps_too_wide "$phys" && continue
    printf '%s\n' "$phys"
  done < <(jq -r '(.repos // []) | if type == "array" then .[] | strings else empty end' "$pj" 2>/dev/null)
}

# scope_root_for <path> — the OUTERMOST scope root containing <path>, physical.
# Outermost, not innermost: a repos[] entry nested inside the project dir must
# not stop a walk that used to reach the project's own marker. Returns 1 when
# <path> is outside every root.
scope_root_for() {
  local phys root best=""
  phys=$(ps_physical "$1") || return 1
  while IFS= read -r root; do
    [ -n "$root" ] || continue
    case "$phys/" in
      "$root"/*)
        if [ -z "$best" ] || [ "${#root}" -lt "${#best}" ]; then best="$root"; fi
        ;;
    esac
  done < <(ps_roots)
  [ -n "$best" ] || return 1
  printf '%s\n' "$best"
}

# in_project_scope <path> — true when <path> lies under a scope root.
in_project_scope() {
  scope_root_for "$1" >/dev/null
}

# ps_tool_in_scope <path> — true when an executable a hook is about to run
# lives in scope: its directory resolves inside a root and, when the file itself
# is a symlink, so does every hop of the link (up to 8).
ps_tool_in_scope() {
  local p="$1" hops=0 target
  in_project_scope "$p" || return 1
  while [ -L "$p" ]; do
    hops=$((hops + 1))
    [ "$hops" -le 8 ] || return 1
    target=$(readlink "$p") || return 1
    case "$target" in
      /*) p="$target" ;;
      *) p="$(dirname "$p")/$target" ;;
    esac
    in_project_scope "$p" || return 1
  done
  return 0
}
