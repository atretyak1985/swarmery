#!/bin/bash
# graft-prompt-context.sh — UserPromptSubmit hook: answer "where is X" from the
# context graph before the agent starts grepping for it.
#
# WHY. Availability is not adoption. A repo can carry a perfectly good index and
# still see every session open with a widening sequence of greps, because nothing
# ever puts the graph in front of the agent — it has to be remembered and asked
# for. This hook removes the remembering: every prompt is quietly run through
# `graft ask`, and when the graph demonstrably covers the question, the locators
# are handed over as context the agent already has.
#
# WHY IT IS GATED ON COVERAGE. Injecting on every prompt would be worse than
# injecting on none: a stream of confident, irrelevant file:line hits teaches the
# agent to ignore the block, and then the one time it mattered is ignored too. So
# the injection is conditional, reproducing the gate graft's own hook applies in
# relevantRetrieval() (dist/claude/format.js, 0.16.0) — deliberately graft's rule
# rather than one of our own, so a project calibrated under `graft init` does not
# silently change behaviour when it adopts the pack. The rule has two branches:
#
#   1. STRUCTURAL results always pass. When `graft ask` resolves the query to a
#      real symbol it answers from the wiring graph and returns NO coverage
#      fields at all. Graft's own comment is explicit about why they are absent:
#      "the resolved intent is itself the relevance signal". Scoring those as 0
#      would suppress precisely the answers graft is most certain about.
#   2. LEXICAL results — the ones that do carry the numbers — pass the same
#      two-clause floor graft applies:
#
#          coverageStrong >= 0.1  ||  coverage >= 0.5
#
#      0.1 and 0.5 are graft's exported STRONG_FLOOR / HIGH_FLOOR (dist/ask/fuse.js,
#      0.16.0), read from the installed CLI rather than copied from a spec. They are
#      overridable per project via the two env vars below so a graft upgrade that
#      moves them can be tracked without a pack release.
#
# WHY A NUDGE, AND WHY TWICE. Below the gate the hook says nothing about content —
# it has nothing trustworthy to say. It emits one short line suggesting `graft ask
# --source` before a grep sweep. Once is too few (the first prompt of a session is
# usually scene-setting and read past); every prompt is a context tax nobody acts
# on. Two per session, then silence.
#
# Kill switch: SWARMERY_GRAFT_PROMPT=0, or `"graft": {"promptContext": false}` in
# the project's .claude/project.json.
# Contract: only additionalContext JSON on stdout, exit 0 on every path, at most
# one line to stderr on an internal error.

set -uo pipefail

# Drain stdin FIRST, before any early exit. A hook that exits without reading its
# payload leaves the writer holding a closed pipe; under `pipefail` that SIGPIPE
# becomes the caller's exit status. A kill switch must not be able to fail the
# very thing it is standing aside from.
input=$(cat)

[ "${SWARMERY_GRAFT_PROMPT:-1}" = "0" ] && exit 0

# graft is the point of the pack; jq and node are how its JSON is read. Any of
# them missing is a machine that simply cannot run this hook — not an error.
command -v graft >/dev/null 2>&1 || exit 0
command -v jq    >/dev/null 2>&1 || exit 0
command -v node  >/dev/null 2>&1 || exit 0

prompt=$(printf '%s' "$input" | jq -r '.prompt // empty' 2>/dev/null)
session_id=$(printf '%s' "$input" | jq -r '.session_id // empty' 2>/dev/null)

# Short prompts are "yes", "continue", "run it" — conversational turns with no
# question in them. Asking the graph about those spends the timeout to learn
# nothing, on the prompts where latency is most visible.
[ "${#prompt}" -ge 12 ] || exit 0

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
PROJECT_JSON="${PROJECT_DIR}/.claude/project.json"

# ── project.json → graft.* ────────────────────────────────────────────────────
# Every field has a default, so a project with no `graft` block (or no
# project.json at all) is a valid, fully working configuration.
graph_dir="graft"
if [ -f "$PROJECT_JSON" ]; then
  cfg=$(PJ="$PROJECT_JSON" node -e '
    try {
      const g = (JSON.parse(require("fs").readFileSync(process.env.PJ, "utf8")).graft) || {};
      process.stdout.write(
        (typeof g.graphDir === "string" && g.graphDir ? g.graphDir : "graft") + "\n" +
        (g.promptContext === false ? "off" : "on") + "\n"
      );
    } catch (e) { process.stdout.write("graft\non\n"); }
  ' 2>/dev/null)
  [ -n "$cfg" ] && {
    graph_dir=$(printf '%s' "$cfg" | sed -n 1p)
    [ "$(printf '%s' "$cfg" | sed -n 2p)" = "off" ] && exit 0
  }
fi

# No index, nothing to ask. Checked before the CLI call so a repo that has never
# been built costs nothing per prompt.
# An index exists either as a single graph (<graphDir>/.graph/wiring.json) or,
# in a multi-repo workspace `graft build` federated, as <graphDir>/workspace.json
# at the root with one graph per member. `graft ask` run from the root answers
# over the federation, so either marker means there is something to ask.
[ -f "${PROJECT_DIR}/${graph_dir}/.graph/wiring.json" ] || [ -f "${PROJECT_DIR}/${graph_dir}/workspace.json" ] || exit 0

# ── bounded CLI call ──────────────────────────────────────────────────────────
# The hook's installed timeout (hooks.json) is 10s; this is 8, so the hook always
# gets to decide what to do about a slow graph instead of being killed mid-write.
#
# GNU `timeout` is not on macOS, and `gtimeout` only appears with coreutils
# installed, so the usual two-rung ladder degrades to "no bound at all" on the
# exact platform this runs on. perl ships with macOS and its alarm(2) form is a
# real bound, so it is the third rung rather than a shrug.
GRAFT_TIMEOUT="${SWARMERY_GRAFT_TIMEOUT:-8}"
GRAFT_STRONG_FLOOR="${SWARMERY_GRAFT_STRONG_FLOOR:-0.1}"
GRAFT_HIGH_FLOOR="${SWARMERY_GRAFT_HIGH_FLOOR:-0.5}"
if command -v timeout >/dev/null 2>&1; then
  _bounded() { timeout "$GRAFT_TIMEOUT" "$@"; }
elif command -v gtimeout >/dev/null 2>&1; then
  _bounded() { gtimeout "$GRAFT_TIMEOUT" "$@"; }
elif command -v perl >/dev/null 2>&1; then
  _bounded() { perl -e 'alarm shift; exec @ARGV or exit 127' "$GRAFT_TIMEOUT" "$@"; }
else
  # Unbounded would put an unkillable graph read on the interactive path. Better
  # to contribute nothing than to hang every prompt.
  exit 0
fi

# --no-refresh is load-bearing: without it a stale index makes `ask` rebuild, and
# a rebuild on the interactive path is exactly the latency this hook exists to
# avoid. A stale graph answering slightly out of date is the acceptable trade;
# the answer carries file:line pointers the agent verifies by opening them.
result=$(cd "$PROJECT_DIR" && _bounded graft ask "$prompt" --json -n 5 --no-refresh 2>/dev/null)
[ -n "$result" ] || exit 0

# ── the gate ──────────────────────────────────────────────────────────────────
# One node call does gate + render, so a strong hit costs a single process.
# It prints either the context block or the literal "WEAK", and nothing else.
rendered=$(GRAFT_JSON="$result" \
  STRONG_FLOOR="$GRAFT_STRONG_FLOOR" HIGH_FLOOR="$GRAFT_HIGH_FLOOR" node -e '
  const raw = process.env.GRAFT_JSON;
  let j;
  try { j = JSON.parse(raw); } catch (e) { process.exit(3); }
  const hits = Array.isArray(j.hits) ? j.hits : [];
  if (hits.length === 0) { process.stdout.write("WEAK"); process.exit(0); }

  const isNum = (v) => typeof v === "number" && Number.isFinite(v);
  const floor = (name, def) => {
    const n = Number.parseFloat(process.env[name]);
    return Number.isFinite(n) ? n : def;
  };
  // Branch 1: no coverage fields at all = a structural answer. Graft resolved
  // the query to a real symbol, so the resolution IS the relevance signal.
  const lexical = isNum(j.coverage) || isNum(j.coverageStrong);
  const pass = !lexical ||
    (j.coverageStrong ?? 0) >= floor("STRONG_FLOOR", 0.1) ||
    (j.coverage ?? 0) >= floor("HIGH_FLOOR", 0.5);
  if (!pass) { process.stdout.write("WEAK"); process.exit(0); }

  const lines = hits.slice(0, 5).map((h) => {
    const title = String(h.title || h.name || "?").trim();
    const ptr = String(h.pointer || "").trim();
    return ptr ? "  " + title + " — " + ptr : "  " + title;
  });
  // The header names WHY these hits are here: a resolved subject reads very
  // differently from a 0.14 lexical match, and the agent should weigh them so.
  const header = lexical
    ? "Context graph hits for this prompt (graft, coverage " + (j.coverage ?? 0).toFixed(2) + "):"
    : "Context graph — graft resolved this prompt to " +
      (j.subject ? String(j.subject) : "a symbol") +
      (j.note ? " (" + j.note + ")" : "") + ":";
  process.stdout.write(
    header + "\n" + lines.join("\n") +
      "\n\nThese are graph locators, not a read of the code — open what you rely on.\n" +
      "For the code inline instead: graft ask \"<question>\" --source -n 5\n"
  );
' 2>/dev/null)
node_rc=$?

if [ "$node_rc" -ne 0 ]; then
  printf 'graft-prompt-context: could not parse the JSON from graft ask\n' >&2
  exit 0
fi

if [ "$rendered" != "WEAK" ] && [ -n "$rendered" ]; then
  jq -n --arg ctx "$rendered" \
    '{hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext: $ctx}}'
  exit 0
fi

# ── weak match: nudge, at most twice per session ──────────────────────────────
# No session id means no way to tell a first prompt from a fiftieth, and a nudge
# that cannot count must not fire at all — that is the every-prompt tax.
[ -n "$session_id" ] || exit 0
safe_session=$(printf '%s' "$session_id" | tr -c 'A-Za-z0-9_.-' '_')
marker_dir="${TMPDIR:-/tmp}/swarmery-graft-nudge"
marker="${marker_dir}/${safe_session}"

count=0
[ -f "$marker" ] && count=$(cat "$marker" 2>/dev/null)
case "$count" in
  ''|*[!0-9]*) count=0 ;;
esac
[ "$count" -ge 2 ] && exit 0

mkdir -p "$marker_dir" 2>/dev/null || exit 0
printf '%s' "$((count + 1))" > "$marker" 2>/dev/null || exit 0

jq -n --arg ctx \
  "The context graph has no strong match for this prompt, so no locators were injected.
If you are about to look for code, run \`graft ask \"<question>\" --source\` before a grep
sweep — it is ranked and returns the code at each file:line, where grep returns every
occurrence unranked." \
  '{hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext: $ctx}}'
exit 0
