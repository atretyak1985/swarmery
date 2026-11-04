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
	@echo "start $$(date +%s.%N) $$$$" >>"$(LOG)"
	@if [ -n "$${DIAG_LOCK:-}" ]; then { echo "  [$$$$ ppid=$$PPID] lock/pid=$$(cat "$$DIAG_LOCK/pid" 2>&1)"; ps -o pid,ppid,stat,args -u "$$(id -u)" | grep -E 'make|sh -c|sleep' | grep -v grep | sed 's/^/    /'; } >>"$(LOG).diag" 2>&1; fi
	@sleep $${STUB_SLEEP:-0}
	@echo "end $$(date +%s.%N) $$$$" >>"$(LOG)"
	@[ "$${STUB_FAIL:-0}" != 1 ]
MK

# run_install <lock> <log> — the real recipe, the stub as its step. The make
# output goes to $OUT when set: case (e) keeps it for the failure dump, since it
# carries the "waiting" and "taking over a stale install lock" lines.
run_install() {
	if [ -n "${OUT:-}" ]; then
		# DIAG: trace every recipe command (sh -x) into the per-install output,
		# and every directory create/remove syscall (strace) into $OUT.strace.
		local pre=()
		command -v strace >/dev/null 2>&1 &&
			pre=(strace -f -tt -qq -s 200 -e 'trace=execve,clone,clone3,vfork,fork,wait4,waitid,exit_group,mkdir,mkdirat,rmdir,unlinkat' -o "$OUT.strace")
		"${pre[@]}" make -s -C "$MK_DIR" install .SHELLFLAGS=-xc \
			INSTALL_LOCK="$1" LOG="$2" \
			INSTALL_STEP="-f $tmp/stub.mk stub" >>"$OUT" 2>&1
		return
	fi
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
# DIAG (draft, diag/make-install-lock-flake): this case flakes on GitHub runners
# (about 1 run in 4: "start start end end", the second install never waited)
# and has not reproduced locally. Repeat it, and on an overlap dump both
# installs' make output, the step log with timestamps and pids, and what each
# step saw in the lock dir when it started.
E_REPEAT="${E_REPEAT:-6}"
e_fail=0
if [ -n "${GITHUB_ACTIONS:-}" ] && ! command -v strace >/dev/null 2>&1; then
	sudo apt-get install -y -qq strace >/dev/null 2>&1 || echo "  (strace unavailable)"
fi
for r in $(seq 1 "$E_REPEAT"); do
	lock="$tmp/e$r.lock"
	log="$tmp/e$r.log"
	DIAG_LOCK="$lock" OUT="$tmp/e$r.out1" STUB_SLEEP=2 run_install "$lock" "$log" &
	first=$!
	DIAG_LOCK="$lock" OUT="$tmp/e$r.out2" STUB_SLEEP=2 run_install "$lock" "$log" &
	second=$!
	wait "$first"
	rc1=$?
	wait "$second"
	rc2=$?
	# Serialized means the log alternates start/end/start/end: no second start
	# before the first end.
	order="$(awk '{print $1}' "$log" 2>/dev/null | tr '\n' ' ')"
	if [ "$rc1" -ne 0 ] || [ "$rc2" -ne 0 ] || [ "$order" != "start end start end " ] || [ -e "$lock" ]; then
		e_fail=$((e_fail + 1))
		left=no
		[ -e "$lock" ] && left=yes
		echo "  ── (e) repeat $r/$E_REPEAT: order=$order rc=$rc1/$rc2 lock-left=$left first=$first second=$second"
		echo "  step log:"
		sed 's/^/    /' "$log" 2>/dev/null
		echo "  lock seen by each step:"
		sed 's/^/  /' "$log.diag" 2>/dev/null
		echo "  make output, first:"
		sed 's/^/    /' "$tmp/e$r.out1" 2>/dev/null
		echo "  make output, second:"
		sed 's/^/    /' "$tmp/e$r.out2" 2>/dev/null
		echo "  strace, first (lock path only):"
		grep -F "e$r.lock" "$tmp/e$r.out1.strace" 2>/dev/null | sed 's/^/    /'
		echo "  strace, second (lock path only):"
		grep -F "e$r.lock" "$tmp/e$r.out2.strace" 2>/dev/null | sed 's/^/    /'
		for which in out1 out2; do
			sf="$tmp/e$r.$which.strace"
			# The process that got EEXIST, and everything it and its parent did.
			bad=$(grep -F "e$r.lock\", 0777) = -1 EEXIST" "$sf" 2>/dev/null | awk '{print $1}' | head -1)
			[ -z "$bad" ] && continue
			echo "  full strace of $which around EEXIST pid $bad:"
			par=$(grep -E "(clone|clone3|vfork|fork).*= $bad\$" "$sf" | awk '{print $1}' | head -1)
			echo "    parent=$par"
			grep -E "^($bad|$par) " "$sf" | grep -vE 'rt_sig|SIGCHLD.*resumed' | sed 's/^/    /' | head -60
		done
		echo "  clocksource: $(cat /sys/devices/system/clocksource/clocksource0/current_clocksource 2>/dev/null)"
		echo "  sh=$(readlink -f /bin/sh) make=$(make --version | head -1)"
		echo "  env MAKE*: $(env | grep -E '^MAKE|^MFLAGS|^GNUMAKEFLAGS' | tr '\n' ' ')"
		echo "  mkdir: $(type -a mkdir | tr '\n' ' ')"
		echo "  /tmp: $(df -T "$tmp" | tail -1)"
	fi
done
if [ "$e_fail" -eq 0 ]; then
	ok_case
else
	fail_case "(e) concurrent installs overlapped in $e_fail of $E_REPEAT repeats"
fi

echo "make-install-lock: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
