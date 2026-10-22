#!/bin/bash
# session-file.sh — where the hooks keep per-user state: the per-day session
# log the statusline, /dashboard and the summary hooks read, the subagent start
# stamps, and the once-per-session markers.
# Sourced, never executed. bash 3.2 only (no ${v,,}, declare -A, mapfile).
#
# WHY A PRIVATE DIRECTORY. This state used to sit at fixed names in the shared
# /tmp, where every account on the machine can read it, pre-create it, or swap
# it for a symlink. The session log records every command and file path a
# session touches. It now lives in a run directory only its owner can enter:
#
#   ${SWARMERY_RUN_DIR:-$HOME/.swarmery/run}     mode 0700, owned by the caller
#   └── claude-session-YYYYMMDD.jsonl            mode 0600
#
# That is the same run directory the daemon and accounts-pack already use, and
# SWARMERY_RUN_DIR moves all of them together. SWARMERY_SESSION_FILE still
# overrides the log path itself (tests, one-off redirection).
#
# Writers call swarmery_run_dir / swarmery_session_file, which create and
# tighten. Readers call swarmery_run_dir_path / swarmery_session_file_find,
# which never create anything: a statusline render or a notification must not
# be what makes a directory appear.

# swarmery_run_dir_path — the run dir's path. No side effects. Fails (prints
# nothing) when neither SWARMERY_RUN_DIR nor HOME is set.
swarmery_run_dir_path() {
  if [ -n "${SWARMERY_RUN_DIR:-}" ]; then
    printf '%s\n' "${SWARMERY_RUN_DIR%/}"
  elif [ -n "${HOME:-}" ]; then
    printf '%s\n' "${HOME%/}/.swarmery/run"
  else
    return 1
  fi
}

# swarmery_run_dir — the run dir, created 0700 if absent and tightened to 0700
# if present. Fails when it cannot be created or is not owned by the caller, so
# a directory someone else prepared is never written into.
swarmery_run_dir() {
  local d
  d=$(swarmery_run_dir_path) || return 1
  if [ ! -d "$d" ]; then
    (umask 077 && mkdir -p "$d") 2>/dev/null || return 1
  fi
  [ -d "$d" ] && [ -O "$d" ] || return 1
  chmod 700 "$d" 2>/dev/null || return 1
  printf '%s\n' "$d"
}

# swarmery_session_file — today's session log path for a WRITER, created 0600
# when absent so the first `>>` does not create it under a looser umask.
# SWARMERY_SESSION_FILE wins when set. Fails when the run dir is unusable;
# callers fall back to /dev/null rather than to a shared location.
swarmery_session_file() {
  local f d
  if [ -n "${SWARMERY_SESSION_FILE:-}" ]; then
    f="$SWARMERY_SESSION_FILE"
  else
    d=$(swarmery_run_dir) || return 1
    f="$d/claude-session-$(date +%Y%m%d).jsonl"
  fi
  [ -e "$f" ] || (umask 077 && : >> "$f") 2>/dev/null || true
  printf '%s\n' "$f"
}

# swarmery_session_file_find — today's non-empty session log for a READER, or
# nothing. SWARMERY_SESSION_FILE wins when set. Otherwise the last non-empty
# match of the two names the log has carried in the run dir.
swarmery_session_file_find() {
  local d f found=""
  if [ -n "${SWARMERY_SESSION_FILE:-}" ]; then
    [ -s "$SWARMERY_SESSION_FILE" ] && printf '%s\n' "$SWARMERY_SESSION_FILE"
    return 0
  fi
  d=$(swarmery_run_dir_path) || return 0
  local today
  today=$(date +%Y%m%d)
  for f in "$d"/claude-session-*-"${today}".jsonl "$d/claude-session-${today}.jsonl"; do
    [ -f "$f" ] && [ -s "$f" ] && found="$f"
  done
  [ -n "$found" ] && printf '%s\n' "$found"
  return 0
}
