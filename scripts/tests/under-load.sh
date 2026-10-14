#!/bin/bash
# under-load.sh [-j N] -- <cmd> [args...]
#
# Runs <cmd> in the foreground while N busy loops keep every CPU occupied, then
# exits with <cmd>'s own exit code. Built to reproduce test flakes that pass
# alone and fail only under load, on demand and with no stress-ng dependency,
# so it behaves the same on a dev Mac (bash 3.2) and on CI ubuntu (bash 5).
#
#   -j N   number of burners; defaults to the online CPU count
#          (getconf _NPROCESSORS_ONLN, then sysctl -n hw.ncpu, then 4).
#
# UNDER_LOAD_PIDFILE, when set, receives the burner PIDs (one per line) before
# <cmd> starts. The test suite uses it to prove no burner outlives the run.
#
# Deliberately NOT named *.test.sh: CI runs every scripts/tests/*.test.sh as a
# suite, and this is a tool. Its suite is under-load.test.sh.
set -u

usage() {
  printf 'usage: %s [-j N] -- <cmd> [args...]\n' "${0##*/}" >&2
  exit 2
}

default_jobs() {
  local n
  n="$(getconf _NPROCESSORS_ONLN 2>/dev/null)" || n=""
  case "$n" in '' | *[!0-9]* | 0) n="$(sysctl -n hw.ncpu 2>/dev/null)" || n="" ;; esac
  case "$n" in '' | *[!0-9]* | 0) n=4 ;; esac
  printf '%s\n' "$n"
}

jobs=""
seen_sep=0
while [ $# -gt 0 ]; do
  case "$1" in
    -j)
      [ $# -ge 2 ] || usage
      jobs="$2"
      shift 2
      ;;
    -j*)
      jobs="${1#-j}"
      shift
      ;;
    --)
      seen_sep=1
      shift
      break
      ;;
    *) usage ;;
  esac
done
[ "$seen_sep" -eq 1 ] || usage
[ $# -gt 0 ] || usage

[ -n "$jobs" ] || jobs="$(default_jobs)"
case "$jobs" in '' | *[!0-9]* | 0) usage ;; esac

burners=()
# shellcheck disable=SC2329  # invoked from the traps below
cleanup() {
  if [ "${#burners[@]}" -gt 0 ]; then
    kill "${burners[@]}" 2>/dev/null
    wait "${burners[@]}" 2>/dev/null
    burners=()
  fi
}
# Installed before the first burner starts, so a signal mid-startup still reaps
# whatever was already running. Subshells do not inherit these traps.
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

i=0
while [ "$i" -lt "$jobs" ]; do
  (while :; do :; done) &
  burners+=("$!")
  i=$((i + 1))
done

if [ -n "${UNDER_LOAD_PIDFILE:-}" ]; then
  printf '%s\n' "${burners[@]}" >"$UNDER_LOAD_PIDFILE"
fi

printf 'under-load: %s burners around: %s\n' "$jobs" "$*" >&2

"$@"
rc=$?
exit "$rc"
