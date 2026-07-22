#!/bin/bash
# post_bash_index_check.sh — Graphify graph staleness check after Bash tool calls.
#
# Compares the project's graphify-out/graph.json `built_at_commit` against the
# repo's current git HEAD. If they differ, the on-disk graph is stale, so it
# nudges the agent to run `graphify update .`. Otherwise silent.
#
# Contract: ALWAYS emits valid JSON and exits 0 (fails open — never blocks a tool call).

ROOT="${CLAUDE_PROJECT_DIR:-.}"
GRAPH="$ROOT/graphify-out/graph.json"

# No graph or no git → nothing to check; pass through.
if [ ! -f "$GRAPH" ] || ! command -v git >/dev/null 2>&1; then
  echo '{"continue": true}'
  exit 0
fi

built=$(grep -m1 -o '"built_at_commit": *"[0-9a-f]*"' "$GRAPH" 2>/dev/null | grep -o '[0-9a-f]\{7,\}')
head_sha=$(git -C "$ROOT" rev-parse HEAD 2>/dev/null)

# Stale only when both SHAs resolved and HEAD does not start with the recorded
# commit (built_at_commit may be abbreviated).
if [ -n "$built" ] && [ -n "$head_sha" ]; then
  case "$head_sha" in
    "$built"*) : ;; # fresh
    *)
      printf '{"continue": true, "systemMessage": "Graphify graph is stale (built at %s, HEAD is %s). Run graphify update . to refresh before trusting impact/query results."}\n' \
        "${built:0:8}" "${head_sha:0:8}"
      exit 0
      ;;
  esac
fi

echo '{"continue": true}'
exit 0
