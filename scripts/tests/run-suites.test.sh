#!/bin/bash
# Behavioral tests for scripts/tests/run-suites.sh, the one file-backed command
# that runs every scripts/tests/*.test.sh suite.
#
# Framework-free (portable, no bats dependency), fully offline. It points the
# runner at a temp dir of stub suites, never at scripts/tests itself, so the
# runner cannot recurse into this file.
# Run locally with `bash scripts/tests/run-suites.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUN_SUITES="$ROOT/scripts/tests/run-suites.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0

ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"; }

SUITES="$WORK/suites"
mkdir -p "$SUITES"
printf '#!/bin/bash\nexit 0\n' >"$SUITES/a.test.sh"
printf '#!/bin/bash\nexit 3\n' >"$SUITES/b.test.sh"
# Not a suite: the runner must ignore anything outside *.test.sh.
printf '#!/bin/bash\nexit 9\n' >"$SUITES/helper.sh"

# ── 1. one passing and one failing suite -> exit 1, the failure named ────────
out="$(bash "$RUN_SUITES" "$SUITES" 2>&1)"
rc=$?
if [ "$rc" -eq 1 ]; then ok
else bad "1. a failing suite -> exit 1" "1" "$rc"; fi
if printf '%s\n' "$out" | grep -qxF 'run-suites: FAIL b.test.sh (exit 3)'; then ok
else bad "1. the failing suite is named with its exit code" "run-suites: FAIL b.test.sh (exit 3)" "$out"; fi
if printf '%s\n' "$out" | grep -qxF 'run-suites: PASS a.test.sh'; then ok
else bad "1. the passing suite is named" "run-suites: PASS a.test.sh" "$out"; fi
if printf '%s\n' "$out" | grep -qxF 'run-suites: 1 passed, 1 failed'; then ok
else bad "1. the summary counts both" "run-suites: 1 passed, 1 failed" "$out"; fi
if ! printf '%s\n' "$out" | grep -qF 'helper.sh'; then ok
else bad "1. a non-*.test.sh file is not run" "no helper.sh line" "$out"; fi

# ── 2. the failing suite removed -> exit 0, nothing failed ───────────────────
rm -f "$SUITES/b.test.sh"
out="$(bash "$RUN_SUITES" "$SUITES" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then ok
else bad "2. all suites pass -> exit 0" "0" "$rc"; fi
if printf '%s\n' "$out" | grep -qxF 'run-suites: 1 passed, 0 failed'; then ok
else bad "2. the summary reports no failure" "run-suites: 1 passed, 0 failed" "$out"; fi

# ── 3. a directory that does not exist -> usage error, not a green run ───────
bash "$RUN_SUITES" "$WORK/missing" >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 2 ]; then ok
else bad "3. a missing directory -> exit 2" "2" "$rc"; fi

# Prefixed run-suites.test, not run-suites: when the runner runs this suite,
# its own summary line must stay the only "run-suites: N passed" line.
printf 'run-suites.test: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
