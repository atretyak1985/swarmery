#!/bin/bash
# Behavioral tests for scripts/tests/under-load.sh, the CPU-load harness used to
# reproduce flakes that are green alone and red under load.
#
# Framework-free (portable, no bats dependency), fully offline. Cases 1-4 run
# with -j 1 or -j 2; case 5 runs 200 short -j 4 harnesses, so the suite stays
# cheap on any machine.
# Run locally with `bash scripts/tests/under-load.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UNDER_LOAD="$ROOT/scripts/tests/under-load.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0

ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"; }

# ── 1. a passing command -> exit 0 ───────────────────────────────────────────
bash "$UNDER_LOAD" -j 1 -- true 2>/dev/null
rc=$?
if [ "$rc" -eq 0 ]; then ok
else bad "1. -j 1 -- true exits 0" "0" "$rc"; fi

# ── 2. the wrapped command's own exit code is propagated ─────────────────────
bash "$UNDER_LOAD" -j 1 -- sh -c 'exit 7' 2>/dev/null
rc=$?
if [ "$rc" -eq 7 ]; then ok
else bad "2. -j 1 -- sh -c 'exit 7' exits 7" "7" "$rc"; fi

# ── 3. burners are alive during the command and all gone after it ────────────
# The wrapped command checks every recorded burner is alive (exit 9 otherwise),
# so the "dead afterwards" half below cannot pass vacuously.
PIDFILE="$WORK/burners.pid"
# shellcheck disable=SC2016  # $1/$p expand inside the wrapped sh, not here
UNDER_LOAD_PIDFILE="$PIDFILE" bash "$UNDER_LOAD" -j 2 -- \
  sh -c 'for p in $(cat "$1"); do kill -0 "$p" || exit 9; done' sh "$PIDFILE" 2>/dev/null
rc=$?
count=0
survivors=""
if [ -f "$PIDFILE" ]; then
  while IFS= read -r pid; do
    [ -n "$pid" ] || continue
    count=$((count + 1))
    if kill -0 "$pid" 2>/dev/null; then
      survivors="$survivors $pid"
      kill "$pid" 2>/dev/null
    fi
  done <"$PIDFILE"
fi
if [ "$rc" -eq 0 ] && [ "$count" -eq 2 ] && [ -z "$survivors" ]; then ok
else bad "3. -j 2: 2 burners alive during the command, none after" \
  "rc=0 count=2 survivors=none" "rc=$rc count=$count survivors=${survivors:-none}"; fi

# ── 4. missing -- or empty command -> usage on stderr, exit 2 ────────────────
bash "$UNDER_LOAD" -j 1 true 2>"$WORK/err-nosep"
rc_nosep=$?
bash "$UNDER_LOAD" -j 1 -- 2>"$WORK/err-empty"
rc_empty=$?
if [ "$rc_nosep" -eq 2 ] && grep -q '^usage:' "$WORK/err-nosep" \
  && [ "$rc_empty" -eq 2 ] && grep -q '^usage:' "$WORK/err-empty"; then ok
else bad "4. missing -- / empty command -> usage on stderr, exit 2" \
  "2/usage and 2/usage" \
  "$rc_nosep/$(head -1 "$WORK/err-nosep") and $rc_empty/$(head -1 "$WORK/err-empty")"; fi

# ── 5. cleanup never hangs on a burner that missed its signal ────────────────
# Regression: a burner SIGTERMed while still inside its fork (holding the
# parent's TERM trap) could drop the signal, spin on, and leave cleanup's `wait`
# blocked forever. `-- true` exits at once, so cleanup races the freshest fork
# on every run; 200 runs because the old code hung about once per 80-400 runs
# under load (bash 5). The watchdog fails the case instead of hanging the suite:
# it trips only when one run makes no progress for 30 s, so a slow machine is
# not a failure, then SIGKILLs the stuck harness and its burners. Only the live
# run's PIDs are kept, so it never signals a recycled PID.
STRESS="$WORK/stress"
mkdir -p "$STRESS"
(
  n=0
  while [ "$n" -lt 200 ]; do
    rm -f "$STRESS/burners" "$STRESS/harness"
    printf '%s\n' "$n" >"$STRESS/progress"
    UNDER_LOAD_PIDFILE="$STRESS/burners" bash "$UNDER_LOAD" -j 4 -- true 2>/dev/null &
    printf '%s\n' "$!" >"$STRESS/harness"
    wait "$!" || exit 1
    n=$((n + 1))
  done
) &
loop=$!
last=""
stalled=0
hung=""
while kill -0 "$loop" 2>/dev/null; do
  sleep 1
  cur="$(cat "$STRESS/progress" 2>/dev/null)"
  if [ "$cur" = "$last" ]; then stalled=$((stalled + 1)); else stalled=0; last="$cur"; fi
  if [ "$stalled" -ge 30 ]; then
    hung="run $cur"
    kill -KILL "$loop" 2>/dev/null
    # shellcheck disable=SC2046  # one PID per word, on purpose
    kill -KILL $(cat "$STRESS/harness" "$STRESS/burners" 2>/dev/null) 2>/dev/null
    break
  fi
done
wait "$loop" 2>/dev/null
rc=$?
if [ -z "$hung" ] && [ "$rc" -eq 0 ]; then ok
else bad "5. 200 x -j 4 -- true: every run exits 0, none stalls 30 s" \
  "hung=none rc=0" "hung=${hung:-none} rc=$rc"; fi

printf 'under-load: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
