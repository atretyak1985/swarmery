#!/bin/bash
# Behavioral tests: the core plugin's post-edit and session-end helpers act only
# inside the project.
#
# session-summary.sh, code-formatter.sh and type-checker.sh each walk up from a
# file the session touched to the nearest project marker (.git, package.json,
# pyproject.toml) and then run that project's tooling: git, npx/prettier/eslint,
# npm run typecheck, .venv/bin/black. A file can sit inside a folder the operator
# did not write (a downloaded archive, a cloned third-party repo next to the
# project), and that folder's own tooling is then whatever its author put there.
#
# These cases pin the confinement contract:
#   - a file outside $CLAUDE_PROJECT_DIR and outside every repos[] entry of
#     .claude/project.json is ignored by all three hooks;
#   - the upward walk stops at the scope root, so a project marker ABOVE the
#     project dir is never used;
#   - git always runs with its fsmonitor and hook plug-in points switched off,
#     inside the project too;
#   - in-project formatting and type checking still happen, and npx is never
#     allowed to fetch a formatter on its own (--no-install);
#   - a repos[] entry that would widen the scope to / or $HOME is ignored.
#
# Hermetic: npx and npm are PATH shims that only run a project's LOCAL
# node_modules/.bin tool or package.json script, every sentinel is a file in a
# mktemp dir, and HOME / the workspace / the session substrate are sandboxed.
# Run: bash scripts/tests/hooks-project-scope.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SS="$ROOT/plugins/core/hooks/session-summary.sh"
CF="$ROOT/plugins/core/hooks/code-formatter.sh"
TCK="$ROOT/plugins/core/hooks/type-checker.sh"

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
absent() { if [ -e "$1" ]; then bad "$2"; else ok; fi; }
present() { if [ -e "$1" ]; then ok; else bad "$2"; fi; }

T=$(mktemp -d)
# Resolve the sandbox physically (macOS: /var → /private/var) so every path the
# hooks see is the same spelling the assertions use.
T=$(cd -P "$T" && pwd -P)
trap 'rm -rf "$T"' EXIT

S="$T/sentinels"
mkdir -p "$S" "$T/home" "$T/bin" "$T/tmp" "$T/run" "$T/wsroot"

# ── PATH shims ───────────────────────────────────────────────────────────────
# npx: log argv, then run ./node_modules/.bin/<tool> when the project has one —
# which is exactly what the real npx does first, and the reason a foreign
# folder's node_modules is a code-execution surface.
cat > "$T/bin/npx" <<'SHIM'
#!/bin/bash
printf '%s\n' "$*" >> "$NPX_LOG"
[ "${1:-}" = "--no-install" ] && shift
tool="${1:-}"; shift || true
if [ -n "$tool" ] && [ -x "./node_modules/.bin/$tool" ]; then exec "./node_modules/.bin/$tool" "$@"; fi
exit 0
SHIM
# npm: `npm run <script>` runs the package.json script, as npm does.
cat > "$T/bin/npm" <<'SHIM'
#!/bin/bash
[ "${1:-}" = "run" ] || exit 0
script=$(jq -r --arg s "${2:-}" '.scripts[$s] // empty' package.json 2>/dev/null)
[ -n "$script" ] && sh -c "$script"
exit 0
SHIM
chmod +x "$T/bin/npx" "$T/bin/npm"
export NPX_LOG="$T/npx.log"
: > "$NPX_LOG"
TEST_PATH="$T/bin:/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin"

# gitrepo <dir> <fsmonitor-sentinel> — a repo with one commit, one unstaged
# change, and core.fsmonitor set to a harmless sentinel command.
gitrepo() {
  local d="$1" sentinel="$2"
  mkdir -p "$d"
  git -C "$d" init -q
  printf 'one\n' > "$d/README.md"
  git -C "$d" add README.md
  git -C "$d" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false commit -qm init
  printf 'two\n' >> "$d/README.md"
  git -C "$d" config core.fsmonitor "touch $sentinel"
}

# node_project <dir> <tag> — package.json with code:fix + a typecheck script and
# a local prettier, each touching a sentinel named after <tag>.
node_project() {
  local d="$1" tag="$2"
  mkdir -p "$d/node_modules/.bin" "$d/src"
  printf '{"scripts":{"code:fix":"true","typecheck":"touch %s/TC_%s"}}\n' "$S" "$tag" > "$d/package.json"
  printf '#!/bin/sh\ntouch %s/PRETTIER_%s\n' "$S" "$tag" > "$d/node_modules/.bin/prettier"
  printf '#!/bin/sh\ntouch %s/ESLINT_%s\n' "$S" "$tag" > "$d/node_modules/.bin/eslint"
  chmod +x "$d/node_modules/.bin/prettier" "$d/node_modules/.bin/eslint"
  printf 'export const x = 1\n' > "$d/src/a.ts"
}

# py_venv <dir> <tag> — a .venv whose black/isort touch sentinels.
py_venv() {
  local d="$1" tag="$2"
  mkdir -p "$d/bin"
  printf '#!/bin/sh\ntouch %s/BLACK_%s\n' "$S" "$tag" > "$d/bin/black"
  printf '#!/bin/sh\ntouch %s/ISORT_%s\n' "$S" "$tag" > "$d/bin/isort"
  chmod +x "$d/bin/black" "$d/bin/isort"
}

# ── Fixture ──────────────────────────────────────────────────────────────────
PROJ="$T/project"            # $CLAUDE_PROJECT_DIR
FOREIGN="$T/foreign"         # sibling folder nobody declared
SIB="$T/sibling"             # sibling repo declared in repos[]
OUTER="$T/outer"             # a project marker ABOVE a second project dir
PROJ2="$OUTER/proj2"

gitrepo "$PROJ" "$S/FSMON_IN"
node_project "$PROJ" IN
mkdir -p "$PROJ/.claude" "$PROJ/py"
printf '[project]\nname="p"\n' > "$PROJ/py/pyproject.toml"
printf 'x = 1\n' > "$PROJ/py/m.py"

gitrepo "$FOREIGN" "$S/FSMON"
node_project "$FOREIGN" FOREIGN
printf '[project]\nname="f"\n' > "$FOREIGN/pyproject.toml"
py_venv "$FOREIGN/.venv" FOREIGN
printf 'x = 1\n' > "$FOREIGN/m.py"
# An in-project .venv that is really a link into the foreign folder.
ln -s "$FOREIGN/.venv" "$PROJ/py/.venv"

node_project "$SIB" SIB
printf '{"repos":["../sibling","/","~"]}\n' > "$PROJ/.claude/project.json"

gitrepo "$OUTER" "$S/FSMON_OUTER"
node_project "$OUTER" OUTER
mkdir -p "$PROJ2/lib"
printf 'export const y = 2\n' > "$PROJ2/lib/b.ts"

# edit_payload <file> — a PostToolUse payload for an Edit of <file>.
edit_payload() { jq -nc --arg f "$1" '{tool_name:"Edit",tool_input:{file_path:$f}}'; }

# run_post <hook> <project-dir> <file>
run_post() {
  edit_payload "$3" | (cd "$2" && env PATH="$TEST_PATH" HOME="$T/home" \
    CLAUDE_PROJECT_DIR="$2" "$1") >/dev/null 2>&1
}

# run_summary <project-dir> <file>... — SessionEnd with a session log naming <file>s.
run_summary() {
  local proj="$1"; shift
  local today log f
  today=$(date +%Y%m%d)
  log="$T/tmp/claude-session-${today}.jsonl"
  : > "$log"
  for f in "$@"; do
    jq -nc --arg f "$f" '{ts:"2026-10-22T10:00:00Z",tool:"Read",file:$f,cmd:""}' >> "$log"
  done
  (cd "$proj" && env -u CLAUDE_PLUGIN_ROOT PATH="$TEST_PATH" HOME="$T/home" \
    AGENT_PROJECT="scope-suite" AGENT_WORKSPACE_ROOT="$T/wsroot" \
    CLAUDE_SESSION_TMP="$T/tmp" SWARMERY_SESSION_FILE="$log" SWARMERY_RUN_DIR="$T/run" \
    CLAUDE_PROJECT_DIR="$proj" "$SS" </dev/null) >/dev/null 2>&1
}

# ── 1. Foreign folder: nothing of it runs ───────────────────────────────────
run_post "$CF"  "$PROJ" "$FOREIGN/src/a.ts"
run_post "$CF"  "$PROJ" "$FOREIGN/package.json"
run_post "$CF"  "$PROJ" "$FOREIGN/m.py"
run_post "$TCK" "$PROJ" "$FOREIGN/src/a.ts"
run_summary "$PROJ" "$FOREIGN/README.md" "$PROJ/src/a.ts"

absent "$S/PRETTIER_FOREIGN" "code-formatter ran the foreign folder's prettier"
absent "$S/ESLINT_FOREIGN"   "code-formatter ran the foreign folder's eslint"
absent "$S/BLACK_FOREIGN"    "code-formatter ran the foreign folder's .venv black"
absent "$S/ISORT_FOREIGN"    "code-formatter ran the foreign folder's .venv isort"
absent "$S/TC_FOREIGN"       "type-checker ran the foreign folder's typecheck script"
absent "$S/FSMON"            "session-summary ran git in the foreign folder with its fsmonitor"

# ── 2. Session summary is still written, for the project only ────────────────
summary=$(find "$PROJ/.swarmery/sessions" -name '*.json' -type f 2>/dev/null | head -1)
if [ -n "$summary" ] && jq -e . "$summary" >/dev/null 2>&1; then ok; else bad "no valid session summary JSON was written"; fi
if [ -n "$summary" ] && jq -e --arg p "$PROJ" 'any(.repos[]; .path == $p)' "$summary" >/dev/null 2>&1; then
  ok
else
  bad "the in-project repo is missing from the session summary"
fi
if [ -n "$summary" ] && jq -e --arg p "$FOREIGN" 'any(.repos[]; .path == $p) | not' "$summary" >/dev/null 2>&1; then
  ok
else
  bad "the foreign repo appears in the session summary"
fi
# git inside the project runs with its plug-in points off too.
absent "$S/FSMON_IN" "session-summary ran git in the project with the repo's fsmonitor enabled"

# ── 3. In-project formatting and type checking still work ────────────────────
run_post "$CF"  "$PROJ" "$PROJ/src/a.ts"
run_post "$TCK" "$PROJ" "$PROJ/src/a.ts"
present "$S/PRETTIER_IN" "in-project prettier no longer runs"
present "$S/ESLINT_IN"   "in-project eslint no longer runs"
present "$S/TC_IN"       "in-project typecheck no longer runs"
if grep -q -- '^--no-install prettier --write' "$NPX_LOG" && grep -q -- '^--no-install eslint --fix' "$NPX_LOG"; then
  ok
else
  bad "npx is called without --no-install (it may download a formatter): $(tr '\n' '|' < "$NPX_LOG")"
fi
if grep -qv -- '^--no-install ' "$NPX_LOG"; then
  bad "some npx call lacks --no-install: $(tr '\n' '|' < "$NPX_LOG")"
else
  ok
fi

# An in-project .venv that links outside the scope is not the project's venv.
run_post "$CF" "$PROJ" "$PROJ/py/m.py"
absent "$S/BLACK_FOREIGN" "code-formatter followed an in-project .venv link into the foreign folder"

# ── 4. repos[] entries are in scope; / and ~ never widen it ─────────────────
run_post "$CF" "$PROJ" "$SIB/src/a.ts"
present "$S/PRETTIER_SIB" "a repos[] sibling is treated as out of scope"
# The project.json above also lists "/" and "~"; the foreign checks in case 1
# ran with that file in place, so they already prove neither widened the scope.

# ── 5. The upward walk stops at the scope root ──────────────────────────────
run_post "$CF"  "$PROJ2" "$PROJ2/lib/b.ts"
run_post "$TCK" "$PROJ2" "$PROJ2/lib/b.ts"
run_summary "$PROJ2" "$PROJ2/lib/b.ts"
absent "$S/PRETTIER_OUTER" "code-formatter walked above the project dir to a parent package.json"
absent "$S/TC_OUTER"       "type-checker walked above the project dir to a parent package.json"
absent "$S/FSMON_OUTER"    "session-summary walked above the project dir to a parent repo"

# ── 6. A ".." path is never in scope ────────────────────────────────────────
run_post "$TCK" "$PROJ" "$PROJ/../foreign/src/a.ts"
absent "$S/TC_FOREIGN" "a ../ path escaped the project scope"

printf 'hooks-project-scope: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
