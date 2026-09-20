#!/bin/bash
# worktree-memory-probe.sh — does a headless run inside a daemon-managed
# worktree read the CANONICAL project's auto-memory, or its own empty one?
#
# Claude Code keeps auto-memory per project under
# <home>/.claude/projects/<slug>/memory/, where <slug> encodes an absolute path
# (every '/' and '.' becomes '-'). The daemon never runs a task in the project's
# checkout — it runs it in a worktree, under a different absolute path, which
# encodes to a different slug. If memory followed the session's cwd, every
# headless run would start amnesiac while the operator's real memory sat under
# the checkout's slug.
#
# This script answers that question and prints one of:
#   MATCH        — a worktree session resolved memory to the canonical slug.
#                  Nothing to fix; tools/swarmery/internal/worktree/memory.go
#                  keeps linkMemory = false and its helpers stay dormant.
#   MISMATCH     — a worktree session resolved memory to its OWN slug. Flip
#                  linkMemory to true and wire the call sites memory.go names.
#   INCONCLUSIVE — the load could not be observed: no worktree session has run
#                  here yet, no transcript records an auto-memory attachment, or
#                  the three evidence lines disagree. Not a failure, and never
#                  rounded up to MATCH — run a worktree session, or --live.
#
# Exit status is 0 for MATCH and INCONCLUSIVE, 1 for MISMATCH — a probe that
# cannot see evidence must not be mistaken for one that saw the good outcome.
#
# Default mode is FREE and read-only: it reads transcripts that already exist on
# this machine and writes nothing. --live spends money (one short headless run)
# and is never the default. Named *.sh rather than *.test.sh on purpose, so the
# CI suite — which discovers scripts/tests/*.test.sh — does not pick it up.
#
# Usage:
#   scripts/tests/worktree-memory-probe.sh                   # free, read-only
#   scripts/tests/worktree-memory-probe.sh --worktree <path> # probe one worktree
#   scripts/tests/worktree-memory-probe.sh --live            # costs one run
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# The canonical checkout, not whatever tree this script happens to sit in. The
# common git dir is shared by a repo and all of its worktrees and always lives
# inside the main checkout, so this resolves the same answer whether the script
# is run from the checkout or from a worktree of it — which matters, because
# running it from a worktree is exactly the situation it reasons about.
COMMON_GIT="$(git -C "$ROOT" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
if [ -n "$COMMON_GIT" ]; then
  REPO="$(dirname "$COMMON_GIT")"
else
  REPO="$(git -C "$ROOT" rev-parse --show-toplevel 2>/dev/null || printf '%s' "$ROOT")"
fi
# SWARMERY_CLAUDE_PROJECTS exists so the verdict logic can be exercised against
# a fabricated tree — including the MISMATCH branch, which by construction
# cannot be reproduced on a machine where the answer is MATCH. Leave it unset
# for a real probe.
PROJECTS="${SWARMERY_CLAUDE_PROJECTS:-${HOME}/.claude/projects}"
WORKTREE_ROOT="${SWARMERY_WORKTREE_ROOT:-${HOME}/.swarmery/worktrees}"

LIVE=0
WT_CWD=""
while [ $# -gt 0 ]; do
  case "$1" in
    --live) LIVE=1 ;;
    --worktree)
      shift
      WT_CWD="${1:-}"
      ;;
    -h | --help)
      sed -n '2,35p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      printf 'unknown argument: %s (try --help)\n' "$1" >&2
      exit 2
      ;;
  esac
  shift
done

# slug <abs-path> — the ~/.claude/projects directory name for a path.
slug() { printf '%s' "$1" | tr './' '--'; }

say() { printf '%s\n' "$*"; }
hr() { printf -- '── %s %s\n' "$1" "$(printf '%.0s─' $(seq 1 $((60 - ${#1}))))"; }

CANON_SLUG="$(slug "$REPO")"

hr "what is being compared"
say "repo checkout        : $REPO"
say "canonical slug       : $CANON_SLUG"

# ── pick a worktree session to inspect ───────────────────────────────────────
# Any daemon-managed worktree of THIS repo will do; the newest one has the best
# chance of carrying a transcript. Never look at another project's checkout.
if [ -z "$WT_CWD" ]; then
  for candidate in "$WORKTREE_ROOT/$CANON_SLUG"/*; do
    [ -d "$candidate" ] || continue
    if [ -d "$PROJECTS/$(slug "$candidate")" ]; then
      WT_CWD="$candidate"
    fi
  done
fi

# Fall back to the recorded transcripts alone: a worktree can be reaped long
# after the session that ran in it, and the transcript outlives the directory.
#
# Matched on the cwd the transcript RECORDS, not on the directory name. A name
# test like `*worktrees*$CANON_SLUG*` is an unanchored substring match, so a
# sibling repo whose path merely begins with this one's matches too
# (/Volumes/Work/swarmery-ui → …worktrees--Volumes-Work-swarmery-ui-plan-1),
# and the probe would end up reasoning about another project's session — which
# is exactly what this file must never do.
if [ -z "$WT_CWD" ]; then
  for d in "$PROJECTS"/*/; do
    name="$(basename "$d")"
    rec_cwd="$(grep -hos '"cwd":"[^"]*"' "$d"/*.jsonl 2> /dev/null | head -1 | sed -e 's/^"cwd":"//' -e 's/"$//')"
    [ -n "$rec_cwd" ] || continue
    case "$rec_cwd" in
      "$WORKTREE_ROOT/$CANON_SLUG"/*) WT_CWD="recorded:$name" ;;
      *) continue ;;
    esac
  done
fi

if [ -z "$WT_CWD" ]; then
  say "worktree slug        : (none — no daemon worktree of this repo has ever run)"
  hr "verdict"
  say "INCONCLUSIVE — run any phase or plan of this repo through the daemon, then re-run."
  exit 0
fi

case "$WT_CWD" in
  recorded:*) WT_SLUG="${WT_CWD#recorded:}" ;;
  *) WT_SLUG="$(slug "$WT_CWD")" ;;
esac

say "worktree cwd         : ${WT_CWD#recorded:}"
say "worktree slug        : $WT_SLUG"

WT_DIR="$PROJECTS/$WT_SLUG"
if [ ! -d "$WT_DIR" ]; then
  hr "verdict"
  say "INCONCLUSIVE — no transcripts under $WT_SLUG yet."
  exit 0
fi

# ── evidence 1: which MEMORY.md did the worktree session actually load? ──────
#
# WHY THIS MATCHES A JSON FRAME AND NOT A BARE PATH. The first version of this
# grepped `projects/[^ "']*memory/MEMORY.md` over the whole transcript, which
# matches ANY occurrence of such a path: assistant prose, a Read of the file,
# the output of a tool that happened to print it — and the probe's own stdout,
# recorded into the transcript of the session that ran it, which made every
# later run on the machine confirm itself. Two failures followed from that, both
# demonstrated rather than theorised:
#
#   - it manufactured MATCH. On a machine where memory really IS cwd-keyed, a
#     worktree session that merely MENTIONS the canonical path sets
#     SAW_CANONICAL=1; no worktree memory/ exists (nothing ever writes one), so
#     the loose probe printed MATCH on a MISMATCH machine.
#   - it read contamination as evidence. A real run printed TWO paths here: the
#     canonical one and a stray `projects/-tmp-p3guard/memory/MEMORY.md` left
#     over from a temp-directory test that was only ever DISCUSSED in that
#     transcript.
#
# The injection itself is recorded, structurally: when the harness loads a
# project's auto-memory it writes an attachment record carrying
# `"path":"<abs>/memory/MEMORY.md","type":"AutoMem"`. That frame is what a LOAD
# looks like; a path in prose is not it. Nested copies of a transcript inside
# another transcript are JSON-escaped (`\"path\":\"`), so the unescaped pattern
# below also excludes a session that was reading transcripts. Do not loosen this
# back to a bare path match.
hr "evidence 1 — the memory index the worktree session loaded"
# Belt and braces on top of the frame: drop any record carrying this script's
# own banner strings, so the probe can never read its own output back. Dropping
# is the safe direction — a lost hit downgrades the verdict to INCONCLUSIVE.
REFS="$(
  grep -hsv \
    -e 'worktree-memory-probe' \
    -e 'evidence 1 — the memory index' \
    -e 'resolved to the canonical slug' \
    "$WT_DIR"/*.jsonl 2> /dev/null \
    | grep -ho '"path":"[^"]*/memory/MEMORY\.md","type":"AutoMem"' \
    | sed -e 's/^"path":"//' -e 's|","type":"AutoMem"$||' \
    | sed 's|^.*/\(projects/\)|\1|' \
    | sort -u
)"
if [ -z "$REFS" ]; then
  say "no transcript under $WT_SLUG records an auto-memory attachment"
else
  printf '%s\n' "$REFS" | sed 's/^/  /'
fi

SAW_CANONICAL=0
SAW_OWN=0
if printf '%s\n' "$REFS" | grep -qx "projects/$CANON_SLUG/memory/MEMORY.md"; then SAW_CANONICAL=1; fi
if printf '%s\n' "$REFS" | grep -qx "projects/$WT_SLUG/memory/MEMORY.md"; then SAW_OWN=1; fi
say "resolved to the canonical slug: $([ "$SAW_CANONICAL" = 1 ] && echo yes || echo no)"
say "resolved to the worktree slug : $([ "$SAW_OWN" = 1 ] && echo yes || echo no)"

# ── evidence 2: does the worktree slug own a memory directory at all? ────────
hr "evidence 2 — does the worktree slug carry its own memory/"
if [ -e "$WT_DIR/memory" ]; then
  say "YES — $WT_SLUG/memory exists, so Claude Code is keeping memory per worktree"
  OWN_MEMORY=1
else
  say "no — Claude Code never created a memory/ under the worktree slug"
  OWN_MEMORY=0
fi

# ── evidence 3: the population. Which slugs carry memory at all? ─────────────
hr "evidence 3 — every slug on this machine that carries memory/"
total=0
with_memory=0
worktree_with_memory=0
for d in "$PROJECTS"/*/; do
  [ -d "$d" ] || continue
  total=$((total + 1))
  [ -e "$d/memory" ] || continue
  with_memory=$((with_memory + 1))
  case "$(basename "$d")" in
    *worktrees*) worktree_with_memory=$((worktree_with_memory + 1)) ;;
  esac
done
say "$with_memory of $total slug directories carry memory/; $worktree_with_memory of those are worktree-shaped"

# ── the optional paid run ────────────────────────────────────────────────────
if [ "$LIVE" = 1 ]; then
  hr "live run (this costs money)"
  if [ -n "${CI:-}" ]; then
    say "refusing --live under CI"
    exit 2
  fi
  if ! command -v claude > /dev/null 2>&1; then
    say "the claude CLI is not on PATH — skipping the live run"
  else
    TMP_WT="$WORKTREE_ROOT/$CANON_SLUG/memory-probe-$$"
    BRANCH="swarm/memory-probe-$$"
    # shellcheck disable=SC2329  # invoked indirectly, by the trap below
    cleanup() {
      git -C "$REPO" worktree remove --force "$TMP_WT" > /dev/null 2>&1
      git -C "$REPO" branch -D "$BRANCH" > /dev/null 2>&1
    }
    trap cleanup EXIT
    if git -C "$REPO" worktree add -b "$BRANCH" "$TMP_WT" HEAD > /dev/null 2>&1; then
      say "throwaway worktree: $TMP_WT"
      say "throwaway slug    : $(slug "$TMP_WT")"
      ( cd "$TMP_WT" && claude -p \
        "Print the absolute path of your auto-memory directory and nothing else." ) \
        2>&1 | sed 's/^/  /'
    else
      say "could not create a throwaway worktree — skipping the live run"
    fi
  fi
fi

# ── verdict ──────────────────────────────────────────────────────────────────
#
# All three evidence lines feed this, so the three the docs claim really are
# three. Evidence 3 (the population) used to be printed and then ignored: it is
# now a corroboration gate on MATCH. It cannot RAISE the verdict — one
# worktree-shaped slug carrying memory/ could be a hand-made symlink in another
# repo — but if per-worktree memory demonstrably happens somewhere on this
# machine, this probe is not entitled to call the question settled.
hr "verdict"
if [ "$SAW_OWN" = 1 ] || { [ "$OWN_MEMORY" = 1 ] && [ "$SAW_CANONICAL" = 0 ]; }; then
  say "MISMATCH — a worktree session resolves memory to its own slug."
  say "Set linkMemory = true in tools/swarmery/internal/worktree/memory.go and wire"
  say "LinkMemory into Manager.Acquire and UnlinkMemory into Manager.Remove plus the"
  say "wtjanitor removal path."
  exit 1
fi
if [ "$SAW_CANONICAL" = 1 ] && [ "$OWN_MEMORY" = 0 ]; then
  if [ "$worktree_with_memory" -gt 0 ]; then
    say "INCONCLUSIVE — this worktree loaded the canonical index and owns no memory/,"
    say "but $worktree_with_memory worktree-shaped slug(s) on this machine DO carry memory/."
    say "Evidence 1 and 2 say MATCH, evidence 3 disagrees; find out which of those"
    say "slugs has one and why before trusting either answer."
    exit 0
  fi
  say "MATCH — the worktree session loaded the canonical project's memory index,"
  say "the worktree slug has no memory/ of its own, and no worktree-shaped slug on"
  say "this machine has one either. Claude Code resolves auto-memory to the"
  say "canonical project natively, so linkMemory stays false."
  exit 0
fi
say "INCONCLUSIVE — transcripts exist under $WT_SLUG but none records an auto-memory"
say "attachment naming an index. The canonical project may simply have no memory yet"
say "(check $PROJECTS/$CANON_SLUG/memory), or these sessions predate the attachment"
say "record this probe matches on. Re-run after a fresh worktree session, or use"
say "--live to measure it directly. A probe that cannot see the load says so."
exit 0
