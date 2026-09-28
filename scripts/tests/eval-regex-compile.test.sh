#!/usr/bin/env bash
# Test evals/scripts/check-regex.mjs: every eval regex assertion must compile
# as a JS RegExp. An injected PCRE `(?i)` must fail and name its case.
set -euo pipefail
here="$(cd "$(dirname "$0")/../.." && pwd)"
checker="$here/evals/scripts/check-regex.mjs"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# The checker needs js-yaml from evals/node_modules (npm ci). The validate job
# has none, so skip there; the agent-evals job runs this after `npm ci`.
set +e
node "$checker" "$here/evals/agents" > "$tmp/real.out" 2>&1
rc=$?
set -e
if [ "$rc" -eq 2 ]; then
  echo "SKIP: no YAML parser resolvable (run npm ci in evals/)"
  exit 0
fi
[ "$rc" -eq 0 ] || { echo "FAIL: real evals/agents exit $rc"; cat "$tmp/real.out"; exit 1; }
echo "PASS: real evals/agents compile"

# Fixture: one real agent YAML plus a case with a (?i) pattern JS rejects.
mkdir "$tmp/agents"
cp "$here/evals/agents/forecast.yaml" "$tmp/agents/"
cat >> "$tmp/agents/forecast.yaml" <<'YAML'

- description: injected pcre flag case
  vars: { task: x }
  assert:
    - type: regex
      value: "(?i)must stay within forecast"
YAML
set +e
node "$checker" "$tmp/agents" > "$tmp/bad.out" 2>&1
rc=$?
set -e
[ "$rc" -eq 1 ] || { echo "FAIL: injected (?i) exit $rc, want 1"; cat "$tmp/bad.out"; exit 1; }
grep -q 'forecast.yaml:injected pcre flag case:0' "$tmp/bad.out" \
  || { echo "FAIL: output does not name the case"; cat "$tmp/bad.out"; exit 1; }
echo "PASS: injected (?i) exits 1 and names the case"
