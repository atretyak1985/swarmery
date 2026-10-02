#!/usr/bin/env bash
# agent-roles.test.sh — every shipped agent has a picker role, and no role
# names an agent that does not exist.
#
# The dashboard's agent picker groups agents by the role in
# plugins/<pack>/agents/roles.json; the daemon only reads it and falls back to
# "domain" for anything missing. Without this gate a new agent (or a renamed
# one) would silently land in the wrong group, so drift is a CI failure here.
#
# The check runs against the live repo first, then against seeded temp trees so
# the gate itself is proven to fail on each defect class.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

# check_roles <root> — prints one PROBLEM line per defect, exits 1 on any.
check_roles() {
  node - "$1" <<'JS'
const fs = require('fs');
const path = require('path');
const root = process.argv[2];
const ROLES = new Set(['orchestrate', 'implement', 'review', 'research', 'ops', 'domain']);
const problems = [];
const pluginsDir = path.join(root, 'plugins');
for (const pack of fs.readdirSync(pluginsDir).sort()) {
  const agentsDir = path.join(pluginsDir, pack, 'agents');
  if (!fs.existsSync(agentsDir)) continue;
  const agents = new Set();
  for (const f of fs.readdirSync(agentsDir)) {
    if (!f.endsWith('.md')) continue;
    const text = fs.readFileSync(path.join(agentsDir, f), 'utf8');
    const m = /^name:\s*["']?([^"'\s]+)/m.exec(text.split('\n').slice(0, 15).join('\n'));
    agents.add(m ? m[1] : f.replace(/\.md$/, ''));
  }
  if (agents.size === 0) continue;
  const rolesPath = path.join(agentsDir, 'roles.json');
  if (!fs.existsSync(rolesPath)) {
    problems.push(`PROBLEM ${pack}: agents/roles.json is missing (${agents.size} agents)`);
    continue;
  }
  let roles;
  try {
    roles = JSON.parse(fs.readFileSync(rolesPath, 'utf8'));
  } catch (e) {
    problems.push(`PROBLEM ${pack}: agents/roles.json is not valid JSON (${e.message})`);
    continue;
  }
  if (roles === null || typeof roles !== 'object' || Array.isArray(roles)) {
    problems.push(`PROBLEM ${pack}: agents/roles.json must be an object {"<agent>": "<role>"}`);
    continue;
  }
  for (const a of [...agents].sort()) {
    if (!(a in roles)) problems.push(`PROBLEM ${pack}: agent '${a}' has no role in agents/roles.json`);
    else if (!ROLES.has(roles[a])) problems.push(`PROBLEM ${pack}: agent '${a}' has unknown role '${roles[a]}'`);
  }
  for (const a of Object.keys(roles).sort()) {
    if (!agents.has(a)) problems.push(`PROBLEM ${pack}: agents/roles.json names '${a}', which is not an agent in this pack`);
  }
}
for (const p of problems) console.log(p);
process.exit(problems.length > 0 ? 1 : 0);
JS
}

expect_pass() {
  local label="$1" root="$2" out
  if out=$(check_roles "$root" 2>&1); then
    echo "ok   $label"; pass=$((pass+1))
  else
    echo "FAIL $label — expected pass, got:"; echo "$out" | sed 's/^/     /'; fail=$((fail+1))
  fi
}

expect_fail() {
  local label="$1" root="$2" needle="$3" out
  if out=$(check_roles "$root" 2>&1); then
    echo "FAIL $label — expected failure, check passed"; fail=$((fail+1))
  elif ! grep -qF "$needle" <<<"$out"; then
    echo "FAIL $label — failed without '$needle':"; echo "$out" | sed 's/^/     /'; fail=$((fail+1))
  else
    echo "ok   $label"; pass=$((pass+1))
  fi
}

# make_agent <root> <pack> <name>
make_agent() {
  mkdir -p "$1/plugins/$2/agents"
  printf -- '---\nname: %s\ndescription: test agent\n---\nbody\n' "$3" > "$1/plugins/$2/agents/$3.md"
}

# 1. the live repo
expect_pass "live repo: every agent has a valid role" "$REPO_ROOT"

# 2. clean fixture (a pack without agents needs no roles.json)
R="$TMP/clean"; make_agent "$R" core alpha; make_agent "$R" core beta
printf '{"alpha":"implement","beta":"review"}\n' > "$R/plugins/core/agents/roles.json"
mkdir -p "$R/plugins/empty-pack/skills"
expect_pass "clean fixture" "$R"

# 3. an agent without an entry
R="$TMP/missing-entry"; make_agent "$R" core alpha; make_agent "$R" core beta
printf '{"alpha":"implement"}\n' > "$R/plugins/core/agents/roles.json"
expect_fail "missing entry" "$R" "agent 'beta' has no role"

# 4. a pack with agents but no roles.json
R="$TMP/missing-file"; make_agent "$R" web-pack gamma
expect_fail "missing roles.json" "$R" "agents/roles.json is missing"

# 5. an entry for an agent that does not exist
R="$TMP/stale-entry"; make_agent "$R" core alpha
printf '{"alpha":"implement","gone":"review"}\n' > "$R/plugins/core/agents/roles.json"
expect_fail "stale entry" "$R" "names 'gone', which is not an agent"

# 6. a role outside the fixed vocabulary
R="$TMP/bad-role"; make_agent "$R" core alpha
printf '{"alpha":"wizard"}\n' > "$R/plugins/core/agents/roles.json"
expect_fail "unknown role" "$R" "unknown role 'wizard'"

# 7. malformed JSON
R="$TMP/bad-json"; make_agent "$R" core alpha
printf '{"alpha":' > "$R/plugins/core/agents/roles.json"
expect_fail "malformed json" "$R" "not valid JSON"

echo "agent-roles: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
