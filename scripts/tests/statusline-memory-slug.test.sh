#!/bin/bash
# Regression test for the ~/.claude/projects slug the statusline uses to find a
# project's auto-memory:
#   plugins/core/statusline/statusline.sh — MEM_DIR / the "📁 <n> Memories" field.
#
# WHY THIS EXISTS. Claude Code names each per-project directory under
# `~/.claude/projects` by mapping every '/' AND every '.' to '-', so a dot
# segment produces a DOUBLED dash: `/Users/dev/.local/src/acme` is written as
# `-Users-dev--local-src-acme`. The statusline used to encode '/' only. For any
# project whose path has no dot the two agree, which is exactly why the bug was
# invisible — and for one that does, MEM_DIR pointed at a directory that never
# exists and the Memories counter silently read 0 forever.
#
# The canonical encoder is tools/swarmery/internal/claudeproj.Slug; the shell in
# the statusline is a copy only because a statusline hook cannot call Go, so this
# suite is what keeps the copy honest.
#
# Note that this is NOT ingest.SlugForPath: the DB identity slug encodes '/'
# only, on purpose, and its own Go test pins that. Do not "fix" one to match the
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

# ── the fixture: a project path with a dot segment ───────────────────────
# `.local` is the whole point — it is where the two encodings diverge.
FAKE_HOME="$WORK/home"
PROJECT_DIR="$WORK/proj/.local/src/acme"
mkdir -p "$FAKE_HOME" "$PROJECT_DIR"

# The name Claude Code actually writes ('/' and '.' both become '-') …
RIGHT_SLUG="$(printf '%s' "$PROJECT_DIR" | tr '/.' '--')"
# … and the name the old, '/'-only encoding produced.
WRONG_SLUG="$(printf '%s' "$PROJECT_DIR" | tr '/' '-')"

if [ "$RIGHT_SLUG" = "$WRONG_SLUG" ]; then
  bad "fixture divergence" "the two encodings differ for a dotted path" \
    "both encode to '$RIGHT_SLUG' — the fixture path lost its dot segment"
else
  ok
fi

# Seed BOTH directories with different counts. The real one holds 3 memories;
# the one the old encoding pointed at holds 7. A decoy rather than an empty dir
# is deliberate: it turns the regression from "0 vs 3" (which a merely missing
# directory would also produce) into an unambiguous "7 vs 3" — the old line can
# only ever report the decoy.
RIGHT_DIR="$FAKE_HOME/.claude/projects/$RIGHT_SLUG/memory"
WRONG_DIR="$FAKE_HOME/.claude/projects/$WRONG_SLUG/memory"
mkdir -p "$RIGHT_DIR" "$WRONG_DIR"
for n in 1 2 3; do printf 'x\n' > "$RIGHT_DIR/mem-$n.md"; done
printf 'x\n' > "$RIGHT_DIR/MEMORY.md"   # the index is excluded from the count
for n in 1 2 3 4 5 6 7; do printf 'x\n' > "$WRONG_DIR/decoy-$n.md"; done

EXPECTED=3
DECOY=7

# ── render helper ────────────────────────────────────────────────────────
# render <script> -> the statusline as rendered for the fixture project.
render() {
  local script="$1" tmp stdin_json out
  tmp="$(mktemp -d)"
  stdin_json="$(printf '{"model":{"display_name":"Claude"},"workspace":{"current_dir":"%s","project_dir":"%s"}}' \
    "$PROJECT_DIR" "$PROJECT_DIR")"
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

# ── (b) the count comes from the DOUBLED-dash directory ──────────────────
# This is the assertion that fails against `tr '/' '-'`.
if [ "$GOT" = "$EXPECTED" ]; then
  ok
elif [ "$GOT" = "$DECOY" ]; then
  bad "(b) memory dir resolves with '.' encoded to '-'" \
    "$EXPECTED (from $RIGHT_SLUG)" \
    "$DECOY — the statusline read $WRONG_SLUG, i.e. it still encodes '/' only"
else
  bad "(b) memory dir resolves with '.' encoded to '-'" \
    "$EXPECTED (from $RIGHT_SLUG)" \
    "$GOT — matched neither the real dir nor the '/'-only decoy"
fi

# ── (c) and it is emphatically NOT the '/'-only directory ────────────────
if [ "$GOT" = "$DECOY" ]; then
  bad "(c) the '/'-only directory is not what gets counted" "not $DECOY" "$GOT"
else
  ok
fi

printf 'statusline-memory-slug: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
