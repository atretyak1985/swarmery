#!/bin/bash
# graft-blast.sh — PostToolUse hook on Edit|Write: after a file changes, name
# what depends on it.
#
# WHY. The expensive mistake is not the edit that fails to compile — it is the
# edit that compiles and quietly breaks a caller nobody looked for, because
# checking meant stopping to ask a question. Cost is why it is skipped, so the
# answer has to arrive without being requested. It costs one JSON read.
#
# WHY IT READS wiring.json RATHER THAN SHELLING OUT. `graft callers` is
# symbol-scoped: it takes a symbol name and has no --file flag (verified against
# 0.16.0; see references/commands.md). The question here is per-FILE, and the
# wiring graph answers it directly — file and symbol nodes carry a `path`, so a
# file's nodes are one group and the edges pointing into that group are its
# dependents. A read-only parse also cannot trigger the index refresh a CLI call
# might, which matters on a hook that fires after every single edit.
#
# WHY IT IS CAPPED. Eight dependents is a nudge; forty is a report, and a report
# arriving unbidden after every edit is scrolled past. Over the cap the note says
# how many more there are, which is the part that changes a decision.
#
# Kill switch: SWARMERY_GRAFT_BLAST=0, or `"graft": {"blastRadius": false}` in
# the project's .claude/project.json.
# Contract: only additionalContext JSON on stdout, exit 0 on every path, at most
# one line to stderr on an internal error.

set -uo pipefail

# Drain stdin FIRST — see the same note in graft-prompt-context.sh. An early exit
# that leaves the payload unread can SIGPIPE the caller.
input=$(cat)

[ "${SWARMERY_GRAFT_BLAST:-1}" = "0" ] && exit 0

command -v jq   >/dev/null 2>&1 || exit 0
command -v node >/dev/null 2>&1 || exit 0
# The pack requires the CLI even though this hook does not call it: without graft
# there is no index to read, and a graph left behind by an uninstalled graft is
# stale by definition.
command -v graft >/dev/null 2>&1 || exit 0

file_path=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty' 2>/dev/null)
[ -n "$file_path" ] || exit 0

PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(pwd)}"
PROJECT_JSON="${PROJECT_DIR}/.claude/project.json"

# ── project.json → graft.* ────────────────────────────────────────────────────
graph_dir="graft"
max_dependents=8
if [ -f "$PROJECT_JSON" ]; then
  cfg=$(PJ="$PROJECT_JSON" node -e '
    try {
      const g = (JSON.parse(require("fs").readFileSync(process.env.PJ, "utf8")).graft) || {};
      const n = Number.isInteger(g.maxDependents) && g.maxDependents > 0 ? g.maxDependents : 8;
      process.stdout.write(
        (typeof g.graphDir === "string" && g.graphDir ? g.graphDir : "graft") + "\n" +
        (g.blastRadius === false ? "off" : "on") + "\n" + n + "\n"
      );
    } catch (e) { process.stdout.write("graft\non\n8\n"); }
  ' 2>/dev/null)
  [ -n "$cfg" ] && {
    graph_dir=$(printf '%s' "$cfg" | sed -n 1p)
    [ "$(printf '%s' "$cfg" | sed -n 2p)" = "off" ] && exit 0
    max_dependents=$(printf '%s' "$cfg" | sed -n 3p)
  }
fi

# ── which graph? ──────────────────────────────────────────────────────────────
# A single repo keeps one graph at <project>/<graphDir>. A multi-repo workspace
# (a folder of checkouts with no root .git — `graft build` federates it) keeps
# one graph PER MEMBER, at <member>/<graphDir>, and the root holds only
# workspace.json. Node paths are relative to whichever graph they live in. So
# walk up from the edited file towards the project root and take the nearest
# graph; the root graph is simply the last candidate. An edit outside the
# project has no graph on that walk and no dependents either.
case "$file_path" in
  "${PROJECT_DIR}/"*) ;;
  *) exit 0 ;;
esac
BASE=""
WIRING=""
probe=$(dirname "$file_path")
while :; do
  if [ -f "${probe}/${graph_dir}/.graph/wiring.json" ]; then
    BASE="$probe"
    WIRING="${probe}/${graph_dir}/.graph/wiring.json"
    break
  fi
  [ "$probe" = "$PROJECT_DIR" ] && break
  parent=$(dirname "$probe")
  [ "$parent" = "$probe" ] && break
  probe="$parent"
done
[ -n "$WIRING" ] || exit 0

# ── the edited file, as the graph spells it ───────────────────────────────────
# Node paths are graph-relative and forward-slashed; the hook payload carries an
# absolute path. Strip the graph's base directory.
rel="${file_path#"${BASE}"/}"

# The graph's own directory is not source. Editing a card or the wiring file is
# bookkeeping, and reporting its dependents is noise on every rebuild.
case "$rel" in
  "${graph_dir}"|"${graph_dir}"/*) exit 0 ;;
esac

dependents=$(WIRING="$WIRING" REL="$rel" MAXDEP="$max_dependents" node -e '
  const fs = require("fs");
  let g;
  try { g = JSON.parse(fs.readFileSync(process.env.WIRING, "utf8")); } catch (e) { process.exit(3); }
  const rel = process.env.REL;
  const max = Number.parseInt(process.env.MAXDEP, 10) || 8;
  const nodes = Array.isArray(g.nodes) ? g.nodes : [];
  const edges = Array.isArray(g.edges) ? g.edges : [];

  // Every node declared by the edited file — the file node and each symbol in it.
  const own = new Set();
  const pathOf = new Map();
  for (const n of nodes) {
    if (!n || typeof n.id !== "string") continue;
    pathOf.set(n.id, typeof n.path === "string" ? n.path : "");
    if (n.path === rel) own.add(n.id);
  }
  if (own.size === 0) process.exit(0);   // file not in the graph: silent, not an error

  // An incoming edge is a dependent unless it is the file containing its own
  // symbols (structural) or a call between two symbols of this same file.
  const byPath = new Map();
  for (const e of edges) {
    if (!e || !own.has(e.target)) continue;
    if (e.relation === "contains") continue;
    const src = pathOf.get(e.source);
    if (src === undefined || src === rel) continue;
    const name = String(e.source).includes("#") ? String(e.source).split("#").pop() : src;
    if (!byPath.has(src)) byPath.set(src, { rel: e.relation, names: new Set() });
    // A file that both imports and calls into the edited file is labelled by the
    // call: "imports" is satisfied by a type-only reference, "calls" is a real
    // runtime dependency, and only one label fits on the line.
    if (e.relation === "calls") byPath.get(src).rel = "calls";
    if (name && name !== src) byPath.get(src).names.add(name);
  }
  if (byPath.size === 0) process.exit(0);

  const all = [...byPath.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  const shown = all.slice(0, max).map(([p, v]) => {
    const names = [...v.names].slice(0, 3).join(", ");
    return "  " + p + (names ? " — " + names : "") + " [" + v.rel + "]";
  });
  const more = all.length - shown.length;
  process.stdout.write(
    "Blast radius of " + rel + " — " + all.length +
      (all.length === 1 ? " file depends" : " files depend") + " on it:\n" +
      shown.join("\n") +
      (more > 0 ? "\n  +" + more + " more" : "") +
      "\n\nFrom the graft graph, which may lag the working tree. Check these before\n" +
      "changing anything exported here.\n"
  );
' 2>/dev/null)
node_rc=$?

if [ "$node_rc" -eq 3 ]; then
  printf 'graft-blast: could not parse the wiring graph at %s\n' "$WIRING" >&2
  exit 0
fi
[ -n "$dependents" ] || exit 0

jq -n --arg ctx "$dependents" \
  '{hookSpecificOutput: {hookEventName: "PostToolUse", additionalContext: $ctx}}'
exit 0
