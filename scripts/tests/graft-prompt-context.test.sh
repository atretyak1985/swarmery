#!/bin/bash
# Behavioral tests for plugins/graft-pack/hooks/graft-prompt-context.sh.
#
# Framework-free (portable, no bats dependency), and hermetic: `graft` is a stub
# script on a synthetic PATH that echoes canned JSON, so the suite asserts the
# hook's GATE, not the CLI's ranking. The canned payloads are copies of real
# `graft ask --json` output captured from @nanonets/graft 0.16.0 — including the
# structural shape that carries no coverage fields, which is the case a spec-only
# reading of the gate gets backwards.
#
# Each case asserts: (a) exit 0 — always, (b) stdout is valid JSON or empty,
# (c) injection / nudge / silence as expected, (d) the nudge counter behaves.
# Run: bash scripts/tests/graft-prompt-context.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/graft-pack/hooks/graft-prompt-context.sh"

pass=0
fail=0
fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

valid_json() { printf '%s' "$1" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{JSON.parse(s)})' 2>/dev/null; }

# ctx <stdout> — the injected additionalContext string, or empty.
ctx() {
  printf '%s' "$1" | node -e '
    let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{
      try { process.stdout.write(JSON.parse(s).hookSpecificOutput.additionalContext || ""); }
      catch (e) { process.stdout.write(""); }
    })' 2>/dev/null
}

TMP=$(mktemp -d)
export TMPDIR="$TMP/markers"   # per-session nudge markers land here, not in /tmp
mkdir -p "$TMPDIR"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

BIN="$TMP/bin"
mkdir -p "$BIN"

# ── stub CLI ─────────────────────────────────────────────────────────────────
# Prints whatever $TMP/graft-response holds. A missing file means "graft printed
# nothing", which is how a crashed or timed-out CLI looks to the hook.
cat > "$BIN/graft" <<'STUB'
#!/bin/bash
[ -f "$GRAFT_STUB_RESPONSE" ] && cat "$GRAFT_STUB_RESPONSE"
exit 0
STUB
chmod +x "$BIN/graft"

# A project with an index present. The hook checks for wiring.json before it ever
# calls the CLI, so every case needs one on disk.
REPO="$TMP/repo"
mkdir -p "$REPO/graft/.graph" "$REPO/.claude"
echo '{"meta":{},"nodes":[],"edges":[]}' > "$REPO/graft/.graph/wiring.json"

RESP="$TMP/graft-response"

# Real 0.16.0 payloads.
STRONG_JSON='{"query":"q","mode":"lexical","hits":[{"kind":"symbol","title":"beta · function","pointer":"src/a.ts:L2-L2","snippet":"function beta()","score":1.5}],"coverage":0.139,"coverageStrong":0.139}'
WEAK_JSON='{"query":"q","mode":"lexical","hits":[{"kind":"symbol","title":"beta · function","pointer":"src/a.ts:L2-L2","snippet":"function beta()","score":0.2}],"coverage":0.02,"coverageStrong":0.02}'
BROAD_JSON='{"query":"q","mode":"lexical","hits":[{"kind":"file","title":"b.ts · file","pointer":"src/b.ts","snippet":"","score":1}],"coverage":0.62,"coverageStrong":0.01}'
STRUCTURAL_JSON='{"query":"what does gamma call","mode":"structural","subject":"gamma","note":"outgoing edges from gamma","hits":[{"kind":"callee","title":"alpha","pointer":"src/a.ts:L1-L1","snippet":"function alpha()","relation":"calls","score":1}]}'
NOHITS_JSON='{"query":"q","mode":"lexical","hits":[],"coverage":0.9,"coverageStrong":0.9}'

RC=0
OUT=""
# run_hook <session-id> <prompt> [extra-env...] — sets OUT and RC.
run_hook() {
  local sid="$1" prompt="$2"; shift 2
  local payload
  payload=$(node -e '
    process.stdout.write(JSON.stringify({session_id: process.argv[1], prompt: process.argv[2]}))
  ' "$sid" "$prompt")
  OUT=$(printf '%s' "$payload" | env "$@" \
    PATH="$BIN:/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin" \
    GRAFT_STUB_RESPONSE="$RESP" \
    CLAUDE_PROJECT_DIR="$REPO" TMPDIR="$TMPDIR" bash "$HOOK")
  RC=$?
}

# ── Case 1: strong lexical hit → locators injected ───────────────────────────
# coverageStrong 0.139 clears graft's real STRONG_FLOOR of 0.1. (A spec that said
# 0.15 would score this same payload as weak — that is the point of the case.)
printf '%s' "$STRONG_JSON" > "$RESP"
run_hook "sid-strong" "where is the beta helper defined in this repo"
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && valid_json "$OUT" && [[ "$C" == *"src/a.ts:L2-L2"* ]] && [[ "$C" == *"Context graph hits"* ]]; then
  ok_case
else
  fail_case "case1 strong-hit: expected injected locators (rc=$RC, out=$OUT)"
fi

# ── Case 2: broad coverage clears the second clause ──────────────────────────
printf '%s' "$BROAD_JSON" > "$RESP"
run_hook "sid-broad" "how does the request pipeline fit together"
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && [[ "$C" == *"src/b.ts"* ]]; then
  ok_case
else
  fail_case "case2 broad-coverage: expected injection via coverage>=0.5 (rc=$RC, out=$OUT)"
fi

# ── Case 3: structural answer (NO coverage fields) → injected ────────────────
# Graft returns no scores here because the query resolved to a real symbol. The
# resolution IS the relevance signal; scoring the absent fields as 0 would
# suppress graft's most certain answers.
printf '%s' "$STRUCTURAL_JSON" > "$RESP"
run_hook "sid-structural" "what does gamma call in this codebase"
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && [[ "$C" == *"resolved this prompt to gamma"* ]] && [[ "$C" == *"src/a.ts:L1-L1"* ]]; then
  ok_case
else
  fail_case "case3 structural: expected injection with resolved subject (rc=$RC, out=$OUT)"
fi

# ── Case 4: weak hit → nudge once, twice, then silent ────────────────────────
printf '%s' "$WEAK_JSON" > "$RESP"
SID="sid-weak"
run_hook "$SID" "please take another look at that thing we discussed"
C1=$(ctx "$OUT"); RC1=$RC
run_hook "$SID" "and also check the other thing while you are there"
C2=$(ctx "$OUT"); RC2=$RC
run_hook "$SID" "one more question about the same area of the code"
C3=$(ctx "$OUT"); RC3=$RC
if [ "$RC1" -eq 0 ] && [ "$RC2" -eq 0 ] && [ "$RC3" -eq 0 ] \
   && [[ "$C1" == *"no strong match"* ]] && [[ "$C2" == *"no strong match"* ]] && [ -z "$C3" ]; then
  ok_case
else
  fail_case "case4 nudge-cap: expected nudge,nudge,silent (1='$C1' 2='$C2' 3='$C3')"
fi

# ── Case 5: a different session gets its own two nudges ─────────────────────
run_hook "sid-weak-other" "a fresh session asking something the graph cannot answer"
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && [[ "$C" == *"no strong match"* ]]; then
  ok_case
else
  fail_case "case5 per-session-counter: expected a nudge in a new session (out=$OUT)"
fi

# ── Case 6: short prompt → silent, and the CLI is never called ──────────────
printf '%s' "$STRONG_JSON" > "$RESP"
run_hook "sid-short" "yes"
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case6 short-prompt: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 7: kill switch → silent even on a strong hit ───────────────────────
run_hook "sid-kill" "where is the beta helper defined in this repo" SWARMERY_GRAFT_PROMPT=0
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case7 kill-switch: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 8: project.json promptContext:false → silent ───────────────────────
echo '{"name":"t","codePath":".","enabledPacks":[],"graft":{"promptContext":false}}' > "$REPO/.claude/project.json"
run_hook "sid-cfgoff" "where is the beta helper defined in this repo"
RC8=$RC; OUT8=$OUT
rm -f "$REPO/.claude/project.json"
if [ "$RC8" -eq 0 ] && [ -z "$OUT8" ]; then
  ok_case
else
  fail_case "case8 project-config-off: expected silence (rc=$RC8, out=$OUT8)"
fi

# ── Case 9: no graft on PATH → silent ──────────────────────────────────────
OUT=$(printf '{"session_id":"sid-nocli","prompt":"where is the beta helper defined"}' \
  | env PATH="/usr/bin:/bin" CLAUDE_PROJECT_DIR="$REPO" TMPDIR="$TMPDIR" bash "$HOOK")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case9 no-cli: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 10: no index on disk → silent (CLI never consulted) ───────────────
BARE="$TMP/bare"
mkdir -p "$BARE"
printf '%s' "$STRONG_JSON" > "$RESP"
OUT=$(printf '{"session_id":"sid-noindex","prompt":"where is the beta helper defined"}' \
  | env PATH="$BIN:/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin" \
        GRAFT_STUB_RESPONSE="$RESP" CLAUDE_PROJECT_DIR="$BARE" TMPDIR="$TMPDIR" bash "$HOOK")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case10 no-index: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 11: CLI prints nothing (crash/timeout) → silent, exit 0 ───────────
rm -f "$RESP"
run_hook "sid-empty" "where is the beta helper defined in this repo"
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case11 empty-cli-output: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 12: CLI prints garbage → exit 0, no stdout, one stderr line ───────
printf 'not json at all {{{' > "$RESP"
ERR="$TMP/err"
OUT=$(printf '{"session_id":"sid-garbage","prompt":"where is the beta helper defined"}' \
  | env PATH="$BIN:/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin" \
        GRAFT_STUB_RESPONSE="$RESP" CLAUDE_PROJECT_DIR="$REPO" TMPDIR="$TMPDIR" \
        bash "$HOOK" 2>"$ERR")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ] && [ "$(wc -l < "$ERR")" -eq 1 ]; then
  ok_case
else
  fail_case "case12 garbage-cli-output: expected exit 0 + 1 stderr line (rc=$RC, out=$OUT, err=$(cat "$ERR"))"
fi

# ── Case 13: hits present but empty array → nudge path, never a broken block ─
printf '%s' "$NOHITS_JSON" > "$RESP"
run_hook "sid-nohits" "a question whose payload carries no hits at all"
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && { [ -z "$C" ] || [[ "$C" == *"no strong match"* ]]; }; then
  ok_case
else
  fail_case "case13 no-hits: expected nudge or silence, never an empty locator block (out=$OUT)"
fi

# ── Case 14: malformed stdin → exit 0, silent ─────────────────────────────
printf '%s' "$STRONG_JSON" > "$RESP"
OUT=$(printf 'this is not json {{{' \
  | env PATH="$BIN:/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin" \
        GRAFT_STUB_RESPONSE="$RESP" CLAUDE_PROJECT_DIR="$REPO" TMPDIR="$TMPDIR" bash "$HOOK")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case14 malformed-stdin: expected silence + exit 0 (rc=$RC, out=$OUT)"
fi

printf 'graft-prompt-context: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
