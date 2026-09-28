#!/bin/bash
# Regression test for the "📁 <n> Memories" field of the statusline — BOTH
# halves of how it finds a project's auto-memory:
#   plugins/core/statusline/statusline.sh — MEM_DIR.
#
#   (1) the SLUG: which directory name under <cfgdir>/projects, and
#   (2) the ACCOUNT: which <cfgdir> to look in at all.
#
# WHY THIS EXISTS. Claude Code names each per-project directory by mapping
# EVERY character outside [A-Za-z0-9] to '-' — '/', '.', '_', '+', space and
# brackets alike. So `/Users/dev/.local/src/my_app+v2` is written as
# `-Users-dev--local-src-my-app-v2`: a dot segment produces a DOUBLED dash, and
# the underscore and the plus collapse into dashes too. The statusline used to
# encode '/' and '.' only, so for any project path carrying a third character
# MEM_DIR pointed at a directory that never exists and the counter silently read
# 0 forever. The rule is read out of the shipped Claude Code binary; the
# canonical encoder is tools/swarmery/internal/claudeproj.Slug, and the shell in
# the statusline is a copy only because a statusline hook cannot call Go.
#
# The second half is the account. An operator running several subscriptions
# launches Claude Code with a different CLAUDE_CONFIG_DIR per account, and the
# per-project memory then lives under THAT dir. Reading $HOME/.claude instead
# reports a different account's file count on every prompt.
#
# NOT TAUTOLOGICAL, on purpose. The expected slug's discriminating tail is a
# LITERAL here — `-proj--local-src-my-app-v2` — not a value recomputed from the
# rule under test. A suite that derives its expectation from the encoder it is
# checking can only ever catch "the statusline called the wrong function", never
# "the rule is wrong". Only the temp-root prefix, which is not under test, is
# encoded programmatically.
#
# Note that this is NOT ingest.SlugForPath: the DB identity slug encodes '/'
# only, on purpose, and its own Go test pins that. Never "fix" one to match the
# other.
#
# Framework-free (portable, no bats dependency), fully offline, and hermetic:
# HOME is redirected into a fresh temp dir, so the developer's real
# ~/.claude/projects is never read or written. Each render also gets its own
# brand-new empty TMPDIR so the weather cache is deterministically cold.
# Run locally with `bash scripts/tests/statusline-memory-slug.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATUSLINE="$ROOT/plugins/core/statusline/statusline.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"; }

# ── the fixture: a path carrying a dot segment, an underscore AND a plus ──
# All three are characters the old encoding left alone; `.local` doubles the
# dash, `my_app+v2` collapses two more. Claude Code's own worktree directories
# are where the '+' was first observed in the wild.
FAKE_HOME="$WORK/home"
PROJECT_DIR="$WORK/proj/.local/src/my_app+v2"
mkdir -p "$FAKE_HOME" "$PROJECT_DIR"

# The temp root is not what is under test, so it is encoded once. Everything
# after it — the discriminating part — is written out by hand.
WORK_PREFIX="$(printf '%s' "$WORK" | tr -c 'A-Za-z0-9' '-')"
RIGHT_SLUG="${WORK_PREFIX}-proj--local-src-my-app-v2"
# … and the name the old encoding produced, spelled out the same way. Seeding it
# as a decoy is what turns a regression into "7 vs 3" instead of "0 vs 3".
WRONG_SLUG="${WORK_PREFIX}-proj--local-src-my_app+v2"

if [ "$RIGHT_SLUG" = "$WRONG_SLUG" ]; then
  bad "fixture divergence" "the two encodings differ for this path" \
    "both encode to '$RIGHT_SLUG' — the fixture path lost its discriminating characters"
else
  ok
fi

# ── seed the two directories the two rules point at ──────────────────────
# The real one holds 3 memories (plus a MEMORY.md, which the count excludes);
# the decoy holds 7. The old line can only ever report the decoy.
RIGHT_DIR="$FAKE_HOME/.claude/projects/$RIGHT_SLUG/memory"
WRONG_DIR="$FAKE_HOME/.claude/projects/$WRONG_SLUG/memory"
mkdir -p "$RIGHT_DIR" "$WRONG_DIR"
for n in 1 2 3; do printf 'x\n' > "$RIGHT_DIR/mem-$n.md"; done
printf 'x\n' > "$RIGHT_DIR/MEMORY.md"   # the index is excluded from the count
for n in 1 2 3 4 5 6 7; do printf 'x\n' > "$WRONG_DIR/decoy-$n.md"; done

EXPECTED=3
DECOY=7

# ── the second account: a different config dir, a different count ─────────
# A project bound to a second account keeps its memory under that account's
# config dir. 5 here, so it can be confused with neither 3 nor 7.
OTHER_CFG="$WORK/cfg-other"
OTHER_DIR="$OTHER_CFG/projects/$RIGHT_SLUG/memory"
mkdir -p "$OTHER_DIR"
for n in 1 2 3 4 5; do printf 'x\n' > "$OTHER_DIR/mem-$n.md"; done
OTHER_EXPECTED=5
# statusline.sh's transcript_config_dir() strips exactly three path components
# from transcript_path, so this resolves back to $OTHER_CFG. The shape is the
# one Claude Code really writes: <cfgdir>/projects/<slug>/<uuid>.jsonl.
OTHER_TRANSCRIPT="$OTHER_CFG/projects/$RIGHT_SLUG/11111111-2222-3333-4444-555555555555.jsonl"
printf '{}\n' > "$OTHER_TRANSCRIPT"

# ── render helper ────────────────────────────────────────────────────────
# render <script> [transcriptPath] -> the statusline as rendered for the
# fixture project. With no transcript the script falls back to
# ${CLAUDE_CONFIG_DIR:-$HOME/.claude}, and CLAUDE_CONFIG_DIR is explicitly unset
# so the ambient shell cannot influence the result.
render() {
  local script="$1" transcript="${2:-}" tmp stdin_json out
  tmp="$(mktemp -d)"
  stdin_json="$(printf '{"model":{"display_name":"Claude"},"workspace":{"current_dir":"%s","project_dir":"%s"},"transcript_path":"%s"}' \
    "$PROJECT_DIR" "$PROJECT_DIR" "$transcript")"
  out="$(
    unset CLAUDE_CONFIG_DIR SWARMERY_USAGE_OAUTH SWARMERY_STATUSLINE_FABLE
    printf '%s' "$stdin_json" | HOME="$FAKE_HOME" TMPDIR="$tmp" bash "$script" 2>/dev/null
  )"
  rm -rf "$tmp"
  printf '%s' "$out"
}

# memories_of <render> -> the integer in the "<n> Memories" field, ANSI stripped.
# The ESC byte is built with printf so the expression works under BSD sed (macOS)
# and GNU sed (CI) alike — neither understands a \x1b escape in the pattern.
memories_of() {
  printf '%s\n' "$1" \
    | sed "s/$(printf '\033')\[[0-9;]*m//g" \
    | sed -n 's/.*[^0-9]\([0-9][0-9]*\) Memories.*/\1/p' \
    | head -n 1
}

OUT="$(render "$STATUSLINE")"
GOT="$(memories_of "$OUT")"

# ── (a) the field is present and parseable at all ────────────────────────
case "$GOT" in
  ''|*[!0-9]*)
    bad "(a) the render carries a numeric Memories field" "an integer" "'$GOT' (render: $OUT)" ;;
  *) ok ;;
esac

# ── (b) the count comes from the fully-encoded directory ─────────────────
# This is the assertion that fails against the old '/'-and-'.' encoding.
if [ "$GOT" = "$EXPECTED" ]; then
  ok
elif [ "$GOT" = "$DECOY" ]; then
  bad "(b) memory dir resolves with every non-alphanumeric encoded to '-'" \
    "$EXPECTED (from $RIGHT_SLUG)" \
    "$DECOY — the statusline read $WRONG_SLUG, i.e. it still leaves '_' and '+' alone"
else
  bad "(b) memory dir resolves with every non-alphanumeric encoded to '-'" \
    "$EXPECTED (from $RIGHT_SLUG)" \
    "$GOT — matched neither the real dir nor the old rule's decoy"
fi

# ── (c) and it is emphatically NOT the old rule's directory ──────────────
if [ "$GOT" = "$DECOY" ]; then
  bad "(c) the old rule's directory is not what gets counted" "not $DECOY" "$GOT"
else
  ok
fi

# ── (d) the account half: the count follows the session's config dir ─────
# Same project, same slug — only the transcript path differs, which is how the
# statusline learns which account the session runs under. Reading $HOME/.claude
# here would report 3 (or 7), never 5.
OUT_ACCT="$(render "$STATUSLINE" "$OTHER_TRANSCRIPT")"
GOT_ACCT="$(memories_of "$OUT_ACCT")"
if [ "$GOT_ACCT" = "$OTHER_EXPECTED" ]; then
  ok
else
  bad "(d) memory dir follows the session's account config dir" \
    "$OTHER_EXPECTED (from $OTHER_CFG)" \
    "$GOT_ACCT — the statusline read HOME's config dir instead of the session's"
fi

printf 'statusline-memory-slug: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
