#!/bin/bash
# post_bash_index_check.sh — Graphify graph staleness check after Bash tool calls.
#
# Compares the project's graphify-out/graph.json `built_at_commit` against the
# repo's current git HEAD. If they differ, the on-disk graph is stale, so it
# nudges the agent to run `graphify update .` — but at most ONCE per session, so
# the nudge is not re-injected after every Bash call (a pure context tax nobody
# acts on). The once-per-session gate uses a marker file keyed by the session_id
# carried in the PostToolUse hook stdin JSON. Otherwise silent.
#
# Contract: ALWAYS emits valid JSON and exits 0 (fails open — never blocks a tool
# call). A missing/unparseable session_id degrades to the pre-gate behavior
# (nudge on every stale call) rather than suppressing the nudge.

ROOT="${CLAUDE_PROJECT_DIR:-.}"
GRAPH="$ROOT/graphify-out/graph.json"

# Read the hook payload once (may be empty or malformed — parsing tolerates both).
input=$(cat)

# No graph or no git → nothing to check; pass through.
if [ ! -f "$GRAPH" ] || ! command -v git >/dev/null 2>&1; then
  echo '{"continue": true}'
  exit 0
fi

built=$(grep -m1 -o '"built_at_commit": *"[0-9a-f]*"' "$GRAPH" 2>/dev/null | grep -o '[0-9a-f]\{7,\}')
head_sha=$(git -C "$ROOT" rev-parse HEAD 2>/dev/null)

# Fresh (or unresolvable SHAs) → silent pass-through.
if [ -z "$built" ] || [ -z "$head_sha" ]; then
  echo '{"continue": true}'
  exit 0
fi
case "$head_sha" in
  "$built"*)  # fresh
    echo '{"continue": true}'
    exit 0
    ;;
esac

# ── Stale ────────────────────────────────────────────────────────────────────
# Extract session_id from the stdin JSON (jq preferred; python3 fallback if jq is
# absent). Both silence errors and return empty on malformed/missing input.
if command -v jq >/dev/null 2>&1; then
  session_id=$(printf '%s' "$input" | jq -r '.session_id // empty' 2>/dev/null)
elif command -v python3 >/dev/null 2>&1; then
  session_id=$(printf '%s' "$input" | python3 -c 'import sys,json;
try:
    print(json.load(sys.stdin).get("session_id") or "")
except Exception:
    print("")' 2>/dev/null)
else
  session_id=""
fi

# With a resolvable session_id, nudge at most once: a marker means we already
# nudged this session, so stay silent. Missing session_id → fail open (nudge every
# time, as before), and never create a marker.
#
# The marker lives in the private run dir (lib/session-file.sh), and the id is
# reduced to [A-Za-z0-9_-] before it names a file: it comes from the payload,
# and a `/` or `..` in it must not choose where the marker lands. No usable run
# dir → no marker, which is the same fail-open as a missing id.
session_id=$(printf '%s' "$session_id" | tr -cd 'A-Za-z0-9_-')
run_dir=""
if [ -n "$session_id" ]; then
  # shellcheck source=lib/session-file.sh
  . "$(dirname "${BASH_SOURCE[0]}")/lib/session-file.sh" 2>/dev/null \
    && run_dir=$(swarmery_run_dir 2>/dev/null) || run_dir=""
fi
if [ -n "$session_id" ] && [ -n "$run_dir" ]; then
  marker="${run_dir}/graphify-stale-nudge-${session_id}"
  if [ -f "$marker" ]; then
    echo '{"continue": true}'
    exit 0
  fi
  : > "$marker" 2>/dev/null || true
fi

printf '{"continue": true, "systemMessage": "Graphify graph is stale (built at %s, HEAD is %s). Run graphify update . to refresh before trusting impact/query results."}\n' \
  "${built:0:8}" "${head_sha:0:8}"
exit 0
