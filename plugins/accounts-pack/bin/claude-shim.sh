#!/bin/sh
SWARMERY_REAL_CLAUDE="__SWARMERY_REAL_CLAUDE__"
# accounts-pack PATH shim — installed as <shim dir>/claude by
#   install-shell-function.sh --shim
# which rewrites line 2 with the absolute path of the REAL claude binary.
#
# Every shell that reads the login profile has the shim dir first on PATH, so a
# plain `claude` — even in a shell opened before the profile's claude()
# function existed — is routed through `swarmery account exec`, which composes
# the project's account and credentials into the child's environment.
#
# Rules this file must keep:
#   1. FAIL OPEN. A terminal never loses `claude`: no swarmery on PATH (or a
#      non-executable one) execs the real binary directly.
#   2. THE LOOP GUARD IS THE ABSOLUTE PATH IN ARGV, AND NOTHING ELSE. The real
#      binary is handed to `account exec` by absolute path, so its resolution
#      never reaches this file again. No environment variable is read as a
#      guard: an inherited marker would stop a nested `claude` started after a
#      `cd` from re-resolving against the new project.
#   3. No output on the happy path; one stderr line and exit 127 only when no
#      real claude exists at all, and one stderr line when the shim dir is not
#      safe (rule 4).
#   4. AN UNSAFE SHIM DIR IS NOT TRUSTED. Not owned by the user, or writable by
#      group/other: anything in it (the swarmery binary included) may have been
#      planted, so the real binary runs directly — still fail open.

shim_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd -P) || shim_dir=""
shim_dir_safe=0
if [ -n "$shim_dir" ] &&
  [ -n "$(find "$shim_dir" -prune -user "$(id -u)" ! -perm -020 ! -perm -002 2>/dev/null)" ]; then
  shim_dir_safe=1
fi

# The recorded binary is gone (an upgrade moved it): re-probe PATH with this
# shim's own directory removed, and skip any candidate that is itself a shim
# (a symlink to this file elsewhere, a second shim dir), so the answer can
# never loop back into a shim.
if [ ! -f "$SWARMERY_REAL_CLAUDE" ] || [ ! -x "$SWARMERY_REAL_CLAUDE" ]; then
  found=""
  old_ifs=$IFS
  IFS=:
  set -f
  for d in $PATH; do
    case "$d" in /*) ;; *) continue ;; esac
    real_d=$(cd "$d" 2>/dev/null && pwd -P) || continue
    [ "$real_d" = "$shim_dir" ] && continue
    if [ -f "$d/claude" ] && [ -x "$d/claude" ]; then
      # -f/-x/head all follow symlinks: this reads the file it resolves to.
      if head -c 512 "$d/claude" 2>/dev/null | grep -qF '# accounts-pack PATH shim'; then
        continue
      fi
      found="$d/claude"
      break
    fi
  done
  set +f
  IFS=$old_ifs
  if [ -z "$found" ]; then
    echo "claude: the swarmery shim's recorded binary is gone and no other claude is on PATH — reinstall claude, then re-run: install-shell-function.sh --shim" >&2
    exit 127
  fi
  SWARMERY_REAL_CLAUDE="$found"
fi

if [ "$shim_dir_safe" != 1 ]; then
  echo "claude: ${shim_dir:-the swarmery shim dir} is not owned by you or is writable by group/other — running claude without swarmery (fix: chmod go-w it)" >&2
  exec "$SWARMERY_REAL_CLAUDE" "$@"
fi

swarmery_bin=$(command -v swarmery 2>/dev/null) || swarmery_bin=""
case "$swarmery_bin" in
  /*) [ -x "$swarmery_bin" ] || swarmery_bin="" ;;
  *) swarmery_bin="" ;;
esac

if [ -z "$swarmery_bin" ]; then
  exec "$SWARMERY_REAL_CLAUDE" "$@"
fi

exec "$swarmery_bin" account exec -- "$SWARMERY_REAL_CLAUDE" "$@"
