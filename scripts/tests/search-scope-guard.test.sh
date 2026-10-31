#!/bin/bash
# Behavioral tests for plugins/core/hooks/search-scope-guard.sh.
#
# Framework-free (portable, no bats dependency), same pattern as
# protect-sensitive-files.test.sh: each case feeds a hook JSON payload on
# stdin and asserts the exit code and/or stderr content. Every rule here
# ships in `warn`, so exit 0 is the contract for all of them — the assertions
# that matter are on what gets PRINTED, not on the exit code.
set -uo pipefail

HOOK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/plugins/core/hooks/search-scope-guard.sh"

pass=0
fail=0

record() {
  local expected="$1" actual="$2" desc="$3"
  if [ "$actual" -eq "$expected" ]; then
    pass=$((pass + 1))
    if [ -n "${VERBOSE:-}" ]; then
      printf '  ✓ %s (exit %s)\n' "$desc" "$actual"
    fi
  else
    fail=$((fail + 1))
    printf '  ✗ %s (expected exit %s, got %s)\n' "$desc" "$expected" "$actual"
  fi
}

# assert_exit <expected-exit> <description> <json-payload> — exit code only.
assert_exit() {
  local expected="$1" desc="$2" payload="$3"
  printf '%s' "$payload" | "$HOOK" >/dev/null 2>&1
  record "$expected" "$?" "$desc"
}

# assert_silent <description> <json-payload> — exit 0 AND no stderr output.
assert_silent() {
  local desc="$1" payload="$2" stderr_out exit_code
  stderr_out=$(printf '%s' "$payload" | "$HOOK" 2>&1 >/dev/null)
  exit_code=$?
  if [ "$exit_code" -eq 0 ] && [ -z "$stderr_out" ]; then
    pass=$((pass + 1))
    [ -n "${VERBOSE:-}" ] && printf '  ✓ %s\n' "$desc"
  else
    fail=$((fail + 1))
    printf '  ✗ %s (exit=%s stderr=%q)\n' "$desc" "$exit_code" "$stderr_out"
  fi
}

# assert_warns <description> <rule-id> <json-payload> — exit 0 AND stderr
# names the given rule id.
assert_warns() {
  local desc="$1" rule="$2" payload="$3" stderr_out exit_code
  stderr_out=$(printf '%s' "$payload" | "$HOOK" 2>&1 >/dev/null)
  exit_code=$?
  if [ "$exit_code" -eq 0 ] && printf '%s' "$stderr_out" | grep -q "\[$rule\]"; then
    pass=$((pass + 1))
    [ -n "${VERBOSE:-}" ] && printf '  ✓ %s\n' "$desc"
  else
    fail=$((fail + 1))
    printf '  ✗ %s (expected rule [%s], exit=%s, got: %s)\n' "$desc" "$rule" "$exit_code" "$stderr_out"
  fi
}

# assert_additional_context <description> <rule-id> <json-payload> — exit 0
# AND stdout is JSON with a non-empty hookSpecificOutput.additionalContext
# naming the given rule, AND permissionDecision is "allow".
assert_additional_context() {
  local desc="$1" rule="$2" payload="$3" stdout_out exit_code ctx decision
  stdout_out=$(printf '%s' "$payload" | "$HOOK" 2>/dev/null)
  exit_code=$?
  ctx=$(printf '%s' "$stdout_out" | jq -r '.hookSpecificOutput.additionalContext // empty' 2>/dev/null)
  decision=$(printf '%s' "$stdout_out" | jq -r '.hookSpecificOutput.permissionDecision // empty' 2>/dev/null)
  if [ "$exit_code" -eq 0 ] && [ -n "$ctx" ] && printf '%s' "$ctx" | grep -q "\[$rule\]" \
      && [ "$decision" = "allow" ]; then
    pass=$((pass + 1))
    [ -n "${VERBOSE:-}" ] && printf '  ✓ %s\n' "$desc"
  else
    fail=$((fail + 1))
    printf '  ✗ %s (expected additionalContext [%s], exit=%s, got stdout: %s)\n' \
      "$desc" "$rule" "$exit_code" "$stdout_out"
  fi
}

# gp <pattern> <path> — Glob payload.
gp() { printf '{"tool_name":"Glob","tool_input":{"pattern":"%s","path":"%s"}}' "$1" "$2"; }
# gp_nopath <pattern> — Glob payload with no path field at all.
gp_nopath() { printf '{"tool_name":"Glob","tool_input":{"pattern":"%s"}}' "$1"; }
# grp <pattern> <path> <glob> — Grep payload.
grp() { printf '{"tool_name":"Grep","tool_input":{"pattern":"%s","path":"%s","glob":"%s"}}' "$1" "$2" "$3"; }
grp_nopath() { printf '{"tool_name":"Grep","tool_input":{"pattern":"%s"}}' "$1"; }
# grp_cwd <pattern> <path> <cwd> — Grep payload with an explicit hook cwd.
grp_cwd() { printf '{"tool_name":"Grep","tool_input":{"pattern":"%s","path":"%s"},"cwd":"%s"}' "$1" "$2" "$3"; }

MIGRATIONS_PATTERN='tools/swarmery/internal/store/migrations/01[23]*.sql'

# ── Mode 4a: glob-broad-path ───────────────────────────────────────
assert_warns "narrow pattern, repo-root path -> fires" "glob-broad-path" \
  "$(gp "$MIGRATIONS_PATTERN" "/Volumes/Work/swarmery")"

assert_additional_context "glob-broad-path delivers additionalContext to the model" \
  "glob-broad-path" "$(gp "$MIGRATIONS_PATTERN" "/Volumes/Work/swarmery")"

assert_warns "narrow pattern, no path at all -> fires" "glob-broad-path" \
  "$(gp_nopath "$MIGRATIONS_PATTERN")"

assert_silent "same pattern, path already pushed to the literal ancestor" \
  "$(gp "$MIGRATIONS_PATTERN" "/Volumes/Work/swarmery/tools/swarmery/internal/store/migrations")"

assert_silent "same pattern, path is the bare relative ancestor" \
  "$(gp "$MIGRATIONS_PATTERN" "tools/swarmery/internal/store/migrations")"

assert_silent "exact filename pattern with no wildcard -> never fires glob-broad-path" \
  "$(gp "plugins/core/hooks/hooks.json" "")"

# ── Mode 4b: glob-dependency-tree (the false-positive case to guard) ──
assert_warns "node_modules/.pnpm target -> fires glob-dependency-tree, not glob-broad-path" \
  "glob-dependency-tree" \
  "$(gp_nopath '**/node_modules/.pnpm/some-package/**')"

# Must NOT be the "narrow your path" rule for a dependency-tree target.
dep_tree_stderr=$(printf '%s' "$(gp_nopath '**/node_modules/.pnpm/some-package/**')" | "$HOOK" 2>&1 >/dev/null)
if ! printf '%s' "$dep_tree_stderr" | grep -q '\[glob-broad-path\]'; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ dependency-tree target must not also fire glob-broad-path\n'
fi

assert_warns "vendor directory is the explicit path -> fires glob-dependency-tree" \
  "glob-dependency-tree" \
  "$(gp 'build/*env*' '/repo/vendor')"

assert_additional_context "glob-dependency-tree delivers additionalContext to the model" \
  "glob-dependency-tree" "$(gp_nopath '**/node_modules/.pnpm/some-package/**')"

# Ordinary path that merely has a "build"-named ancestor segment (not the
# tree root itself) must not fire glob-dependency-tree. Use a pattern with
# no literal ancestor ("*.ts") so only the dependency-tree absence is being
# tested here — this pattern may legitimately fire glob-broad-path on its
# own, which is not what this assertion checks.
no_dep_tree_ancestor_stderr=$(printf '%s' "$(gp '*.ts' '/home/me/build/app')" | "$HOOK" 2>&1 >/dev/null)
if ! printf '%s' "$no_dep_tree_ancestor_stderr" | grep -q '\[glob-dependency-tree\]'; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ ordinary path with a "build"-named ancestor must not fire glob-dependency-tree\n'
fi

# ── Mode 4c: grep-unscoped-root ────────────────────────────────────
assert_warns "Grep path=\".\", no glob -> fires grep-unscoped-root" "grep-unscoped-root" \
  "$(grp 'foo' '.' '')"

assert_warns "Grep with no path field at all, no glob -> fires grep-unscoped-root" \
  "grep-unscoped-root" "$(grp_nopath 'foo')"

assert_silent "Grep with scoped path and a glob filter" \
  "$(grp 'foo' 'apps/<mainApp>/src' '*.ts')"

assert_additional_context "grep-unscoped-root delivers additionalContext to the model" \
  "grep-unscoped-root" "$(grp 'foo' '.' '')"

assert_warns "Grep path equals the hook's own cwd (absolute repo root) -> fires grep-unscoped-root" \
  "grep-unscoped-root" "$(grp_cwd 'foo' '/Volumes/Work/swarmery' '/Volumes/Work/swarmery')"

# ── Every rule exits 0 in warn mode ────────────────────────────────
assert_exit 0 "glob-broad-path exits 0 (warn mode)" \
  "$(gp "$MIGRATIONS_PATTERN" "/Volumes/Work/swarmery")"
assert_exit 0 "glob-dependency-tree exits 0 (warn mode)" \
  "$(gp_nopath '**/node_modules/.pnpm/some-package/**')"
assert_exit 0 "grep-unscoped-root exits 0 (warn mode)" \
  "$(grp 'foo' '.' '')"

# ── Not Grep/Glob, or missing pattern: nothing to judge ────────────
assert_exit 0 "non-matching tool_name is ignored" \
  '{"tool_name":"Bash","tool_input":{"command":"ls"}}'
assert_exit 0 "Glob with no pattern at all" \
  '{"tool_name":"Glob","tool_input":{"path":"."}}'

printf 'search-scope-guard: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
