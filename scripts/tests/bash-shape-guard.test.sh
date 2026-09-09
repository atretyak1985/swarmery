#!/bin/bash
# Behavioral tests for plugins/core/hooks/bash-shape-guard.sh.
#
# Framework-free, same style as protect-sensitive-files.test.sh: feed a hook
# JSON payload on stdin and assert the outcome.
#
# These assert the DECISION, not the exit code. The guard ships in warn mode
# (exit 0 + a WARN line) and flips to exit 2 later; a suite pinned to the exit
# code would go red on the flip and get "fixed" by loosening it. A refusal is
# therefore recognised by its rule tag on stderr, which is identical in both
# modes — so this file needs no edit when the gate hardens.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/bash-shape-guard.sh"

# Redirect the burn-in log for the whole suite. Without this every run of this
# file would append a few dozen synthetic hits to the real per-rule counter —
# and that counter is what the warn→block flip is argued from, so a polluted
# log is a wrong decision, not just noise.
TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT
export BASH_SHAPE_GUARD_LOG="$TESTDIR/burn-in.jsonl"

pass=0
fail=0

# decision <json-payload> — echoes "BLOCK <rule>" or "ALLOW".
decision() {
  local payload="$1" err rc
  # Execute the hook, do NOT run it as `bash "$HOOK"`. Claude Code invokes it
  # by path, so the interpreter comes from its own shebang — on macOS that is
  # bash 3.2, while a dev machine's PATH `bash` is usually homebrew 5.x. That
  # gap is what let a bash-4-only builtin (`mapfile`) sit on a hot path with
  # this suite green: 77 passing tests over a hook that died on every real
  # call. Running it the way production runs it is the only honest harness.
  err=$(printf '%s' "$payload" | "$HOOK" 2>&1 >/dev/null)
  rc=$?
  if printf '%s' "$err" | grep -Eq '^(⚠️  WARN \(enforce from [0-9-]+\)|🚫 BLOCKED): \[[a-z-]+\]'; then
    printf 'BLOCK %s' "$(printf '%s' "$err" | sed -nE 's/^.*\[([a-z-]+)\].*$/\1/p' | head -1)"
    return 0
  fi
  # A non-zero exit with no recognisable refusal line is a crash, not a verdict.
  if [ "$rc" -ne 0 ]; then
    printf 'ERROR(rc=%s)' "$rc"
    return 0
  fi
  printf 'ALLOW'
}

# jc <command> — minimal Bash hook payload. jq builds it so quoting in the
# command under test cannot corrupt the JSON.
jc() { jq -nc --arg c "$1" '{tool_input:{command:$c}}'; }

# expect <expected-decision> <description> <command>
expect() {
  local expected="$1" desc="$2" cmd="$3" actual
  actual=$(decision "$(jc "$cmd")")
  if [ "$actual" = "$expected" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s\n      command:  %s\n      expected: %s\n      got:      %s\n' \
      "$desc" "$cmd" "$expected" "$actual"
  fi
}

# stderr_contains <needle> <description> <command> — case-insensitive, so a
# sentence-cased message still satisfies a lowercase contract phrase.
stderr_contains() {
  local needle="$1" desc="$2" cmd="$3" err
  err=$(printf '%s' "$(jc "$cmd")" | "$HOOK" 2>&1 >/dev/null)
  if printf '%s' "$err" | grep -qiF "$needle"; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s (stderr does not mention %s)\n' "$desc" "$needle"
  fi
}

# ── rule: heredoc (SC-1) ──────────────────────────────────────────
expect "BLOCK heredoc" "unquoted heredoc"        "cat <<EOF > f.txt"
expect "BLOCK heredoc" "quoted heredoc"          "cat <<'EOF' > f.txt"
expect "BLOCK heredoc" "dash heredoc"            $'cat <<-EOF > f.txt\n  body\nEOF'
expect "BLOCK heredoc" "custom delimiter"        "cat <<SHEOF > f.txt"
stderr_contains "Write tool" "heredoc names the alternative" "cat <<EOF > f.txt"
# A herestring is not a heredoc.
expect "ALLOW" "herestring is not a heredoc"     "grep foo <<< \"\$var\""

# ── rule: multi-mutation (SC-2) ───────────────────────────────────
expect "BLOCK multi-mutation" "git add && git commit"  "git add -A && git commit -m x"
expect "BLOCK multi-mutation" "mkdir && touch && chmod" "mkdir -p a && touch a/b && chmod +x a/b"
expect "BLOCK multi-mutation" "rm ; mv"                "rm -f old.txt ; mv new.txt old.txt"
expect "BLOCK multi-mutation" "two redirections"       "echo a > x.txt && echo b > y.txt"
expect "BLOCK multi-mutation" "install then commit"    "npm install && git commit -am deps"
stderr_contains "one operation per call" "names the rule" "git add -A && git commit -m x"
stderr_contains "git add -A"             "names the offending segment" "git add -A && git commit -m x"

# ── canonical commands must NOT fire (SC-3) ───────────────────────
# Every one of these is documented in this repo's own CLAUDE.md. A guard that
# breaks them gets switched off rather than used.
expect "ALLOW" "cd + make test"        "cd tools/swarmery && make test"
expect "ALLOW" "find | xargs shellcheck" "find plugins scripts -name '*.sh' -print0 | xargs -0 shellcheck -S error"
expect "ALLOW" "git log | head"        "git log --oneline | head -20"
expect "ALLOW" "grep | head"           "grep -rn foo . | head"
expect "ALLOW" "npm run build"         "npm run build"
expect "ALLOW" "cd + npm run build"    "cd web && npm run build"
expect "ALLOW" "vet then test"         "go vet ./... && go test ./..."
expect "ALLOW" "single mutation"       "git commit -m 'one thing'"
expect "ALLOW" "cd + single mutation"  "cd tools/swarmery && make install"
expect "ALLOW" "read chain, three parts" "cat a.txt && wc -l b.txt && ls -la"
expect "ALLOW" "redirect to /dev/null" "make test >/dev/null && echo done"

# ── rule: sleep-before-read (SC-4) ────────────────────────────────
expect "BLOCK sleep-before-read" "sleep && tail"  "sleep 5 && tail -n 50 /tmp/run.log"
expect "BLOCK sleep-before-read" "sleep ; cat"    "sleep 60 ; cat /tmp/out.log"
expect "BLOCK sleep-before-read" "sleep ; grep"   "sleep 2 ; grep ERROR /tmp/run.log"
stderr_contains "separate call" "sleep names the fix" "sleep 5 && tail -n 50 /tmp/run.log"
# A sleep on its own is fine, and so is a read that precedes one.
expect "ALLOW" "bare sleep"            "sleep 30"
expect "ALLOW" "read then sleep"       "cat /tmp/run.log && sleep 5"

# ── payload edge cases ────────────────────────────────────────────
expect "ALLOW" "empty command"         ""
if printf '%s' '{"tool_input":{}}' | "$HOOK" >/dev/null 2>&1; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ payload with no command must exit 0\n'
fi
if printf '%s' 'not json at all' | "$HOOK" >/dev/null 2>&1; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ non-JSON payload must exit 0 rather than crash\n'
fi

# ── rule: worktree-escape ─────────────────────────────────────────
# This rule only has an opinion inside an isolated worktree, so its cases need
# a payload carrying a cwd — `expect` deliberately sends none, which is itself
# the "no root ⇒ no opinion" case.

# A real git worktree-shaped checkout: the rule keys on the `worktrees/`
# segment the worktree manager lays isolated trees out under, then asks git for
# the exact root.
WT="$TESTDIR/worktrees/proj/T-1"
mkdir -p "$WT/sub"
git -C "$WT" init -q 2>/dev/null

# expect_cwd <expected> <description> <cwd> <command>
expect_cwd() {
  local expected="$1" desc="$2" cwd="$3" cmd="$4" actual payload
  payload=$(jq -nc --arg c "$cmd" --arg w "$cwd" '{session_id:"s",cwd:$w,tool_input:{command:$c}}')
  actual=$(decision "$payload")
  if [ "$actual" = "$expected" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s\n      cwd:      %s\n      command:  %s\n      expected: %s\n      got:      %s\n' \
      "$desc" "$cwd" "$cmd" "$expected" "$actual"
  fi
}

expect_cwd "BLOCK worktree-escape" "absolute path outside the root" \
  "$WT" "cat /Volumes/elsewhere/project/file.txt"
expect_cwd "BLOCK worktree-escape" "reaching into a sibling worktree" \
  "$WT" "cp /Volumes/elsewhere/worktrees/proj/T-2/notes.md ."

# Negatives. Each one is a command a working agent legitimately issues; a rule
# that refuses these is a rule that gets the guard switched off.
expect_cwd "ALLOW" "path inside the root"              "$WT" "cat $WT/README.md"
expect_cwd "ALLOW" "path inside the root, cwd is a subdir" "$WT/sub" "cat $WT/README.md"
expect_cwd "ALLOW" "relative path"                     "$WT" "cat docs/plan.md"
expect_cwd "ALLOW" "toolchain read under a system prefix" "$WT" "ls /usr/local/bin"
expect_cwd "ALLOW" "temp dir"                          "$WT" "cat /tmp/build.log"
expect_cwd "ALLOW" "a URL is not a path"               "$WT" "curl -s https://example.test/a/b"
expect_cwd "ALLOW" "a sed expression is not a path"    "$WT" "sed -E 's/^foo/bar/' README.md"
expect_cwd "ALLOW" "root cannot be resolved — no opinion" \
  "/some/ordinary/checkout" "cat /Volumes/elsewhere/project/file.txt"
stderr_contains_cwd() {
  local needle="$1" desc="$2" cwd="$3" cmd="$4" err
  err=$(printf '%s' "$(jq -nc --arg c "$cmd" --arg w "$cwd" '{session_id:"s",cwd:$w,tool_input:{command:$c}}')" |
    "$HOOK" 2>&1 >/dev/null)
  if printf '%s' "$err" | grep -qiF "$needle"; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s (stderr does not mention %s)\n' "$desc" "$needle"
  fi
}
stderr_contains_cwd "/Volumes/elsewhere/project/file.txt" "names the offending path" \
  "$WT" "cat /Volumes/elsewhere/project/file.txt"
stderr_contains_cwd "root:" "names the worktree root" "$WT" "cat /Volumes/elsewhere/project/file.txt"
stderr_contains_cwd "placed INSIDE the root" "states the lending contract" \
  "$WT" "cat /Volumes/elsewhere/project/file.txt"

# ── rule: ambiguous-git ───────────────────────────────────────────
expect "BLOCK ambiguous-git" "relative cd then commit"   "cd tools/swarmery && git commit -m x"
expect "BLOCK ambiguous-git" "relative cd then checkout" "cd web ; git checkout -- ."
stderr_contains "git -C tools/swarmery commit -m x" "hands back the -C replacement" \
  "cd tools/swarmery && git commit -m x"

expect "ALLOW" "already -C"              "cd tools/swarmery && git -C . commit -m x"
expect "ALLOW" "read-only query after cd" "cd tools/swarmery && git status"
expect "ALLOW" "read-only log after cd"   "cd web && git log --oneline -5"
expect "ALLOW" "absolute cd is unambiguous" "cd /srv/repo && git commit -m x"
expect "ALLOW" "no cd at all"            "git commit -m x"
expect "ALLOW" "cd then a non-git build" "cd tools/swarmery && make test"

# ── per-rule enforcement is independent ───────────────────────────
# The whole point of the per-rule mapping: raising one rule must not raise the
# others, and must not lower them either.
one_blocked=$(sed -E 's/^( *)(heredoc\))( *printf '\''warn'\'')/\1\2 printf '\''block'\''/' "$HOOK")
printf '%s' "$one_blocked" > "$TESTDIR/hook-one-blocked.sh"
probe_rc() {
  local hook="$1" cmd="$2"
  printf '%s' "$(jq -nc --arg c "$cmd" '{session_id:"s",tool_input:{command:$c}}')" \
    | bash "$hook" >/dev/null 2>&1
  printf '%s' "$?"
}
if [ "$(probe_rc "$TESTDIR/hook-one-blocked.sh" 'cat <<EOF > f.txt')" = "2" ]; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ setting a rule to block must make that rule exit 2\n'
fi
for other in 'git add -A && git commit -m x' 'sleep 5 && tail -n 5 /tmp/run.log'; do
  if [ "$(probe_rc "$TESTDIR/hook-one-blocked.sh" "$other")" = "0" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ blocking one rule changed another rule'\''s decision: %s\n' "$other"
  fi
done

# Every rule ships in warn mode at the end of Phase 2 — the switch was built,
# not thrown. A rule set to block without its row in docs/GATE-HARDENING.md
# being filled is exactly the failure this plan exists to prevent.
if ! sed -n '/^rule_mode()/,/^}/p' "$HOOK" | grep -q "printf 'block'"; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ a rule is set to block — check its row in docs/GATE-HARDENING.md is filled first\n'
fi

# ── burn-in telemetry ─────────────────────────────────────────────
# The flip from warn to block is argued from counted per-rule hits, so the
# counter is load-bearing: an undercount reads as "this rule never fires".

# hit <session> <command> [cwd] — run the hook against a fresh log, echo the log.
hit() {
  local session="$1" cmd="$2" cwd="${3:-/tmp/x}" payload
  : > "$BASH_SHAPE_GUARD_LOG"
  payload=$(jq -nc --arg c "$cmd" --arg s "$session" --arg w "$cwd" \
    '{session_id:$s,cwd:$w,tool_input:{command:$c}}')
  printf '%s' "$payload" | "$HOOK" >/dev/null 2>&1
  cat "$BASH_SHAPE_GUARD_LOG"
}

# probe_for <rule-id> — the command (and cwd, after a tab) that makes one rule
# fire. Every rule the hook can refuse with owes this table an entry: a rule
# that fires but does not log has silently left the flip decision.
probe_for() {
  case "$1" in
    heredoc)           printf 'cat <<EOF > f.txt' ;;
    multi-mutation)    printf 'git add -A && git commit -m x' ;;
    sleep-before-read) printf 'sleep 5 && tail -n 5 /tmp/run.log' ;;
    ambiguous-git)     printf 'cd tools/pkg && git commit -m x' ;;
    worktree-escape)   printf 'cat /Volumes/elsewhere/x.txt\t%s' "$WT" ;;
  esac
}

# logged_once <rule> <description> <command> — exactly one well-formed record
# carrying the rule id, the decision, a timestamp and the session.
logged_once() {
  local rule="$1" desc="$2" cmd="$3" out lines ok
  out=$(hit "sess-$rule" "$cmd")
  lines=$(printf '%s\n' "$out" | grep -c .)
  ok=$(printf '%s' "$out" | jq -r --arg r "$rule" '
    select(.rule == $r and (.decision == "warn" or .decision == "block")
           and (.ts | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T"))
           and .session == ("sess-" + $r)
           and .cmd != "") | "ok"' 2>/dev/null | head -1)
  if [ "$lines" = "1" ] && [ "$ok" = "ok" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s (lines=%s parsed=%s)\n      log: %s\n' "$desc" "$lines" "${ok:-none}" "$out"
  fi
}

logged_once "heredoc"           "heredoc hit is counted"           "cat <<EOF > f.txt"
logged_once "multi-mutation"    "multi-mutation hit is counted"    "git add -A && git commit -m x"
logged_once "sleep-before-read" "sleep-before-read hit is counted" "sleep 5 && tail -n 5 /tmp/run.log"

# A command is arbitrary text: newlines and quotes inside it must not be able
# to forge a second record, or one hit inflates the count it is judged by.
multiline=$(hit "sess-multiline" $'git add -A && git commit -m "line one\nline two"')
if [ "$(printf '%s\n' "$multiline" | grep -c .)" = "1" ] &&
   printf '%s' "$multiline" | jq -e '.rule == "multi-mutation"' >/dev/null 2>&1; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ a command containing newlines must produce exactly one log line\n      log: %s\n' "$multiline"
fi

# A rule that fires but does not log has been silently retired from the flip
# decision, so every refusal path must be counted — not just the three above.
rules_in_hook=$(grep -oE '^ *refuse "[a-z-]+"' "$HOOK" | sed -E 's/.*"([a-z-]+)".*/\1/' | sort -u)
rules_logged=""
for r in $rules_in_hook; do
  probe=$(probe_for "$r")
  if [ -z "$probe" ]; then
    fail=$((fail + 1))
    printf '  ✗ rule [%s] has no telemetry probe in this suite — add one to probe_for()\n' "$r"
    continue
  fi
  probe_cmd=${probe%%$'\t'*}
  probe_cwd=""
  [ "$probe" != "$probe_cmd" ] && probe_cwd=${probe#*$'\t'}
  if printf '%s' "$(hit "sess-sweep" "$probe_cmd" ${probe_cwd:+"$probe_cwd"})" |
     jq -e --arg r "$r" '.rule == $r' >/dev/null 2>&1; then
    rules_logged="$rules_logged $r"
  fi
done
if [ "$(printf '%s' "$rules_logged" | wc -w | tr -d ' ')" = "$(printf '%s' "$rules_in_hook" | wc -w | tr -d ' ')" ]; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ not every refuse() rule reaches the burn-in log (hook: %s / logged:%s)\n' \
    "$(printf '%s' "$rules_in_hook" | tr '\n' ' ')" "$rules_logged"
fi

# Telemetry is strictly secondary to the allow-or-block contract: an unwritable
# log must leave the decision and the message byte-identical.
payload=$(jq -nc '{session_id:"s",tool_input:{command:"cat <<EOF > f.txt"}}')
good_err=$(printf '%s' "$payload" | "$HOOK" 2>&1 >/dev/null); good_rc=$?
blocked_dir="$TESTDIR/readonly"
mkdir -p "$blocked_dir" && chmod 500 "$blocked_dir"
bad_err=$(printf '%s' "$payload" |
  BASH_SHAPE_GUARD_LOG="$blocked_dir/nested/deep.jsonl" "$HOOK" 2>&1 >/dev/null); bad_rc=$?
chmod 700 "$blocked_dir"
if [ "$good_err" = "$bad_err" ] && [ "$good_rc" = "$bad_rc" ]; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ an unwritable log changed the hook output (rc %s→%s)\n' "$good_rc" "$bad_rc"
fi

# The reader and the hook must agree on where the log lives; if they drift, the
# operator reads an empty table for a rule that is firing.
if diff <(sed -n '/^guard_log_file()/,/^}/p' "$HOOK" | grep -E 'BASH_SHAPE_GUARD_LOG|AGENT_PROJECT|CLAUDE_PROJECT_DIR|TMPDIR' | sed 's/^ *//') \
        <(sed -n '/^resolve_log()/,/^}/p' "$ROOT/scripts/guard-hits.sh" | grep -E 'BASH_SHAPE_GUARD_LOG|AGENT_PROJECT|CLAUDE_PROJECT_DIR|TMPDIR' | sed 's/^ *//') >/dev/null; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ hook guard_log_file() and scripts/guard-hits.sh resolve_log() disagree on the log path\n'
fi

# Every rule the hook can fire owes the flip-decision document a row, or the
# flip gets made for a rule nobody reviewed.
DECISION_DOC="$ROOT/docs/GATE-HARDENING.md"
for r in $rules_in_hook; do
  if grep -q "\`$r\`" "$DECISION_DOC"; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ rule [%s] has no row in docs/GATE-HARDENING.md\n' "$r"
  fi
done

# ── the gate itself ───────────────────────────────────────────────
# Hardening one rule must stay a one-line edit an operator can make without
# reading the rest of the hook: a `rule_mode` case arm per rule, each printing
# warn or block and nothing else.
if sed -n '/^rule_mode()/,/^}/p' "$HOOK" | grep -Eq "^ *[a-z*-]+\) +printf '(warn|block)' ;;$" &&
   grep -Eq '^ENFORCE_FROM="[0-9]{4}-[0-9]{2}-[0-9]{2}"' "$HOOK"; then
  pass=$((pass + 1))
else
  fail=$((fail + 1))
  printf '  ✗ the per-rule gate is no longer a readable rule_mode() mapping with a dated ENFORCE_FROM\n'
fi

# Every rule the hook can fire owes rule_mode() an arm; the catch-all must not
# be what decides a real rule's mode, or a rule silently inherits a default.
for r in $rules_in_hook; do
  if sed -n '/^rule_mode()/,/^}/p' "$HOOK" | grep -q "^ *$r)"; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ rule [%s] has no arm in rule_mode() — it falls through to the catch-all\n' "$r"
  fi
done

printf 'bash-shape-guard: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
