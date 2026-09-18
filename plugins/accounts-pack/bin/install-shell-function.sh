#!/usr/bin/env bash
# Install (or remove) the accounts-pack `claude` shell function in a shell
# profile, so that plain `claude` runs under the account bound to the project
# you are standing in.
#
# Usage:
#   install-shell-function.sh [--profile <path>]     install / refresh the block
#   install-shell-function.sh --uninstall [--profile <path>]
#   install-shell-function.sh --status   [--profile <path>]
#
# The profile is edited ONLY by running this script. Enabling the pack does not
# touch it: a pack that silently rewrites a login profile is not what anyone
# signs up for by ticking a checkbox.
#
# Surgery discipline (the same rules the daemon's settings surgery follows):
#   - the block is fenced by two markers and nothing outside them is touched;
#   - a mismatched pair of markers ABORTS without writing — a half-edited
#     profile is fixed by a human, not guessed at by a script;
#   - the original is copied to <profile>.bak before the FIRST write;
#   - idempotent: a second install produces no diff and no second backup;
#   - --uninstall removes exactly the marker block.
set -euo pipefail

BEGIN_MARKER='# >>> swarmery accounts-pack >>>'
END_MARKER='# <<< swarmery accounts-pack <<<'

MODE="install"
PROFILE="${SWARMERY_ACCOUNTS_PROFILE:-}"

usage() {
  sed -n '2,9p' "$0" >&2
}

# ── the block ───────────────────────────────────────────────────────────────
#
# Three properties this function cannot be shipped without:
#
#   1. it delegates to `swarmery account exec`, which hands the project's WHOLE
#      environment delta to the child through execve. That is the only form
#      that can carry per-account MCP credentials: `swarmery account env`
#      prints to this terminal, so it carries the config dir and deliberately
#      nothing secret. Parsing `account env` here would leave every ${VAR} in a
#      plugin's .mcp.json unexpanded.
#   2. `command claude` in the fallback — NOT `claude`. Calling `claude` inside
#      a function named `claude` recurses until the shell dies. The `claude`
#      handed to `account exec` is safe: swarmery resolves it on PATH with
#      execve, which never sees a shell function.
#   3. a SILENT fallback — no CLI on PATH falls through to plain `claude` with
#      no output. This runs on every single invocation; a warning here would be
#      noise forever. A swarmery that IS present and fails is NOT retried: its
#      exit status is the command's, and re-running claude after a nonzero exit
#      would start a second session behind the operator's back.
block_content() {
  cat <<'SWARMERY_ACCOUNTS_BLOCK'
# >>> swarmery accounts-pack >>>
claude() {
  if command -v swarmery >/dev/null 2>&1; then
    swarmery account exec -- claude "$@"
    return
  fi
  command claude "$@"
}
# <<< swarmery accounts-pack <<<
SWARMERY_ACCOUNTS_BLOCK
}

# ── helpers ─────────────────────────────────────────────────────────────────

# default_profile picks the rc file of the operator's login shell. It refuses to
# guess when the shell is neither bash nor zsh: writing a bash function into a
# fish profile would break every new terminal.
default_profile() {
  case "$(basename "${SHELL:-}")" in
    zsh) printf '%s\n' "${HOME}/.zshrc" ;;
    bash) printf '%s\n' "${HOME}/.bashrc" ;;
    *)
      echo "accounts-pack: cannot tell which profile to edit (SHELL=${SHELL:-unset})." >&2
      echo "               Name it explicitly: --profile ~/.zshrc" >&2
      return 1
      ;;
  esac
}

# marker_count counts whole-line occurrences of a marker in a file.
marker_count() {
  local file="$1" marker="$2" n
  n="$(grep -cFx -- "$marker" "$file" 2>/dev/null)" || n=0
  printf '%s' "${n:-0}"
}

# assert_markers_paired refuses to operate on a profile whose markers were
# hand-edited into an unbalanced state — stripping an unterminated block would
# delete everything after it.
assert_markers_paired() {
  local file="$1" begins ends
  [ -f "$file" ] || return 0
  begins="$(marker_count "$file" "$BEGIN_MARKER")"
  ends="$(marker_count "$file" "$END_MARKER")"
  if [ "$begins" != "$ends" ]; then
    echo "accounts-pack: $file has $begins opening and $ends closing markers." >&2
    echo "               Refusing to edit it — restore the pair by hand and re-run." >&2
    return 1
  fi
  if [ "$begins" -gt 1 ]; then
    echo "accounts-pack: warning: $file has $begins accounts-pack blocks; all of them will be replaced by one." >&2
  fi
  return 0
}

# strip_block prints the file without any accounts-pack block.
strip_block() {
  awk -v b="$BEGIN_MARKER" -v e="$END_MARKER" '
    $0 == b { skip = 1; next }
    $0 == e { skip = 0; next }
    skip != 1 { print }
  ' "$1"
}

# end_with_newline appends a newline when the file does not end in one, so an
# appended block cannot glue itself onto the operator's last line.
end_with_newline() {
  local file="$1"
  [ -s "$file" ] || return 0
  if [ "$(tail -c 1 "$file" | wc -l | tr -d ' ')" -eq 0 ]; then
    printf '\n' >>"$file"
  fi
}

# commit writes the candidate over the profile, backing the original up first.
#
# It writes THROUGH the existing path (`cat >`) instead of moving a temp file
# over it: a dotfile is very often a symlink into a dotfiles repo, and `mv`
# would silently replace that symlink with a regular file.
commit() {
  local candidate="$1" target="$2"
  if [ -f "$target" ] && cmp -s "$candidate" "$target"; then
    return 1 # nothing to do
  fi
  if [ -f "$target" ] && [ ! -f "${target}.bak" ]; then
    cp -p "$target" "${target}.bak"
    echo "accounts-pack: backed up ${target} → ${target}.bak" >&2
  fi
  cat "$candidate" >"$target"
  return 0
}

# ── argument parsing ────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
  case "$1" in
    --uninstall) MODE="uninstall" ;;
    --status) MODE="status" ;;
    --profile)
      [ $# -ge 2 ] || { echo "accounts-pack: --profile needs a path" >&2; exit 2; }
      PROFILE="$2"
      shift
      ;;
    --profile=*) PROFILE="${1#--profile=}" ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "accounts-pack: unknown argument $1" >&2
      usage
      exit 2
      ;;
  esac
  shift
done

if [ -z "$PROFILE" ]; then
  PROFILE="$(default_profile)"
fi

TMP=""
cleanup() { [ -n "$TMP" ] && rm -f "$TMP"; }
trap cleanup EXIT

# ── modes ───────────────────────────────────────────────────────────────────

case "$MODE" in
  status)
    if [ ! -f "$PROFILE" ]; then
      echo "not installed — $PROFILE does not exist"
      exit 0
    fi
    if [ "$(marker_count "$PROFILE" "$BEGIN_MARKER")" -gt 0 ]; then
      echo "installed in $PROFILE"
    else
      echo "not installed in $PROFILE"
    fi
    ;;

  install)
    assert_markers_paired "$PROFILE"
    TMP="$(mktemp)"
    if [ -f "$PROFILE" ]; then
      strip_block "$PROFILE" >"$TMP"
      end_with_newline "$TMP"
    fi
    block_content >>"$TMP"
    if commit "$TMP" "$PROFILE"; then
      echo "accounts-pack: shell function installed in $PROFILE"
      echo "               open a new shell (or: source $PROFILE) — then plain \`claude\` follows the project binding"
    else
      echo "accounts-pack: already installed in $PROFILE (nothing written)"
    fi
    ;;

  uninstall)
    if [ ! -f "$PROFILE" ]; then
      echo "accounts-pack: $PROFILE does not exist (nothing to remove)"
      exit 0
    fi
    assert_markers_paired "$PROFILE"
    if [ "$(marker_count "$PROFILE" "$BEGIN_MARKER")" -eq 0 ]; then
      echo "accounts-pack: not installed in $PROFILE (nothing written)"
      exit 0
    fi
    TMP="$(mktemp)"
    strip_block "$PROFILE" >"$TMP"
    if commit "$TMP" "$PROFILE"; then
      echo "accounts-pack: shell function removed from $PROFILE"
      echo "               already-open shells keep the function until they are restarted (or: unset -f claude)"
    else
      echo "accounts-pack: nothing to remove from $PROFILE"
    fi
    ;;
esac
