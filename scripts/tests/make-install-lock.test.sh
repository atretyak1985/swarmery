#!/bin/bash
# Behavioral tests for the machine-wide lock around `make install` in
# tools/swarmery/Makefile.
#
# Framework-free. Every case runs the REAL install recipe with INSTALL_LOCK
# pointed into a throwaway dir and INSTALL_STEP pointed at a stub makefile, so
# nothing is built, swapped or restarted. It asserts that:
#   (a) a free lock runs the step and is released afterwards;
#   (b) a failing step fails the install and still releases the lock;
#   (c) a lock held by a live pid makes install wait until it is released;
#   (d) a lock whose pid is gone is taken over at once;
#   (e) two concurrent installs run their steps one after the other.
# Observed 2026-10-19: two sessions' installs overlapped and the second's
# `npm ci` wiped web/node_modules under the first's vite build.
# Run locally with `bash scripts/tests/make-install-lock.test.sh`; CI discovers
# scripts/tests/*.test.sh.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MK_DIR="$ROOT/tools/swarmery"

pass=0
fail=0
fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

tmp="$(mktemp -d)"
holder_pid=""
cleanup() {
	[ -n "$holder_pid" ] && kill "$holder_pid" 2>/dev/null
	rm -rf "$tmp"
}
trap cleanup EXIT

# The stub step: log a start and an end line around a short sleep, or fail
# when STUB_FAIL=1. LOG is per case.
cat >"$tmp/stub.mk" <<'MK'
stub:
	@echo "start $$(date +%s) $$$$" >>"$(LOG)"
	@sleep $${STUB_SLEEP:-0}
	@echo "end $$(date +%s) $$$$" >>"$(LOG)"
	@[ "$${STUB_FAIL:-0}" != 1 ]
MK

# run_install <lock> <log> — the real recipe, the stub as its step.
run_install() {
	make -s -C "$MK_DIR" install \
		INSTALL_LOCK="$1" LOG="$2" \
		INSTALL_STEP="-f $tmp/stub.mk stub" >/dev/null 2>&1
}

# --- (a) free lock ---------------------------------------------------------
lock="$tmp/a.lock"
log="$tmp/a.log"
if run_install "$lock" "$log" && grep -q '^end ' "$log" && [ ! -e "$lock" ]; then
	ok_case
else
	fail_case "(a) a free lock runs the step and releases the lock"
fi

# --- (b) failing step ------------------------------------------------------
lock="$tmp/b.lock"
log="$tmp/b.log"
if STUB_FAIL=1 run_install "$lock" "$log"; then
	fail_case "(b) a failing step must fail the install"
elif [ -e "$lock" ]; then
	fail_case "(b) a failing step must still release the lock"
else
	ok_case
fi

# --- (c) live holder: wait, then run once released --------------------------
lock="$tmp/c.lock"
log="$tmp/c.log"
sleep 60 &
holder_pid=$!
mkdir "$lock"
echo "$holder_pid" >"$lock/pid"
run_install "$lock" "$log" &
waiter=$!
sleep 2
if [ -s "$log" ]; then
	fail_case "(c) install ran its step while a live pid held the lock"
else
	rm -rf "$lock"
	wait "$waiter"
	rc=$?
	if [ "$rc" -eq 0 ] && grep -q '^end ' "$log"; then
		ok_case
	else
		fail_case "(c) install did not proceed after the lock was released (rc=$rc)"
	fi
fi
kill "$holder_pid" 2>/dev/null
wait "$holder_pid" 2>/dev/null
holder_pid=""

# --- (d) stale lock: dead pid is taken over ---------------------------------
lock="$tmp/d.lock"
log="$tmp/d.log"
sleep 0 &
dead=$!
wait "$dead"
mkdir "$lock"
echo "$dead" >"$lock/pid"
start=$(date +%s)
if run_install "$lock" "$log" && grep -q '^end ' "$log" && [ ! -e "$lock" ] &&
	[ $(($(date +%s) - start)) -lt 5 ]; then
	ok_case
else
	fail_case "(d) a lock whose pid is gone is taken over at once"
fi

# --- (e) two concurrent installs serialize ----------------------------------
lock="$tmp/e.lock"
log="$tmp/e.log"
STUB_SLEEP=2 run_install "$lock" "$log" &
first=$!
STUB_SLEEP=2 run_install "$lock" "$log" &
second=$!
wait "$first"
rc1=$?
wait "$second"
rc2=$?
# Serialized means the log alternates start/end/start/end: no second start
# before the first end.
order="$(awk '{print $1}' "$log" 2>/dev/null | tr '\n' ' ')"
if [ "$rc1" -eq 0 ] && [ "$rc2" -eq 0 ] && [ "$order" = "start end start end " ] && [ ! -e "$lock" ]; then
	ok_case
else
	fail_case "(e) concurrent installs overlapped (order: $order rc=$rc1/$rc2)"
fi

echo "make-install-lock: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
