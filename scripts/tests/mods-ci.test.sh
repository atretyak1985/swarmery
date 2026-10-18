#!/usr/bin/env bash
# mods-ci.test.sh — strict validate + engine tests for every mod plugin.
#
# A mod is a plugin whose hooks/hooks.json lists `modules` (a hooks module of
# function hooks, not shell scripts) — the same test the daemon's System → Mods
# scan uses. Mods are discovered, not listed: a hand-kept list silently stops
# covering the next mod someone adds.
#
# For each one:
#   claude plugin validate --strict --json <dir>   reads the manifest and the
#       module's source the way the engine will (hooks, calls, env reads);
#   claude plugin test <dir>                       runs the module's *.test.ts
#       files against the engine.
#
# The CI job `mods` (.github/workflows/ci.yml) installs a pinned Claude Code CLI
# and runs this file. The validate job's "Shell tests" step runs every
# scripts/tests/*.test.sh too, on a runner WITHOUT the CLI: there this prints a
# SKIP line and exits 0. Where the CLI is present, any failure is a hard fail.
# MODS_CI_REQUIRE_CLI=1 (set by the `mods` job) turns a missing CLI into a
# failure too, so a broken install can never pass as a skip.
#
# Run with `bash scripts/tests/mods-ci.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if ! command -v claude >/dev/null 2>&1; then
  if [ "${MODS_CI_REQUIRE_CLI:-0}" = "1" ]; then
    echo "FAIL mods-ci: the claude CLI is not on PATH and MODS_CI_REQUIRE_CLI=1"
    exit 1
  fi
  echo "SKIP mods-ci: the claude CLI is not on PATH (the CI job 'mods' installs it)"
  exit 0
fi

if ! command -v node >/dev/null 2>&1; then
  echo "FAIL mods-ci: node is needed to read hooks/hooks.json"
  exit 1
fi

# One plugin dir (relative to ROOT) per line, for every plugin whose
# hooks/hooks.json carries a non-empty `modules` array. A hooks.json that does
# not parse is printed as `!<dir>` so it fails below instead of being skipped.
# shellcheck disable=SC2016  # a node program, not shell: nothing should expand
mods="$(node -e '
  const fs = require("fs");
  const path = require("path");
  const root = process.argv[1];
  const dir = path.join(root, "plugins");
  for (const name of fs.readdirSync(dir).sort()) {
    const file = path.join(dir, name, "hooks", "hooks.json");
    if (!fs.existsSync(file)) continue;
    let doc;
    try { doc = JSON.parse(fs.readFileSync(file, "utf8")); }
    catch { console.log("!plugins/" + name); continue; }
    if (Array.isArray(doc.modules) && doc.modules.length > 0) console.log("plugins/" + name);
  }
' "$ROOT")" || { echo "FAIL mods-ci: plugin discovery exited non-zero"; exit 1; }

if [ -z "$mods" ]; then
  echo "FAIL mods-ci: no mod plugin found under plugins/ (expected at least one hooks.json with modules)"
  exit 1
fi

echo "mods-ci ($(claude --version 2>/dev/null | head -1))"

pass=0
fail=0
while IFS= read -r p; do
  case "$p" in
    '!'*)
      echo "FAIL ${p#!}: hooks/hooks.json is not valid JSON"
      fail=$((fail + 1))
      continue
      ;;
  esac
  echo "── $p"
  if (cd "$ROOT" && claude plugin validate --strict --json "$p"); then
    echo "ok   validate --strict $p"
    pass=$((pass + 1))
  else
    echo "FAIL validate --strict $p"
    fail=$((fail + 1))
  fi
  if (cd "$ROOT" && claude plugin test "$p"); then
    echo "ok   plugin test $p"
    pass=$((pass + 1))
  else
    echo "FAIL plugin test $p"
    fail=$((fail + 1))
  fi
done <<<"$mods"

printf 'mods-ci: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
