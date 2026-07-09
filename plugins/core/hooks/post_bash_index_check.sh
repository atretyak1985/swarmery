#!/bin/bash
# post_bash_index_check.sh — GitNexus index staleness check after Bash tool calls.
#
# Compares each GitNexus-indexed repo's current git HEAD against the commit recorded
# in ~/.gitnexus/registry.json. If they differ, the on-disk index is stale, so it
# nudges the agent to re-run `gitnexus analyze`. Otherwise silent.
#
# Contract: ALWAYS emits valid JSON and exits 0 (fails open — never blocks a tool call).

REG="$HOME/.gitnexus/registry.json"

# No registry or no node → nothing to check; pass through.
if [ ! -f "$REG" ] || ! command -v node >/dev/null 2>&1; then
  echo '{"continue": true}'
  exit 0
fi

node -e '
const out = { continue: true };
try {
  const fs = require("fs"), cp = require("child_process");
  const reg = JSON.parse(fs.readFileSync(process.env.HOME + "/.gitnexus/registry.json", "utf8"));
  const stale = [];
  for (const r of (Array.isArray(reg) ? reg : [])) {
    try {
      const head = cp.execSync("git -C " + JSON.stringify(r.path) + " rev-parse HEAD",
        { stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
      if (r.lastCommit && head && head !== r.lastCommit) stale.push(r.name);
    } catch (e) { /* not a git repo / git missing — skip */ }
  }
  if (stale.length) {
    out.systemMessage = "GitNexus index is stale for: " + stale.join(", ") +
      ". Run `gitnexus analyze <repo-path>` to refresh before trusting impact/query results.";
  }
} catch (e) { /* fail open */ }
process.stdout.write(JSON.stringify(out));
' 2>/dev/null || echo '{"continue": true}'
exit 0
