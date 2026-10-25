#!/bin/bash
# Phase guard — PreToolUse hook on Edit / Write / MultiEdit / NotebookEdit / Bash.
#
# The first line of defence for a phase run: the phase doc lent into the run's
# worktree (<root>/.swarmery/plan/*.md) names, in its `kind: guard` block, the
# files this phase must not touch. This hook refuses a tool call that reaches
# one of them, BEFORE it runs:
#
#   Edit / Write (and MultiEdit / NotebookEdit) — by the target path, as
#     written and where it lands once symlinks are resolved;
#   Bash — by EVERY path-shaped token of the command, so `sed -i`, `tee`, a `>`
#     redirect, `chmod`, a heredoc body, `python -c` and a plain `cat` of a
#     protected file are all refused. Reading is refused too: a protected file
#     is read with the Read / Grep tools, which this hook does not watch.
#
# No silent exceptions: the only allowlist is the guard block's own `allow:`
# list, which the daemon writes when the operator answers a guard decision with
# allow-add-to-scope — an allowed path is exempt from the doc's protected_paths,
# never from the implicit globs below. On top of the doc's globs, the guard's
# own inputs are always protected: .swarmery/plan/** (the doc that carries the
# guard) and .swarmery/guard.log (the record of what it refused), plus the
# .swarmery and .swarmery/plan directory names themselves — removing either
# would switch the guard off. One opening, so a guarded run can still tick its
# criteria and write its Completion Report: an Edit, MultiEdit or Write of a
# lent doc (.swarmery/plan/<doc>.md, named directly, no symlink) goes through
# when the doc's guard parses to the same globs and allow entries after the
# edit as before. Bash on the doc, and any edit that changes the guard, stay
# blocked.
#
# Exit codes (house protocol, see protect-sensitive-files.sh): 2 blocks the
# call and the reason goes to STDERR, which is the only stream that reaches the
# model; 0 allows it.
#   - No .swarmery/plan/ at or above the session cwd or $CLAUDE_PROJECT_DIR:
#     exit 0 at once, silently — a session outside a run never pays for this.
#   - No guard block in the doc: only the implicit protection applies.
#   - An unparsable guard block, a payload that is not JSON, or jq missing:
#     every call is blocked with the reason (fail closed — the hook cannot know
#     what is protected, so nothing is assumed safe).
#   - A glob that names a directory (`dir/` or `dir/**`): a warning on stderr;
#     it still protects the files under it, exactly as the Go parser keeps it.
#
# Every block appends one line to <root>/.swarmery/guard.log, never truncated:
#   <RFC3339 UTC ts>\t<tool>\t<path-or-token>\t<reason>
#
# Glob semantics: protected paths are root-relative globs. `**` is translated
# to bash's `*` (which already crosses "/"), and a `**/` also matches zero
# directories, so "**/*_test.go" covers "trap_test.go" at the root. A Bash
# token is resolved against the cwd first, so a protected file's bare
# basename typed from another directory does not match — only path-shaped
# tokens (with a "/" or a ".", or naming an existing file) are considered.
# Matching ignores case (a case-insensitive file system resolves both).
#
# Limits, stated so nobody mistakes this for a sandbox: a command that reaches
# a protected file without naming it (`rm pkg/*`, `find … -exec`, a variable,
# `git checkout .`) is not seen here. This hook is the early, explained "no";
# the run's diff and guard.log are what the later stages verify.
#
# bash 3.2 ONLY (/bin/bash on macOS): no ${v,,}, no declare -A, no mapfile.
# Parser: lib/guard-parse.sh. Covered by scripts/tests/phase-guard.test.sh.

set -u
shopt -s nocasematch

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

input=$(cat)

have_jq=0
command -v jq >/dev/null 2>&1 && have_jq=1

hook_cwd=""
if [ "$have_jq" -eq 1 ]; then
  hook_cwd=$(printf '%s' "$input" | jq -r 'if type == "object" then (.cwd // "") | strings else "" end' 2>/dev/null)
fi
case "$hook_cwd" in
  /*) ;;
  *) hook_cwd="$PWD" ;;
esac

# The run root: the first directory at or above the cwd holding .swarmery/plan/
# — or at or above $CLAUDE_PROJECT_DIR, so a session that cd'd out of its
# worktree still meets the guard when it reaches back in by absolute path.
root=""
for start in "$hook_cwd" "${CLAUDE_PROJECT_DIR:-}"; do
  case "$start" in /*) ;; *) continue ;; esac
  d="$start"
  while :; do
    if [ -d "$d/.swarmery/plan" ]; then root="$d"; break; fi
    [ "$d" = "/" ] && break
    d="${d%/*}"
    [ -n "$d" ] || d="/"
  done
  [ -n "$root" ] && break
done
[ -n "$root" ] || exit 0
root="${root%/}"
[ -n "$root" ] || root="/"
root_phys=$(cd -P "$root" 2>/dev/null && pwd -P) || root_phys="$root"

LOG="$root/.swarmery/guard.log"
tool="unknown"

# oneline <text> — <text> with tabs, CRs and newlines turned into spaces, so a
# field can never break the guard.log line format.
oneline() {
  local s="$1"
  s="${s//$'\t'/ }"
  s="${s//$'\r'/ }"
  s="${s//$'\n'/ }"
  printf '%s' "$s"
}

# block <path-or-token> <reason> <message line>... — log one line, explain, exit 2.
block() {
  local what="$1" reason="$2"
  shift 2
  printf '%s\t%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(oneline "$tool")" \
    "$(oneline "$what")" "$(oneline "$reason")" >> "$LOG" 2>/dev/null || true
  local line
  for line in "$@"; do printf '%s\n' "$line" >&2; done
  exit 2
}

if [ "$have_jq" -eq 0 ]; then
  block "-" "jq missing" "phase-guard: jq is missing, so the phase doc's guard cannot be applied — blocking (see .swarmery/plan/)"
fi
if ! printf '%s' "$input" | jq -e 'type == "object"' >/dev/null 2>&1; then
  block "-" "hook input is not a JSON object" "phase-guard: hook input is not a JSON object — blocking"
fi

tool=$(printf '%s' "$input" | jq -r '.tool_name // "" | strings' 2>/dev/null)
case "$tool" in
  Edit|Write|MultiEdit|NotebookEdit|Bash) ;;
  *) exit 0 ;;
esac

# ── The protected globs: the guard's own inputs, then every doc's block ──
# kinds[i] says where globs[i] came from; it is the guard.log reason.
globs=(".swarmery/plan/**" ".swarmery/guard.log" ".swarmery/plan" ".swarmery")
kinds=("implicit" "implicit" "implicit" "implicit")
# allows: the guard blocks' `allow:` entries (guard_parse prints them as "!…").
allows=()

# shellcheck source=lib/guard-parse.sh
. "$HOOK_DIR/lib/guard-parse.sh"

for doc in "$root/.swarmery/plan"/*.md; do
  [ -f "$doc" ] || continue
  out=$(guard_parse "$doc")
  rc=$?
  case "$rc" in
    0)
      while IFS= read -r g; do
        [ -n "$g" ] || continue
        case "$g" in
          '!'*) allows+=("${g#!}"); continue ;;
        esac
        globs+=("$g")
        kinds+=("protected_paths")
      done <<< "$out"
      ;;
    1) ;;
    *)
      [ "$rc" -eq 3 ] || out="the guard parser failed (exit $rc)"
      what="-"
      if [ "$tool" != "Bash" ]; then
        what=$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_input.notebook_path // .tool_input.path // "-" | strings' 2>/dev/null)
      fi
      block "${what:--}" "${doc##*/}: $out" \
        "phase-guard: the guard block in .swarmery/plan/${doc##*/} cannot be read — $out" \
        "phase-guard: every Edit, Write and Bash call is blocked until the planner fixes it; read files with Read/Grep (see .swarmery/plan/)"
      ;;
  esac
done

# ── Path arithmetic (no forks: a heredoc can carry hundreds of tokens) ──

# _norm <absolute path> — NORM: the path with ".", ".." and empty segments
# folded lexically. Parameter expansion only: bash 3.2 backs a here-string with
# a temp file, which a few thousand tokens turn into seconds.
_norm() {
  local rest="$1" seg n=0 i=0
  local -a stack
  case "$rest/" in
    */./*|*/../*|*//*) ;;
    *) NORM="${rest%/}"; [ -n "$NORM" ] || NORM="/"; return 0 ;;
  esac
  while [ -n "$rest" ]; do
    case "$rest" in
      */*) seg="${rest%%/*}"; rest="${rest#*/}" ;;
      *) seg="$rest"; rest="" ;;
    esac
    case "$seg" in
      ''|.) ;;
      ..) [ "$n" -gt 0 ] && n=$((n - 1)) ;;
      *) stack[n]="$seg"; n=$((n + 1)) ;;
    esac
  done
  NORM=""
  while [ "$i" -lt "$n" ]; do NORM="$NORM/${stack[i]}"; i=$((i + 1)); done
  [ -n "$NORM" ] || NORM="/"
}

# _to_rel <path> — REL: <path> (absolute, or relative to the cwd) relative to
# the run root. Returns 1 for the root itself and for anything outside it.
_to_rel() {
  local p="$1" r
  case "$p" in
    /*) ;;
    *) p="$hook_cwd/$p" ;;
  esac
  _norm "$p"
  for r in "$root" "$root_phys"; do
    [ "$r" = "/" ] && r=""
    case "$NORM/" in
      "$r"/?*) REL="${NORM:${#r}+1}"; return 0 ;;
    esac
  done
  return 1
}

# _glob_match <rel> <glob> — whether <rel> matches one protected-path glob.
_glob_match() {
  local rel="$1" g="$2" p1 p2
  case "$g" in */) g="$g**" ;; esac
  p1=${g//\*\*/*}
  # shellcheck disable=SC2053  # the right side is a glob on purpose
  [[ $rel == $p1 ]] && return 0
  case "$g" in
    *'**/'*)
      p2=${g//\*\*\//}
      p2=${p2//\*\*/*}
      # shellcheck disable=SC2053
      [[ $rel == $p2 ]] && return 0
      ;;
  esac
  return 1
}

# _allowed <rel> — whether <rel> matches an `allow:` entry.
_allowed() {
  local a
  for a in ${allows[@]+"${allows[@]}"}; do
    _glob_match "$1" "$a" && return 0
  done
  return 1
}

# _protected <rel> — HIT_GLOB / HIT_KIND: the first glob <rel> matches. A
# protected_paths hit on an allowed path does not count; an implicit one does.
_protected() {
  local rel="$1" i=0
  while [ "$i" -lt "${#globs[@]}" ]; do
    if _glob_match "$rel" "${globs[i]}"; then
      if [ "${kinds[i]}" = "implicit" ] || ! _allowed "$rel"; then
        HIT_GLOB="${globs[i]}"; HIT_KIND="${kinds[i]}"; return 0
      fi
    fi
    i=$((i + 1))
  done
  return 1
}

# _plan_doc_edit_ok <rel> — whether this Edit / MultiEdit / Write of a lent
# phase doc (.swarmery/plan/<doc>.md, no symlink on the way) leaves the doc's
# guard exactly as it was: the edit is applied to a copy and the guard parser
# must print the same globs and allow entries, with the same status, before and
# after. That is what lets the executor tick its criteria and write its
# Completion Report while the guard itself stays out of reach. An edit that
# cannot be applied (no old_string, no match) is refused.
_plan_doc_edit_ok() {
  local rel="$1" doc tmp before after rc_before rc_after
  case "$tool" in Edit|MultiEdit|Write) ;; *) return 1 ;; esac
  case "$rel" in
    .swarmery/plan/*/*) return 1 ;;
    .swarmery/plan/*.md) ;;
    *) return 1 ;;
  esac
  doc="$root/$rel"
  [ -f "$doc" ] && [ ! -L "$doc" ] || return 1
  [ "$(cd -P "$root/.swarmery/plan" 2>/dev/null && pwd -P)" = "${root_phys%/}/.swarmery/plan" ] || return 1
  tmp=$(mktemp "${TMPDIR:-/tmp}/phase-guard.XXXXXX") || return 1
  # jq's split/join keeps this byte-exact on non-ASCII text (index() would not).
  if ! printf '%s' "$input" | jq -j --rawfile doc "$doc" '
      def apply($s; $e):
        ($e.old_string // "" | strings) as $o
        | ($e.new_string // "" | strings) as $n
        | if $o == "" then error("no old_string") else . end
        | ($s | split($o)) as $parts
        | if ($parts | length) < 2 then error("old_string not found")
          elif ($e.replace_all // false) then $parts | join($n)
          else $parts[0] + $n + ($parts[1:] | join($o)) end;
      .tool_name as $t | .tool_input as $in
      | if $t == "Write" then ($in.content // error("no content") | strings)
        elif $t == "Edit" then apply($doc; $in)
        else reduce (($in.edits // []) | .[]) as $e ($doc; apply(.; $e)) end' > "$tmp" 2>/dev/null; then
    rm -f "$tmp"
    return 1
  fi
  before=$(guard_parse "$doc"); rc_before=$?
  after=$(guard_parse "$tmp"); rc_after=$?
  rm -f "$tmp"
  [ "$rc_before" -eq "$rc_after" ] && [ "$before" = "$after" ]
}

refuse() {
  local rel="$1" extra="${2:-}"
  local -a lines=(
    "phase-guard: $rel is protected by the phase doc; read it with Read/Grep, changes to it are outside this phase's scope (see .swarmery/plan/)"
    "phase-guard: it matches the $HIT_KIND glob \"$HIT_GLOB\""
  )
  [ -n "$extra" ] && lines+=("$extra")
  block "$rel" "$HIT_KIND \"$HIT_GLOB\"" "${lines[@]}"
}

# ── Edit / Write: every target path, as written and where it lands ──
if [ "$tool" != "Bash" ]; then
  while IFS= read -r -d '' p; do
    cur="$p"
    case "$cur" in /*) ;; *) cur="$hook_cwd/$cur" ;; esac
    hops=0
    while :; do
      if _to_rel "$cur" && _protected "$REL"; then
        # The lent doc itself, named directly: ticks and the report go through
        # when the guard parses the same after the edit.
        if [ "$hops" -eq 0 ] && [ "$HIT_GLOB" = ".swarmery/plan/**" ] && _plan_doc_edit_ok "$REL"; then
          break
        fi
        refuse "$REL"
      fi
      # Where the write lands: the existing directory prefix through symlinks.
      _norm "$cur"
      dir="$NORM"
      while [ ! -d "$dir" ]; do dir="${dir%/*}"; [ -n "$dir" ] || dir="/"; done
      phys_dir=$(cd -P "$dir" 2>/dev/null && pwd -P) || phys_dir="$dir"
      land="${phys_dir%/}${NORM:${#dir}}"
      [ "$dir" = "/" ] && land="${phys_dir%/}$NORM"
      if [ "$land" != "$NORM" ] && _to_rel "$land" && _protected "$REL"; then refuse "$REL"; fi
      [ -L "$land" ] || break
      hops=$((hops + 1))
      [ "$hops" -le 8 ] || block "$p" "symlink chain too deep" "phase-guard: $p is a symlink chain too deep to follow — blocking"
      target=$(readlink "$land") || break
      case "$target" in /*) cur="$target" ;; *) cur="${land%/*}/$target" ;; esac
    done
  done < <(printf '%s' "$input" | jq -j '
    [ .tool_input.file_path, .tool_input.path, .tool_input.notebook_path,
      ((.tool_input.edits // []) | if type == "array" then .[] | objects | .file_path else empty end) ]
    | .[] | strings | select(. != "") | . + "\u0000"' 2>/dev/null)
  exit 0
fi

# ── Bash: every path-shaped token of the command ──
command_text=$(printf '%s' "$input" | jq -r '.tool_input.command // "" | strings' 2>/dev/null)
[ -n "$command_text" ] || exit 0

# Separators become spaces: ; & | ( ) quotes < > = , : backticks braces
# backslashes, tabs and newlines (a heredoc body is just more tokens).
flat=$(printf '%s' "$command_text" | tr ";&|()\"'<>=,:\`{}\\\\\t\r\n" '                   ')
read -r -a toks <<< "$flat"

for t in ${toks[@]+"${toks[@]}"}; do
  while [ "${t#./}" != "$t" ]; do t="${t#./}"; done
  while [ "${#t}" -gt 1 ] && [ "${t%/}" != "$t" ]; do t="${t%/}"; done
  [ -n "$t" ] || continue
  case "$t" in
    */*|*.*) ;;
    *) [ -e "$hook_cwd/$t" ] || continue ;;
  esac
  if _to_rel "$t" && _protected "$REL"; then
    refuse "$REL" "phase-guard: a Bash command that names a protected path is blocked even when it only reads it"
  fi
done
exit 0
