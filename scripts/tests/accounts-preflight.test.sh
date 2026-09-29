#!/bin/bash
# Behavioral tests for plugins/accounts-pack/hooks/preflight-account.sh — the
# SessionStart hook that reports credential COVERAGE (which ${VAR}s the enabled
# plugins reference and which are unset), not account equality.
#
# Framework-free (portable, no bats dependency), fully offline. A stub
# `swarmery` stands in for `swarmery account doctor --fast --json`: it prints
# the AUTHORITATIVE Report shape (every field accountdoctor.Fast marshals),
# deriving launchedViaSwarmery and daemon from its environment and --path the
# same way the real doctor does. The hook payload is shaped exactly like
# internal/hookshim/shim_test.go's sessionStartStdin fixture (session_id, cwd,
# hook_event_name — NO transcript_path).
# Run locally with `bash scripts/tests/accounts-preflight.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/accounts-pack/hooks/preflight-account.sh"
BASH_BIN="$(command -v bash)"
JQ_BIN="$(command -v jq)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
LOG="$WORK/test.log"
: >"$LOG"

pass=0
fail=0

ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3" | tee -a "$LOG"; }

# stdin_for <projectDir> [sessionId] -> the hook's SessionStart payload.
stdin_for() {
  printf '{"session_id":"%s","cwd":"%s","hook_event_name":"SessionStart"}' "${2:-sid-1}" "$1"
}

FAKE_HOME="$WORK/home"; mkdir -p "$FAKE_HOME"
STUB_BIN="$WORK/stubbin"; mkdir -p "$STUB_BIN"
cat >"$STUB_BIN/swarmery" <<'STUB'
#!/bin/bash
path=""
while [ $# -gt 0 ]; do
  case "$1" in --path) path="$2"; shift ;; esac
  shift
done
case "${STUB_MODE:-json}" in
  fail) exit 1 ;;
  garbage) echo 'not json'; exit 0 ;;
  hang) sleep 30; exit 0 ;;
esac
launched=false
[ -n "${SWARMERY_LAUNCH_PATH:-}" ] && [ "$SWARMERY_LAUNCH_PATH" = "$path" ] && launched=true
daemon=false
case "$path" in "$HOME/.swarmery/worktrees/"*) daemon=true ;; esac
jq -c --arg p "$path" --argjson l "$launched" --argjson d "$daemon" \
  '.path = $p | .launchedViaSwarmery = $l | .daemon = $d' "$STUB_REPORT"
STUB
chmod +x "$STUB_BIN/swarmery"

# report <file> <account> <estate> <credentials> <expected> <present> <missing> [findings] [dups]
# writes the authoritative Report shape; list args are JSON arrays.
report() {
  "$JQ_BIN" -n --arg a "$2" --arg e "$3" --argjson c "$4" \
    --argjson x "$5" --argjson p "$6" --argjson m "$7" \
    --argjson f "${8:-[]}" --argjson s "${9:-[]}" '{
      schema: 1, path: "", account: $a, source: "pin", configDir: "",
      estate: $e, estateRoot: (if $e == "" then "" else "/estate/root" end), settingsFile: "",
      credentials: $c, credentialStore: "", varsExpected: $x, varsPresent: $p, varsMissing: $m,
      launchedViaSwarmery: false, daemon: false, findings: $f, staleDuplicates: $s }' >"$1"
}

# run_hook <reportFile> <projectDir> [extra env assignments...] -> stdout; sets HOOK_EXIT
run_hook() {
  local rep="$1" proj="$2"
  shift 2
  HOOK_OUT="$(stdin_for "$proj" | env -u CLAUDE_CONFIG_DIR -u SWARMERY_LAUNCH_PATH -u SWARMERY_SKIP_PREFLIGHT \
    HOME="$FAKE_HOME" PATH="$STUB_BIN:$PATH" STUB_REPORT="$rep" "$@" "$BASH_BIN" "$HOOK" 2>>"$LOG")"
  HOOK_EXIT=$?
}

ctx_of() { printf '%s' "$1" | jq -r '.hookSpecificOutput.additionalContext // empty' 2>/dev/null; }

PROJ="$WORK/proj"; mkdir -p "$PROJ"
N13=(ALPHA_TOKEN BRAVO_TOKEN CHARLIE_URL DELTA_KEY ECHO_USER FOXTROT_PASS GOLF_HOST HOTEL_PORT INDIA_DB JULIET_ID KILO_SECRET LIMA_AUTH MIKE_TENANT)
N13_JSON="$(printf '%s\n' "${N13[@]}" | jq -R . | jq -sc .)"
FINDINGS='[{"id":"f1","severity":"warn","title":"t","detail":"d","file":"/x"}]'
DUPS='[{"kind":"credential-store","paths":["/a.env","/b.env"],"key":"","overlap":["ALPHA_TOKEN"],"count":2}]'

# ── (a) coverage gap with ACTUAL == BOUND == default: 13 names, 0 present ─────
R_GAP="$WORK/gap.json"; report "$R_GAP" default "" 0 "$N13_JSON" '[]' "$N13_JSON"
run_hook "$R_GAP" "$PROJ"
GAP_OUT="$HOOK_OUT"
lines="$(printf '%s' "$HOOK_OUT" | grep -c '^' || true)"
ctx="$(ctx_of "$HOOK_OUT")"
event="$(printf '%s' "$HOOK_OUT" | jq -r '.hookSpecificOutput.hookEventName // empty' 2>/dev/null)"
missing_names=0
for n in "${N13[@]}"; do printf '%s' "$ctx" | grep -qF "$n" || missing_names=$((missing_names + 1)); done
if [ "$HOOK_EXIT" -eq 0 ] && [ "$lines" -eq 1 ] && [ "$event" = "SessionStart" ] &&
  printf '%s' "$ctx" | grep -qF '13' && [ "$missing_names" -eq 0 ]; then ok
else bad "(a) 0 of 13 present -> one JSON line naming 13 and all 13 NAMES" "1 line, 13 names" "$lines line(s) exit $HOOK_EXIT ctx='$ctx'"; fi

# ── (b) full coverage -> silence ──────────────────────────────────────────────
R_FULL="$WORK/full.json"; report "$R_FULL" default "" 0 "$N13_JSON" "$N13_JSON" '[]'
run_hook "$R_FULL" "$PROJ"
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(b) 13 of 13 present -> silent, exit 0" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi

# ── (c) clean + non-empty findings/staleDuplicates (authoritative shapes) -> silence
R_FULLX="$WORK/fullx.json"; report "$R_FULLX" default "" 0 "$N13_JSON" "$N13_JSON" '[]' "$FINDINGS" "$DUPS"
run_hook "$R_FULLX" "$PROJ"
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(c) clean + findings + staleDuplicates -> silent" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi

# ── (d) gap + non-empty findings/staleDuplicates -> output unchanged vs (a) ──
R_GAPX="$WORK/gapx.json"; report "$R_GAPX" default "" 0 "$N13_JSON" '[]' "$N13_JSON" "$FINDINGS" "$DUPS"
run_hook "$R_GAPX" "$PROJ"
if [ "$HOOK_OUT" = "$GAP_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(d) findings/staleDuplicates do not change the gap output" "identical to (a)" "'$HOOK_OUT'"; fi

# ── (e) D2a: an estate with zero credentials and nothing referenced -> silence ──
R_ZERO="$WORK/zero.json"; report "$R_ZERO" default estatex 0 '[]' '[]' '[]'
run_hook "$R_ZERO" "$PROJ"
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(e) credentials 0 + varsExpected [] -> silent (healthy)" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi

# ── (f) ...the same, plus ONE referenced and missing name -> that NAME ────────
R_ONE="$WORK/one.json"; report "$R_ONE" default estatex 0 '["ONE_NAME"]' '[]' '["ONE_NAME"]'
run_hook "$R_ONE" "$PROJ"
ctx="$(ctx_of "$HOOK_OUT")"
if [ "$HOOK_EXIT" -eq 0 ] && printf '%s' "$ctx" | grep -qF 'ONE_NAME' && printf '%s' "$ctx" | grep -qF "estate 'estatex'"; then ok
else bad "(f) credentials 0 + one missing name -> that name reported" "ONE_NAME in context" "'$HOOK_OUT'"; fi

# ── (g) launch clause: marker absent / matching / daemon worktree ─────────────
run_hook "$R_ONE" "$PROJ"
if ctx_of "$HOOK_OUT" | grep -qF 'exit and open a new shell'; then ok
else bad "(g1) no launch marker -> 'exit and open a new shell'" "phrase present" "'$HOOK_OUT'"; fi
run_hook "$R_ONE" "$PROJ" SWARMERY_LAUNCH_PATH="$PROJ"
ctx="$(ctx_of "$HOOK_OUT")"
if [ -n "$ctx" ] && ! printf '%s' "$ctx" | grep -qF 'exit and open a new shell'; then ok
else bad "(g2) marker == cwd -> the phrase is absent (gap still named)" "phrase absent" "'$HOOK_OUT'"; fi
WT="$FAKE_HOME/.swarmery/worktrees/proj/task"; mkdir -p "$WT"
run_hook "$R_ONE" "$WT"
ctx="$(ctx_of "$HOOK_OUT")"
if printf '%s' "$ctx" | grep -qF 'spawn seam' && ! printf '%s' "$ctx" | grep -qF 'open a new shell'; then ok
else bad "(g3) cwd under the daemon worktree root -> names the spawn seam, no new-shell advice" "spawn seam" "'$HOOK_OUT'"; fi

# ── (h) no value, ever: a planted value never reaches stdout, cache or log ───
PHASE4_PROBE_VALUE="zzq-$RANDOM$RANDOM-probe"
R_VAL="$WORK/val.json"; report "$R_VAL" default "" 0 '["PHASE4_PROBE_A","PHASE4_PROBE_B"]' '["PHASE4_PROBE_A"]' '["PHASE4_PROBE_B"]'
run_hook "$R_VAL" "$PROJ" PHASE4_PROBE_A="$PHASE4_PROBE_VALUE" PHASE4_PROBE_B="$PHASE4_PROBE_VALUE"
CACHE_SID1="$FAKE_HOME/.swarmery/run/preflight/sid-1.env"
leak=0
printf '%s' "$HOOK_OUT" | grep -qF "$PHASE4_PROBE_VALUE" && leak=$((leak + 1))
grep -qF "$PHASE4_PROBE_VALUE" "$CACHE_SID1" 2>/dev/null && leak=$((leak + 1))
grep -qF "$PHASE4_PROBE_VALUE" "$LOG" && leak=$((leak + 1))
if [ "$leak" -eq 0 ] && [ -n "$HOOK_OUT" ]; then ok
else bad "(h) planted value absent from stdout, cache and log" "0 hits" "$leak hit(s)"; fi

# ── (i) fail-open matrix: each silent with exit 0 ─────────────────────────────
EMPTY_BIN="$WORK/emptybin"; mkdir -p "$EMPTY_BIN"
ln -s "$JQ_BIN" "$EMPTY_BIN/jq"
HOOK_OUT="$(stdin_for "$PROJ" | env HOME="$FAKE_HOME" PATH="$EMPTY_BIN:/usr/bin:/bin" "$BASH_BIN" "$HOOK" 2>/dev/null)"; HOOK_EXIT=$?
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(i1) swarmery absent -> silent" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi
for mode in fail garbage; do
  run_hook "$R_GAP" "$PROJ" STUB_MODE="$mode"
  if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
  else bad "(i2) swarmery mode=$mode -> silent" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi
done
NOJQ_BIN="$WORK/nojq"; mkdir -p "$NOJQ_BIN"; ln -s "$STUB_BIN/swarmery" "$NOJQ_BIN/swarmery"
HOOK_OUT="$(stdin_for "$PROJ" | env HOME="$FAKE_HOME" PATH="$NOJQ_BIN" STUB_REPORT="$R_GAP" "$BASH_BIN" "$HOOK" 2>/dev/null)"; HOOK_EXIT=$?
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(i3) jq absent -> silent" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi
run_hook "$R_GAP" "$PROJ" SWARMERY_SKIP_PREFLIGHT=1
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(i4) SWARMERY_SKIP_PREFLIGHT=1 -> silent" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi
SECONDS=0
run_hook "$R_GAP" "$PROJ" STUB_MODE=hang
elapsed=$SECONDS
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ] && [ "$elapsed" -lt 4 ]; then ok
else bad "(i5) swarmery sleeps 30 -> returns under 4 s, silent" "<4s '' exit 0" "${elapsed}s '$HOOK_OUT' exit $HOOK_EXIT"; fi

# ── (j) the character gate stays stricter than claudeacct.ValidKey ────────────
# shellcheck disable=SC2016  # literal $ and backtick are the point
gate_vectors=('a b' 'a$b' 'a;b' 'a`b' 'wörk' '"a"' '-a')
gv=0
for key in "${gate_vectors[@]}"; do
  gv=$((gv + 1))
  R_KEY="$WORK/key$gv.json"; report "$R_KEY" "$key" "" 0 '["ONE_NAME"]' '[]' '["ONE_NAME"]'
  run_hook "$R_KEY" "$PROJ"
  if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
  else bad "(j$gv) account key '$key' -> silent" "'' exit 0" "'$HOOK_OUT' exit $HOOK_EXIT"; fi
done
R_BADVAR="$WORK/badvar.json"; report "$R_BADVAR" default "" 0 '["*","a b","$(x)"]' '[]' '["*","a b","$(x)"]'
run_hook "$R_BADVAR" "$PROJ"
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(j) variable names outside [A-Za-z_][A-Za-z0-9_]* never reach the context" "'' exit 0" "'$HOOK_OUT'"; fi

# ── (k) statusline cache: 0600 under 0700, camelCase keys, lengths only ──────
rm -rf "$FAKE_HOME/.swarmery/run"
run_hook "$R_ONE" "$PROJ"
# GNU form first: BSD `stat -c` fails loudly, whereas GNU `stat -f` exits 0.
dmode="$(stat -c '%a' "$FAKE_HOME/.swarmery/run/preflight" 2>/dev/null || stat -f '%Lp' "$FAKE_HOME/.swarmery/run/preflight" 2>/dev/null)"
fmode="$(stat -c '%a' "$CACHE_SID1" 2>/dev/null || stat -f '%Lp' "$CACHE_SID1" 2>/dev/null)"
want_cache="$(printf 'account=default\nestate=estatex\nvarsExpected=1\nvarsPresent=0\nlaunch=0')"
if [ "$dmode" = "700" ] && [ "$fmode" = "600" ] && [ "$(cat "$CACHE_SID1")" = "$want_cache" ] &&
  ! grep -qvE '^[A-Za-z0-9_.=/-]*$' "$CACHE_SID1"; then ok
else bad "(k) cache 0600 in a 0700 dir with the Report's spelling" "700/600 + $want_cache" "$dmode/$fmode + $(cat "$CACHE_SID1" 2>/dev/null)"; fi
run_hook "$R_ONE" "$PROJ"
HOOK_OUT="$(printf '{"session_id":"../evil","cwd":"%s"}' "$PROJ" | env HOME="$FAKE_HOME" PATH="$STUB_BIN:$PATH" STUB_REPORT="$R_ONE" "$BASH_BIN" "$HOOK" 2>/dev/null)"
if [ ! -e "$FAKE_HOME/.swarmery/run/evil.env" ] && [ -n "$HOOK_OUT" ]; then ok
else bad "(k) a session id outside [A-Za-z0-9_-] writes no cache" "no file" "written"; fi

# ── (m) first sight: one sentence with the estate root and the COUNT, no name ─
FS='[{"id":"first-sight","severity":"warn","title":"t","detail":"d","file":""}]'
R_FS="$WORK/fs.json"; report "$R_FS" default estatex 13 "$N13_JSON" "$N13_JSON" '[]' "$FS"
run_hook "$R_FS" "$PROJ"
ctx="$(ctx_of "$HOOK_OUT")"
lines="$(printf '%s' "$HOOK_OUT" | grep -c '^' || true)"
leaked=0
for n in "${N13[@]}"; do printf '%s' "$ctx" | grep -qF "$n" && leaked=$((leaked + 1)); done
if [ "$HOOK_EXIT" -eq 0 ] && [ "$lines" -eq 1 ] && printf '%s' "$ctx" | grep -qF '/estate/root' &&
  printf '%s' "$ctx" | grep -qF '13 credential' && [ "$leaked" -eq 0 ]; then ok
else bad "(m) first-sight -> one line naming the root and the count, no name" "1 line" "$lines line(s) ctx='$ctx'"; fi
# the same report without the finding says nothing (coverage is full)
R_NOFS="$WORK/nofs.json"; report "$R_NOFS" default estatex 13 "$N13_JSON" "$N13_JSON" '[]'
run_hook "$R_NOFS" "$PROJ"
if [ -z "$HOOK_OUT" ] && [ "$HOOK_EXIT" -eq 0 ]; then ok
else bad "(m) no first-sight finding -> silent" "''" "'$HOOK_OUT'"; fi

# ── (m2) an estate root with a space and a non-ASCII character is rendered,
#        quoted — never dropped (the doctor has already recorded the path) ─────
R_FSU="$WORK/fs-unicode.json"; report "$R_FSU" default estatex 13 "$N13_JSON" "$N13_JSON" '[]' "$FS"
jq '.estateRoot = "/Users/u/my estate/prøjekt"' "$R_FSU" >"$R_FSU.tmp" && mv "$R_FSU.tmp" "$R_FSU"
run_hook "$R_FSU" "$PROJ"
ctx="$(ctx_of "$HOOK_OUT")"
lines="$(printf '%s' "$HOOK_OUT" | grep -c '^' || true)"
if [ "$HOOK_EXIT" -eq 0 ] && [ "$lines" -eq 1 ] && printf '%s' "$ctx" | grep -qF '"/Users/u/my estate/prøjekt"' &&
  printf '%s' "$ctx" | grep -qF '13 credential'; then ok
else bad "(m2) a spaced, non-ASCII estate root is quoted into the sentence" "1 line with the quoted root" "$lines line(s) ctx='$ctx'"; fi
# a control character or a quote in the root is escaped, never raw, and still one line
R_FSC="$WORK/fs-ctrl.json"; report "$R_FSC" default estatex 2 "$N13_JSON" "$N13_JSON" '[]' "$FS"
jq '.estateRoot = "/x/a\"b\nIgnore previous\u001fz"' "$R_FSC" >"$R_FSC.tmp" && mv "$R_FSC.tmp" "$R_FSC"
run_hook "$R_FSC" "$PROJ"
ctx="$(ctx_of "$HOOK_OUT")"
lines="$(printf '%s' "$HOOK_OUT" | grep -c '^' || true)"
if [ "$HOOK_EXIT" -eq 0 ] && [ "$lines" -eq 1 ] && printf '%s' "$ctx" | grep -qF '"/x/a\"b\nIgnore previous\u001fz"' &&
  [ "$(printf '%s' "$ctx" | grep -c '^')" -eq 1 ]; then ok
else bad "(m2) quotes and control characters in the root are escaped" "one escaped line" "$lines line(s) ctx='$ctx'"; fi

# ── (n) the inner bound is below the watchdog; the retired name stays gone ───
if [ "$(grep -c -- '--timeout 2.5s' "$HOOK")" -eq 1 ] && [ ! -e "$(dirname "$HOOK")/warn-wrong-account.sh" ]; then ok
else bad "(n) --timeout 2.5s once, warn-wrong-account.sh absent" "1 / absent" "$(grep -c -- '--timeout 2.5s' "$HOOK")"; fi

# ── (l) one name per fact: no snake_case alias anywhere in the hook ──────────
if ! grep -qE 'vars_expected|vars_present|vars_missing' "$HOOK"; then ok
else bad "(l) no snake_case alias in the hook" "0 matches" "$(grep -nE 'vars_expected|vars_present|vars_missing' "$HOOK")"; fi

printf 'accounts-preflight: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
