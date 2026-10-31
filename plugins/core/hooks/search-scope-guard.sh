#!/bin/bash
# Search-scope guard — PreToolUse hook on Grep/Glob.
#
# The `code-search` skill (plugins/core/skills/code-search/resources/
# search-procedure.md) documents three "Ripgrep search timed out after 20
# seconds" failure modes — 4a (pattern narrow, `path` too broad), 4b (target
# deliberately inside node_modules/vendor/build — no exclusion possible), 4c
# (incidental noise-directory sweep) — but the skill carries
# `disable-model-invocation: true` and is only ever read by `researcher`/
# `implementation-agent`-class agents. Every other agent calls the built-in
# Grep/Glob tools directly and never sees that guidance. This hook fires at
# the moment of the actual tool call, for every agent, independent of whether
# the skill was ever loaded.
#
# It cannot change the built-in tools' internal ripgrep timeout (~20s, fixed,
# not configurable from this repo) — the only lever here is pre-empting the
# bad call shape before the timeout happens.
#
# WARN-MODE BURN-IN (same convention as bash-shape-guard.sh, see
# docs/GATE-HARDENING.md): every rule is new, starts in `warn`, logs its
# decision, and never blocks until a filled row argues the flip.
# ── enforcement, per rule ─────────────────────────────────────────
rule_mode() {
  case "$1" in
    glob-broad-path)      printf 'warn' ;;
    glob-dependency-tree) printf 'warn' ;;
    grep-unscoped-root)   printf 'warn' ;;
    *)                    printf 'warn' ;;
  esac
}
ENFORCE_FROM="2026-12-01"   # review deadline; see docs/GATE-HARDENING.md

LOG_BASENAME="search-scope-guard.jsonl"
LOG_FIELD_MAX=200

set -uo pipefail

input=$(cat)
tool_name=$(printf '%s' "$input" | jq -r '.tool_name // empty' 2>/dev/null)

# Nothing to judge outside Grep/Glob — the hooks.json matcher already scopes
# this, but a hook must not assume its own wiring.
case "$tool_name" in
  Grep|Glob) ;;
  *) exit 0 ;;
esac

pattern=$(printf '%s' "$input" | jq -r '.tool_input.pattern // empty' 2>/dev/null)
path=$(printf '%s' "$input" | jq -r '.tool_input.path // empty' 2>/dev/null)
glob=$(printf '%s' "$input" | jq -r '.tool_input.glob // empty' 2>/dev/null)
session_id=$(printf '%s' "$input" | jq -r '.session_id // empty' 2>/dev/null)
hook_cwd=$(printf '%s' "$input" | jq -r '.cwd // empty' 2>/dev/null)

[ -z "$pattern" ] && exit 0

guard_log_file() {
  if [ -n "${SEARCH_SCOPE_GUARD_LOG:-}" ]; then
    printf '%s' "$SEARCH_SCOPE_GUARD_LOG"
  elif [ -n "${AGENT_PROJECT:-}" ]; then
    printf '%s/%s/workspace/metrics/%s' \
      "${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}" "$AGENT_PROJECT" "$LOG_BASENAME"
  elif [ -n "${CLAUDE_PROJECT_DIR:-}" ]; then
    printf '%s/.claude-workspace/metrics/%s' "${CLAUDE_PROJECT_DIR%/}" "$LOG_BASENAME"
  else
    printf '%s/swarmery-guard/%s' "${TMPDIR:-/tmp}" "$LOG_BASENAME"
  fi
}

log_decision() {
  local rule="$1" decision="$2" logfile dir
  logfile=$(guard_log_file)
  dir=$(dirname "$logfile")
  mkdir -p "$dir" 2>/dev/null || return 0
  jq -cn \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg rule "$rule" \
    --arg decision "$decision" \
    --arg session "$session_id" \
    --arg cwd "$hook_cwd" \
    --arg tool "$tool_name" \
    --arg pattern "${pattern:0:$LOG_FIELD_MAX}" \
    --arg path "${path:0:$LOG_FIELD_MAX}" \
    --arg glob "${glob:0:$LOG_FIELD_MAX}" \
    '{ts:$ts,hook:"search-scope-guard",rule:$rule,decision:$decision,session:$session,cwd:$cwd,
      tool:$tool,pattern:$pattern,path:$path,glob:$glob}' \
    >> "$logfile" 2>/dev/null || true
  return 0
}

# warn <rule-id> <headline> [extra lines…] — same contract as
# bash-shape-guard.sh's refuse(): exit 2 only once a rule is flipped to
# `block`; every rule here ships in `warn`, so this always exits 0.
warn() {
  local rule="$1"; shift
  local mode exit_code
  mode=$(rule_mode "$rule")
  if [ "$mode" = "block" ]; then exit_code=2; else exit_code=0; fi
  log_decision "$rule" "$mode"
  if [ "$exit_code" -eq 0 ]; then
    printf '⚠️  WARN (enforce from %s): [%s] %s\n' "$ENFORCE_FROM" "$rule" "$1" >&2
  else
    printf '🚫 BLOCKED: [%s] %s\n' "$rule" "$1" >&2
  fi
  shift
  local line
  for line in "$@"; do
    printf '%s\n' "$line" >&2
  done
  exit "$exit_code"
}

# is_dep_tree_path <text> — does this string name a dependency/build tree as a
# path segment (not a substring match — "vendor" must not fire on a source
# folder literally named "vendoring"). Checked against both `pattern` and
# `path`: Mode 4b is "the tree itself is the target", which can show up in
# either field.
is_dep_tree_path() {
  printf '%s' "$1" | grep -Eq '(^|/)(node_modules|vendor|\.pnpm|dist|build|\.git)(/|$)'
}

# literal_ancestor <pattern> — the directory prefix of `pattern` that contains
# no glob metacharacter (`*`, `?`, `[`), i.e. the part a directory walk could
# actually be scoped to. Empty when the pattern's first path segment already
# starts with a wildcard (nothing literal to push `path` down to).
literal_ancestor() {
  local pat="$1" head
  # Strip from the first metacharacter onward, then back up to the last `/`.
  head=$(printf '%s' "$pat" | sed -E 's/[*?[].*$//')
  case "$head" in
    */*) printf '%s' "${head%/*}" ;;
    *)   printf '' ;;
  esac
}

# path_covers_ancestor <path> <ancestor> — true when `path` is already at or
# below the literal ancestor directory, i.e. the Glob call is already scoped
# as narrowly as Mode 4a asks for. A suffix match on purpose: `path` may be
# absolute ("/repo/tools/.../migrations") while `ancestor` is the pattern's
# own relative spelling ("tools/.../migrations") — what matters is that `path`
# ends in that literal directory, not that the two strings are equal.
path_covers_ancestor() {
  local p="$1" anc="$2"
  case "$p" in
    "$anc") return 0 ;;
    */"$anc") return 0 ;;
  esac
  return 1
}

if [ "$tool_name" = "Glob" ]; then
  fallback_path="${path:-.}"
  if is_dep_tree_path "$pattern" || is_dep_tree_path "$path"; then
    # Mode 4b: the dependency/build tree IS the target. No `path` narrowing is
    # possible — excluding it would exclude what is being searched for — so
    # this rule never suggests one. It only surfaces the Bash fallback once,
    # instead of letting the same Glob call get retried blind into the same
    # timeout.
    warn "glob-dependency-tree" \
      "this Glob pattern targets a dependency/build tree on purpose (node_modules, vendor, .pnpm, dist, build, or .git)." \
      "That is Mode 4b in plugins/core/skills/code-search/resources/search-procedure.md:" \
      "no exclusion helps here, because the tree being walked IS the target, and the" \
      "built-in Glob's internal ripgrep timeout (~20s) is fixed and not configurable" \
      "from this repo." \
      "" \
      "If this call times out, do not retry it as-is — fall back to Bash with an" \
      "explicit, adjustable timeout instead, e.g.:" \
      "" \
      "  rg --files $fallback_path 2>/dev/null | grep -E '<the literal part of your pattern>'" \
      "  find $fallback_path -path '*<your pattern, shell-globbed>*'"
  else
    ancestor=$(literal_ancestor "$pattern")
    if [ -n "$ancestor" ]; then
      broad=1
      if [ -z "$path" ] || [ "$path" = "." ]; then
        broad=1
      elif path_covers_ancestor "$path" "$ancestor"; then
        broad=0
      fi
      if [ "$broad" -eq 1 ]; then
        warn "glob-broad-path" \
          "this Glob's pattern has a literal ancestor directory, but \`path\` isn't scoped to it." \
          "Glob's directory walk is driven by \`path\`, not by the literal prefix already" \
          "baked into \`pattern\` — a narrow pattern at a broad \`path\` still walks the" \
          "whole tree and can hit the built-in tool's internal ripgrep timeout (~20s)." \
          "This is Mode 4a in plugins/core/skills/code-search/resources/search-procedure.md." \
          "" \
          "  pattern: $pattern" \
          "  path:    ${path:-<none>}" \
          "" \
          "Push \`path\` down to the pattern's literal ancestor and keep only the wildcard" \
          "tail in \`pattern\`, e.g.:" \
          "" \
          "  Glob(pattern: \"${pattern##*/}\", path: \"$ancestor\")"
      fi
    fi
  fi
fi

if [ "$tool_name" = "Grep" ]; then
  path_is_broad=0
  if [ -z "$path" ] || [ "$path" = "." ]; then
    path_is_broad=1
  fi
  if [ "$path_is_broad" -eq 1 ] && [ -z "$glob" ]; then
    warn "grep-unscoped-root" \
      "this Grep call has no \`path\` scope and no \`glob\` filter." \
      "A bare repo-root sweep with no filter walks node_modules/vendor/dist/build/.git" \
      "too, which is the usual reason a broad search times out. This is Mode 4c in" \
      "plugins/core/skills/code-search/resources/search-procedure.md." \
      "" \
      "Narrow \`path\` to the likeliest repo/subtree, or add a filter — e.g." \
      "glob: \"!**/{node_modules,vendor,dist,build,.git}/**\"."
  fi
fi

exit 0
