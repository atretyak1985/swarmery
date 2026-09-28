#!/bin/bash
# Behavioral tests for the statusline account chip:
#   plugins/core/statusline/statusline.sh — account_key_from_config_dir() +
#   transcript_config_dir(), and the 🪪<key> BADGES entry they feed.
#
# Framework-free (portable, no bats dependency), fully offline. Every render
# gets its OWN brand-new empty TMPDIR, so the weather cache can never have been
# warmed by an earlier call in this same run and every invocation deterministically
# takes the cold "warming up" fallback — see phase-6 design Д7: two runs of the
# same script can otherwise differ on their own (clock, weather, counters),
# which would make a diff-based regression proof flaky for reasons unrelated to
# this phase. The default-account case also uses a cwd OUTSIDE this git
# repository, so the git tail of the PWD line vanishes deterministically too.
# Run locally with `bash scripts/tests/statusline-account-chip.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATUSLINE="$ROOT/plugins/core/statusline/statusline.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

OUTSIDE_REPO="$WORK/outside-repo"
mkdir -p "$OUTSIDE_REPO"

pass=0
fail=0

ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"; }

# render <script> <transcriptPathOrEmpty> <configDirOrEmpty> <oauthFlagOrEmpty>
# -> prints the rendered statusline on stdout. CLAUDE_CONFIG_DIR and
# SWARMERY_USAGE_OAUTH are explicitly unset first so the test is independent of
# whatever the ambient shell happens to export. Every call gets a fresh, empty
# TMPDIR (see header comment) that is discarded right after.
render() {
  local script="$1" transcript="$2" cfgdir="$3" oauth="$4" tmp stdin_json out
  tmp="$(mktemp -d)"
  stdin_json="$(printf '{"model":{"display_name":"Claude"},"workspace":{"current_dir":"%s","project_dir":"%s"},"transcript_path":"%s"}' \
    "$OUTSIDE_REPO" "$OUTSIDE_REPO" "$transcript")"
  out="$(
    unset CLAUDE_CONFIG_DIR SWARMERY_USAGE_OAUTH SWARMERY_STATUSLINE_FABLE
    [ -n "$cfgdir" ] && export CLAUDE_CONFIG_DIR="$cfgdir"
    [ -n "$oauth" ] && export SWARMERY_USAGE_OAUTH="$oauth"
    printf '%s' "$stdin_json" | TMPDIR="$tmp" bash "$script" 2>/dev/null
  )"
  rm -rf "$tmp"
  printf '%s' "$out"
}

# ── (a) default account: no chip, PLUS the two-step "unchanged" proof ────
OUT_A="$(render "$STATUSLINE" "" "" "")"
if printf '%s' "$OUT_A" | grep -qF '🪪'; then
  bad "(a) default account -> no 🪪 chip in render" "no 🪪 badge" "$OUT_A"
else
  ok
fi

OLD_STATUSLINE="$WORK/statusline-old.sh"
git -C "$ROOT" show HEAD:plugins/core/statusline/statusline.sh > "$OLD_STATUSLINE" 2>/dev/null

# STEP 1 — fixture stability: the OLD script against itself, twice. Must be
# empty, or the fixture is not pinned and STEP 2 below would prove nothing
# (see Д7 rationale: the clock/weather/counters can differ between runs on
# their own, with no code change involved at all).
STEP1_A="$(render "$OLD_STATUSLINE" "" "" "")"
STEP1_B="$(render "$OLD_STATUSLINE" "" "" "")"
DIFF1="$(diff <(printf '%s\n' "$STEP1_A") <(printf '%s\n' "$STEP1_B") 2>&1 || true)"
if [ -z "$DIFF1" ]; then
  ok
else
  bad "(a) STEP 1 -- fixture is pinned (old vs itself)" "empty diff" "$DIFF1"
fi

# STEP 2 — only meaningful once STEP 1 is clean: old vs the new (edited) script.
STEP2_NEW="$(render "$STATUSLINE" "" "" "")"
DIFF2="$(diff <(printf '%s\n' "$STEP1_A") <(printf '%s\n' "$STEP2_NEW") 2>&1 || true)"
if [ -z "$DIFF2" ]; then
  ok
else
  bad "(a) STEP 2 -- default account renders byte-for-byte unchanged" "empty diff" "$DIFF2"
fi

# ── (b) $TRANSCRIPT under a named account's config dir -> chip shows that key ──
CFG_WORK="$WORK/.claude-work"
TRANSCRIPT_B="$CFG_WORK/projects/-some-slug/deadbeef-session.jsonl"
OUT_B="$(render "$STATUSLINE" "$TRANSCRIPT_B" "" "")"
if printf '%s' "$OUT_B" | grep -qF '🪪work'; then
  ok
else
  bad "(b) transcript under .claude-work -> chip shows 'work'" "🪪work present" "$OUT_B"
fi

# ── (c) no transcript, CLAUDE_CONFIG_DIR fallback -> chip shows that key ─
OUT_C="$(render "$STATUSLINE" "" "$CFG_WORK" "")"
if printf '%s' "$OUT_C" | grep -qF '🪪work'; then
  ok
else
  bad "(c) no transcript, CLAUDE_CONFIG_DIR fallback -> chip shows 'work'" "🪪work present" "$OUT_C"
fi

# ── (d) SWARMERY_USAGE_OAUTH=0 in case (b) -> chip still present (Д5) ─────
OUT_D="$(render "$STATUSLINE" "$TRANSCRIPT_B" "" "0")"
if printf '%s' "$OUT_D" | grep -qF '🪪work'; then
  ok
else
  bad "(d) SWARMERY_USAGE_OAUTH=0 -> chip still present" "🪪work present" "$OUT_D"
fi

# ── (e) preflight cache: coverage marker on the chip ─────────────────────────
# render_pf <script> <cacheContentOrEmpty> -> render with a hermetic HOME holding
# (or not) $HOME/.swarmery/run/preflight/<sid>.env, a transcript under that
# HOME's default config dir (account "default"), and a `swarmery` first on PATH
# that touches a sentinel if it is ever executed.
PF_HOME="$WORK/pfhome"
SENT_BIN="$WORK/sentbin"; mkdir -p "$SENT_BIN"
SENTINEL="$WORK/swarmery-was-run"
printf '#!/bin/sh\ntouch "%s"\n' "$SENTINEL" >"$SENT_BIN/swarmery"; chmod +x "$SENT_BIN/swarmery"
PF_SID="0b5e55ed-phase4-sid"
PF_TRANSCRIPT="$PF_HOME/.claude/projects/-outside-repo/$PF_SID.jsonl"
render_pf() {
  local script="$1" cache="$2" tmp stdin_json out
  rm -rf "$PF_HOME"; mkdir -p "$PF_HOME/.swarmery/run/preflight"
  [ -n "$cache" ] && printf '%s\n' "$cache" >"$PF_HOME/.swarmery/run/preflight/$PF_SID.env"
  tmp="$(mktemp -d)"
  stdin_json="$(printf '{"model":{"display_name":"Claude"},"workspace":{"current_dir":"%s","project_dir":"%s"},"transcript_path":"%s"}' \
    "$OUTSIDE_REPO" "$OUTSIDE_REPO" "$PF_TRANSCRIPT")"
  out="$(
    unset CLAUDE_CONFIG_DIR SWARMERY_USAGE_OAUTH SWARMERY_STATUSLINE_FABLE
    printf '%s' "$stdin_json" | HOME="$PF_HOME" PATH="$SENT_BIN:$PATH" TMPDIR="$tmp" bash "$script" 2>/dev/null
  )"
  rm -rf "$tmp"
  printf '%s' "$out"
}
first_line() { printf '%s\n' "$1" | head -1; }

L="$(first_line "$(render_pf "$STATUSLINE" "$(printf 'account=default\nestate=acme\nvarsExpected=13\nvarsPresent=0\nlaunch=0')")")"
if printf '%s' "$L" | grep -qF '🪪default/acme' && printf '%s' "$L" | grep -qF '⚠0/13'; then ok
else bad "(e1) default + estate + 0/13 -> chip and RED 0/13" "🪪default/acme … ⚠0/13" "$L"; fi

L="$(first_line "$(render_pf "$STATUSLINE" "$(printf 'account=default\nestate=acme\nvarsExpected=13\nvarsPresent=5\nlaunch=1')")")"
if printf '%s' "$L" | grep -qF '⚠5/13'; then ok
else bad "(e2) partial coverage -> YELLOW 5/13" "⚠5/13" "$L"; fi

L="$(first_line "$(render_pf "$STATUSLINE" "$(printf 'account=default\nestate=acme\nvarsExpected=13\nvarsPresent=13\nlaunch=1')")")"
if printf '%s' "$L" | grep -qF '🪪default/acme' && ! printf '%s' "$L" | grep -qF '⚠'; then ok
else bad "(e3) complete coverage -> chip, no marker" "🪪default/acme, no ⚠" "$L"; fi

L="$(first_line "$(render_pf "$STATUSLINE" "$(printf 'account=default\nestate=acme\nvarsExpected=0\nvarsPresent=0\nlaunch=0')")")"
if printf '%s' "$L" | grep -qF '🪪default/acme' && ! printf '%s' "$L" | grep -qF '⚠'; then ok
else bad "(e4) zero-credential estate (0/0) is healthy -> chip, NO marker" "🪪default/acme, no ⚠" "$L"; fi

# no cache / malformed cache -> byte-identical to the pre-change script
OLD_NOCACHE="$(render_pf "$OLD_STATUSLINE" "")"
NEW_NOCACHE="$(render_pf "$STATUSLINE" "")"
D="$(diff <(printf '%s\n' "$OLD_NOCACHE") <(printf '%s\n' "$NEW_NOCACHE") 2>&1 || true)"
if [ -z "$D" ]; then ok
else bad "(e5) no cache file -> render byte-identical to the old script" "empty diff" "$D"; fi
for junk in 'varsExpected=13' "$(printf 'estate=a b\nvarsExpected=1\nvarsPresent=0')" "$(printf 'estate=x\nvarsExpected=many\nvarsPresent=0')" "$(printf 'evil=1\nvarsExpected=2\nvarsPresent=0')"; do
  # A fresh old render right before each new one: the TIME line moves with the
  # clock, so a baseline taken minutes earlier can differ for no code reason.
  OLD_NOW="$(render_pf "$OLD_STATUSLINE" "")"
  NEW_JUNK="$(render_pf "$STATUSLINE" "$junk")"
  D="$(diff <(printf '%s\n' "$OLD_NOW") <(printf '%s\n' "$NEW_JUNK") 2>&1 || true)"
  if [ -z "$D" ]; then ok
  else bad "(e6) malformed cache -> byte-identical to no cache" "empty diff" "$D"; fi
done

# (e7) the statusline never executes swarmery
if [ ! -e "$SENTINEL" ]; then ok
else bad "(e7) a swarmery first on PATH is never executed by a render" "no sentinel" "sentinel touched"; fi

# (e8) one name per fact: no snake_case alias in either shell consumer
if ! grep -qE 'vars_expected|vars_present|vars_missing' "$STATUSLINE" "$ROOT/plugins/accounts-pack/hooks/preflight-account.sh"; then ok
else bad "(e8) no snake_case alias" "0 matches" "matches"; fi

printf 'statusline-account-chip: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
