#!/bin/bash
# Behavioral tests for plugins/core/hooks/phase-guard.sh and its parser
# plugins/core/hooks/lib/guard-parse.sh.
#
# Framework-free, like protect-sensitive-files.test.sh: each case builds a
# throwaway run root (a dir holding .swarmery/plan/<doc>.md), feeds the hook a
# PreToolUse payload on stdin and asserts the exit code — 2 = BLOCK, 0 = ALLOW —
# and, where it matters, the stderr message and the guard.log line.
#
# The parser cases read the SAME golden phase docs the Go parser is held to
# (tools/swarmery/testdata/fixtures/guard-blocks/, mdfence/guard_test.go), so a
# drift between the two parsers fails here or there, not in a live run.
#
# Run: bash scripts/tests/phase-guard.test.sh   (VERBOSE=1 lists passing cases)
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$REPO/plugins/core/hooks/phase-guard.sh"
PARSER="$REPO/plugins/core/hooks/lib/guard-parse.sh"
FIX="$REPO/tools/swarmery/testdata/fixtures/guard-blocks"
READ_GREP_MSG="read it with Read/Grep, changes to it are outside this phase's scope (see .swarmery/plan/)"

# The hook also walks up from $CLAUDE_PROJECT_DIR; a session running this suite
# may export one that points at a real run root. Every case sets its own.
unset CLAUDE_PROJECT_DIR

pass=0
fail=0
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

ok() {
  pass=$((pass + 1))
  if [ -n "${VERBOSE:-}" ]; then printf '  ✓ %s\n' "$1"; fi
}
bad() {
  fail=$((fail + 1))
  printf '  ✗ %s\n' "$1"
}

# mkroot <name> <fixture.md|-> — a run root with pkg/, src/, .github/ files and
# the fixture lent as .swarmery/plan/<fixture>; "-" leaves .swarmery/ out.
mkroot() {
  local r="$TMP/$1"
  mkdir -p "$r/pkg" "$r/src" "$r/.github/workflows" "$r/plugins/x/hooks" "$r/scripts/tests"
  : > "$r/pkg/trap_test.go"
  : > "$r/trap_test.go"
  : > "$r/src/a.go"
  : > "$r/hooks.json"
  : > "$r/.github/workflows/ci.yml"
  : > "$r/plugins/x/hooks/hooks.json"
  : > "$r/scripts/tests/x.test.sh"
  if [ "$2" != "-" ]; then
    mkdir -p "$r/.swarmery/plan"
    cp "$FIX/$2" "$r/.swarmery/plan/$2"
  fi
  printf '%s' "$r"
}

# payload <cwd> <tool> <path-or-command>
payload() {
  jq -nc --arg c "$1" --arg t "$2" --arg v "$3" '
    if $t == "Bash" then {cwd: $c, tool_name: $t, tool_input: {command: $v}}
    else {cwd: $c, tool_name: $t, tool_input: {file_path: $v}} end'
}

# run_hook <json> — RC, ERR (stderr) of one hook call.
run_hook() {
  ERR=$(printf '%s' "$1" | "$HOOK" 2>&1 >/dev/null)
  RC=$?
}

# expect <exit> <description> <cwd> <tool> <path-or-command> [stderr substring]
expect() {
  local want="$1" desc="$2"
  run_hook "$(payload "$3" "$4" "$5")"
  if [ "$RC" -ne "$want" ]; then
    bad "$desc (expected exit $want, got $RC: $ERR)"
    return
  fi
  if [ -n "${6:-}" ] && [[ "$ERR" != *"$6"* ]]; then
    bad "$desc (stderr lacks '$6': $ERR)"
    return
  fi
  ok "$desc"
}

# ── parser: the Go corpus, same answers ───────────────────────────
# shellcheck source=../../plugins/core/hooks/lib/guard-parse.sh
. "$PARSER"

out=$(guard_parse "$FIX/ok.md" 2>/dev/null); rc=$?
want='.github/workflows/*.yml
plugins/*/hooks/hooks.json
**/*_test.go
**/*.test.ts
**/*.test.tsx
scripts/tests/**/*'
if [ "$rc" -eq 0 ] && [ "$out" = "$want" ]; then ok "parser: ok.md → the six globs, quoted example ignored"
else bad "parser: ok.md (rc=$rc) → $out"; fi

guard_parse "$FIX/absent.md" >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 1 ]; then ok "parser: absent.md → absent (1)"; else bad "parser: absent.md rc=$rc, want 1"; fi

out=$(guard_parse "$FIX/unparsable.md" 2>/dev/null); rc=$?
if [ "$rc" -eq 3 ] && [[ "$out" == "unparsable (line 10): "* ]]; then ok "parser: unparsable.md → 3 at line 10 (the unquoted * alias)"
else bad "parser: unparsable.md (rc=$rc) → $out"; fi

out=$(guard_parse "$FIX/dirs-not-files.md" 2>"$TMP/warn"); rc=$?
nwarn=$(grep -c 'phase-guard: warning: protected path' "$TMP/warn")
if [ "$rc" -eq 0 ] && [ "$nwarn" -eq 2 ] && grep -q '"scripts/tests/\*\*/\*"' "$TMP/warn" &&
  [ "$out" = '.github/workflows/**
scripts/tests/
**/*_test.go' ]; then ok "parser: dirs-not-files.md → globs kept, two directory warnings"
else bad "parser: dirs-not-files.md (rc=$rc, warnings=$nwarn) → $out"; fi

if bash "$PARSER" "$FIX/unparsable.md" >/dev/null 2>&1; [ $? -eq 3 ]; then ok "parser: runnable as a script, exit 3 on unparsable"
else bad "parser: bash guard-parse.sh unparsable.md did not exit 3"; fi

# shape <rc> <reason substring> <description> <yaml body> — mirrors
# TestParseGuardShapes in tools/swarmery/internal/mdfence/guard_test.go.
shape() {
  printf '# P\n\n```yaml\n%b\n```\n' "$4" > "$TMP/shape.md"
  out=$(guard_parse "$TMP/shape.md" 2>/dev/null); rc=$?
  if [ "$rc" -eq "$1" ] && [[ "$out" == *"$2"* ]]; then ok "parser shape: $3"
  else bad "parser shape: $3 (rc=$rc, want $1 with '$2') → $out"; fi
}
shape 3 "missing key stop_if" "missing stop_if" 'kind: guard\nprotected_paths: ["a.go"]'
shape 3 "missing key protected_paths" "missing protected_paths" 'kind: guard\nstop_if: [x]'
shape 3 "plain string" "nested entry" 'kind: guard\nprotected_paths:\n  - {a: b}\nstop_if: [x]'
shape 3 "list of strings" "map instead of list" 'kind: guard\nprotected_paths: {a: b}\nstop_if: [x]'
shape 3 "empty entry" "empty entry" 'kind: guard\nprotected_paths: [""]\nstop_if: [x]'
shape 3 "not valid yaml" "not a mapping" 'kind: guard\n- a'
shape 0 "" "empty lists and unknown keys are fine" 'kind: guard\nprotected_paths: []\nstop_if: []\nowner: planner'
shape 0 "a.go" "a bare scalar is a one-element list" 'kind: guard\nprotected_paths: a.go\nstop_if: protected path'
shape 0 "b/*.go" "a multi-line flow list" 'kind: guard\nprotected_paths: [\n  "a.go",\n  "b/*.go"\n]\nstop_if: [x]'
shape 3 "tab" "a tab in the indentation" 'kind: guard\nprotected_paths:\n\t- "a.go"\nstop_if: []'
shape 3 "already defined" "a duplicated key" 'kind: guard\nprotected_paths: []\nprotected_paths: ["a"]\nstop_if: []'

printf '```\nkind: guard\nprotected_paths: [x]\nstop_if: [y]\n```\n\n```yaml\nkind: prior\nareas: [a]\n```\n' > "$TMP/bare.md"
guard_parse "$TMP/bare.md" >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 1 ]; then ok "parser: a bare fence and a forecast are not a guard"; else bad "parser: bare fence + prior rc=$rc, want 1"; fi

# ── BLOCK: a protected path, every way of reaching it ─────────────
R=$(mkroot ok ok.md)
expect 2 "Edit on a protected file (absolute path)"   "$R" Edit  "$R/pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Write on a protected file (relative path)"  "$R" Write "pkg/trap_test.go" "pkg/trap_test.go is protected by the phase doc"
expect 2 "Edit on a protected workflow"               "$R" Edit  "$R/.github/workflows/ci.yml" "$READ_GREP_MSG"
expect 2 "Edit on a protected hook manifest"          "$R" Edit  "$R/plugins/x/hooks/hooks.json" "$READ_GREP_MSG"
expect 2 "Edit on a root-level test (** = zero dirs)" "$R" Edit  "$R/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash sed -i"            "$R" Bash "sed -i '' 's/want/got/' pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash tee"               "$R" Bash "printf x | tee pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash > redirect"        "$R" Bash "echo 'package trap' > pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash >> redirect, no space" "$R" Bash "echo x>>./pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash chmod"             "$R" Bash "chmod 000 pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash heredoc body mentioning the path" "$R" Bash "cat > /tmp/notes.txt <<'EOF'
rewrite pkg/trap_test.go to expect the current value
EOF" "$READ_GREP_MSG"
expect 2 "Bash python -c with the path" "$R" Bash "python3 -c \"open('pkg/trap_test.go','w').write('x')\"" "$READ_GREP_MSG"
expect 2 "Bash cat (a read is blocked too, message names Read/Grep)" "$R" Bash "cat pkg/trap_test.go" "Read/Grep"
expect 2 "Bash absolute path token" "$R" Bash "cp /dev/null $R/pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash path through .."     "$R" Bash "cat src/../pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash git show HEAD:<path>" "$R" Bash "git show HEAD:pkg/trap_test.go" "$READ_GREP_MSG"
expect 2 "Bash from a subdirectory cwd" "$R/pkg" Bash "cat trap_test.go" "pkg/trap_test.go is protected"
expect 2 "Bash path in another letter case" "$R" Bash "cat PKG/TRAP_TEST.GO" "$READ_GREP_MSG"
expect 2 "Bash compound command, protected token last" "$R" Bash "go vet ./... && git add pkg/trap_test.go" "$READ_GREP_MSG"

# As written, src/wf/ci.yml matches no glob; it lands on .github/workflows/ci.yml.
ln -s ../.github/workflows "$R/src/wf"
expect 2 "Edit through a symlinked directory" "$R" Edit "$R/src/wf/ci.yml" ".github/workflows/ci.yml is protected"
ln -s "$R/pkg/trap_test.go" "$R/src/alias.go"
expect 2 "Write through a symlinked file"     "$R" Write "$R/src/alias.go" "pkg/trap_test.go is protected"

run_hook "$(jq -nc --arg c "$R" '{cwd: $c, tool_name: "MultiEdit", tool_input: {file_path: ($c + "/src/a.go"),
  edits: [{file_path: ($c + "/pkg/trap_test.go"), old_string: "a", new_string: "b"}]}}')"
if [ "$RC" -eq 2 ]; then ok "MultiEdit: a protected edits[].file_path blocks"; else bad "MultiEdit edits[] (rc=$RC)"; fi

# ── ALLOW: what the phase may do ──────────────────────────────────
expect 0 "Bash go test ./..."       "$R" Bash "go test ./..."
expect 0 "Bash go test ./pkg/..."   "$R" Bash "go test ./pkg/..."
expect 0 "Edit on an unprotected file" "$R" Edit "$R/src/a.go"
expect 0 "Bash cat on an unprotected file" "$R" Bash "cat src/a.go"
expect 0 "a protected path's basename alone does not match" "$R" Bash "cat hooks.json"
expect 0 "Write .swarmery/report.md (not the guard's input)" "$R" Write "$R/.swarmery/report.md"
expect 0 "Edit outside the run root" "$R" Edit "$TMP/elsewhere/x_test.go"
expect 0 "a tool this hook does not judge" "$R" Read "$R/pkg/trap_test.go"

# ── the guard's own inputs, with NO guard block ───────────────────
A=$(mkroot absent absent.md)
expect 2 "absent block: Edit .swarmery/plan/<doc>"   "$A" Edit  "$A/.swarmery/plan/absent.md" "$READ_GREP_MSG"
expect 2 "absent block: Write .swarmery/guard.log"   "$A" Write "$A/.swarmery/guard.log" "$READ_GREP_MSG"
expect 2 "absent block: Bash cat of the plan doc"    "$A" Bash  "cat .swarmery/plan/absent.md" "Read/Grep"
expect 2 "absent block: Bash append to guard.log"    "$A" Bash  "echo forged >> .swarmery/guard.log" "$READ_GREP_MSG"
expect 2 "absent block: Bash rm -rf .swarmery"       "$A" Bash  "rm -rf .swarmery" "$READ_GREP_MSG"
expect 2 "absent block: Bash mv .swarmery/plan/"     "$A" Bash  "mv .swarmery/plan/ /tmp/p" "$READ_GREP_MSG"
expect 0 "absent block: a test file is not protected" "$A" Edit "$A/pkg/trap_test.go"
expect 0 "absent block: go test passes"               "$A" Bash "go test ./..."
expect 2 "ok block: the plan doc is protected too"   "$R" Edit "$R/.swarmery/plan/ok.md" "$READ_GREP_MSG"

# ── unparsable block: everything is blocked, with the parser's reason ──
U=$(mkroot unparsable unparsable.md)
expect 2 "unparsable: Edit on an ordinary file" "$U" Edit "$U/src/a.go" "unparsable (line 10)"
expect 2 "unparsable: any Bash command"         "$U" Bash "ls" "unparsable (line 10)"
if grep -q $'\tunparsable.md: unparsable (line 10): ' "$U/.swarmery/guard.log"; then ok "unparsable: guard.log carries the reason"
else bad "unparsable: guard.log lacks the reason: $(cat "$U/.swarmery/guard.log" 2>/dev/null)"; fi

# ── directory-only globs: a warning, still protective, go test passes ──
D=$(mkroot dirs dirs-not-files.md)
expect 0 "dir-only glob: go test ./pkg/... passes" "$D" Bash "go test ./pkg/..." "names a directory, not files"
expect 2 "dir-only glob: .github/workflows/** still protects ci.yml" "$D" Edit "$D/.github/workflows/ci.yml" "$READ_GREP_MSG"
expect 2 "dir-only glob: scripts/tests/ still protects its files"   "$D" Bash "bash scripts/tests/x.test.sh" "$READ_GREP_MSG"

# ── no .swarmery/plan/: silent, no log ────────────────────────────
N=$(mkroot none -)
run_hook "$(payload "$N" Edit "$N/pkg/trap_test.go")"
if [ "$RC" -eq 0 ] && [ -z "$ERR" ] && [ ! -e "$N/.swarmery" ]; then ok "no plan dir: exit 0, silent, nothing written"
else bad "no plan dir: rc=$RC stderr='$ERR'"; fi

# ── a session that cd'd out of the worktree still meets the guard ──
mkdir -p "$TMP/outside"
ERR=$(payload "$TMP/outside" Bash "cat $R/pkg/trap_test.go" | CLAUDE_PROJECT_DIR="$R" "$HOOK" 2>&1 >/dev/null); RC=$?
if [ "$RC" -eq 2 ] && [[ "$ERR" == *"pkg/trap_test.go is protected"* ]]; then ok "cwd outside the run, CLAUDE_PROJECT_DIR inside: still blocked"
else bad "cwd outside the run with CLAUDE_PROJECT_DIR: rc=$RC ($ERR)"; fi
ERR=$(payload "$TMP/outside" Bash "cat $R/pkg/trap_test.go" | "$HOOK" 2>&1 >/dev/null); RC=$?
if [ "$RC" -eq 0 ]; then ok "cwd outside the run, no CLAUDE_PROJECT_DIR: not a run, allowed"
else bad "cwd outside the run without CLAUDE_PROJECT_DIR: rc=$RC ($ERR)"; fi

# ── fail closed: bad input, jq missing ────────────────────────────
ERR=$(printf 'not json' | (cd "$R" && "$HOOK") 2>&1 >/dev/null); RC=$?
if [ "$RC" -eq 2 ]; then ok "inside a run: a payload that is not JSON blocks"; else bad "non-JSON payload rc=$RC"; fi

NOJQ="$TMP/nojq-bin"
mkdir -p "$NOJQ"
for b in cat dirname date tr awk readlink pwd; do ln -s "$(command -v "$b")" "$NOJQ/$b"; done
ERR=$(printf '{}' | (cd "$R" && PATH="$NOJQ" "$HOOK") 2>&1 >/dev/null); RC=$?
if [ "$RC" -eq 2 ] && [[ "$ERR" == *"jq is missing"* ]]; then ok "inside a run: jq missing blocks"; else bad "jq missing in a run: rc=$RC ($ERR)"; fi
ERR=$(printf '{}' | (cd "$N" && PATH="$NOJQ" "$HOOK") 2>&1 >/dev/null); RC=$?
if [ "$RC" -eq 0 ] && [ -z "$ERR" ]; then ok "outside a run: jq missing stays silent"; else bad "jq missing outside a run: rc=$RC ($ERR)"; fi

# ── guard.log: one line per block, <ts>\t<tool>\t<path>\t<reason> ──
L=$(mkroot log ok.md)
printf 'earlier line\n' > "$L/.swarmery/guard.log"
run_hook "$(payload "$L" Edit "$L/pkg/trap_test.go")"
run_hook "$(payload "$L" Bash "cat .swarmery/plan/ok.md")"
run_hook "$(payload "$L" Bash "go test ./...")"
run_hook "$(payload "$L" Write "pkg/trap_test.go")"
log="$L/.swarmery/guard.log"
lines=$(wc -l < "$log" | tr -d ' ')
if [ "$lines" -eq 4 ] && [ "$(head -1 "$log")" = "earlier line" ]; then ok "guard.log: three blocks → three appended lines, nothing truncated"
else bad "guard.log: $lines lines: $(cat "$log")"; fi
re=$'^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\t(Edit|Write|Bash)\t[^\t]+\t[^\t]+$'
if tail -n 3 "$log" | grep -Evq "$re"; then bad "guard.log: a line breaks the format: $(tail -n 3 "$log")"
else ok "guard.log: every line is <RFC3339 ts>\\t<tool>\\t<path>\\t<reason>"; fi
expected_rows=$'Edit\tpkg/trap_test.go\tprotected_paths "**/*_test.go"\nBash\t.swarmery/plan/ok.md\timplicit ".swarmery/plan/**"\nWrite\tpkg/trap_test.go\tprotected_paths "**/*_test.go"'
if [ "$(tail -n 3 "$log" | cut -f2-)" = "$expected_rows" ]; then ok "guard.log: tool, root-relative path and the matching glob per line"
else bad "guard.log rows: $(tail -n 3 "$log" | cut -f2-)"; fi

# ── speed: a long heredoc stays far inside the 5 s hook timeout ───
big="cat > /tmp/out.txt <<'EOF'"$'\n'
i=0
while [ "$i" -lt 3000 ]; do big="$big word$i src/file$i.go"$'\n'; i=$((i + 1)); done
big="${big}EOF"
start=$SECONDS
run_hook "$(payload "$R" Bash "$big")"
took=$((SECONDS - start))
if [ "$RC" -eq 0 ] && [ "$took" -lt 5 ]; then ok "a 6000-token command is judged in ${took}s"
else bad "6000-token command: rc=$RC in ${took}s"; fi

# ── hooks.json: registered before protect-sensitive-files, with a timeout ──
HOOKS_JSON="$REPO/plugins/core/hooks/hooks.json"
entry=$(jq -c '[.hooks.PreToolUse[] | .hooks[] | .command] | index("${CLAUDE_PLUGIN_ROOT}/hooks/phase-guard.sh")' "$HOOKS_JSON")
psf=$(jq -c '[.hooks.PreToolUse[] | .hooks[] | .command] | index("${CLAUDE_PLUGIN_ROOT}/hooks/protect-sensitive-files.sh")' "$HOOKS_JSON")
reg=$(jq -r '.hooks.PreToolUse[] | select(any(.hooks[]; .command | endswith("/phase-guard.sh")))
  | "\(.matcher) \(.hooks[] | select(.command | endswith("/phase-guard.sh")) | .timeout)"' "$HOOKS_JSON")
case "$reg" in
  *Edit*Write*Bash*" 5")
    if [ "$entry" != "null" ] && [ "$psf" != "null" ] && [ "$entry" -lt "$psf" ]; then ok "hooks.json: Edit|Write|Bash, timeout 5, before protect-sensitive-files"
    else bad "hooks.json: phase-guard ($entry) must come before protect-sensitive-files ($psf)"; fi ;;
  *) bad "hooks.json registration: '$reg'" ;;
esac

printf 'phase-guard: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
