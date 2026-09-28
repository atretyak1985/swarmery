#!/usr/bin/env bash
# Install (or remove) the accounts-pack `claude` shell function in a shell
# profile, so that plain `claude` runs under the account bound to the project
# you are standing in — or the PATH shim, which does the same for any shell
# that reads the profile, including one that never sourced the function.
#
# Usage:
#   install-shell-function.sh [--profile <path>]     install / refresh the block
#   install-shell-function.sh --uninstall [--profile <path>]
#   install-shell-function.sh --status   [--profile <path>]
#   install-shell-function.sh --shim [--real-bin <path>] [--profile <path>]
#   install-shell-function.sh --shim-uninstall [--profile <path>]
#
# The shim is written to $SWARMERY_BIN_DIR/claude (default ~/.swarmery/bin).
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
REAL_BIN=""
SHIM_DIR="${SWARMERY_BIN_DIR:-${HOME}/.swarmery/bin}"
SHIM_DIR="${SHIM_DIR%/}"
SHIM_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/claude-shim.sh"
SHIM_PLACEHOLDER='__SWARMERY_REAL_CLAUDE__'
# The header line every shim this script writes carries (claude-shim.sh line 3).
# A `claude` in the shim dir without it is NOT ours and is never overwritten or
# removed.
SHIM_HEADER='# accounts-pack PATH shim'
# Machine-wide dirs probed after PATH — the SAME order as the Go resolver
# (tools/swarmery/internal/claudebin: systemProbeDirs, then the home-relative
# candidates below). SWARMERY_CLAUDE_PROBE_DIRS overrides only the system half,
# so tests stay hermetic on a machine that really has claude installed there.
SYSTEM_PROBE_DIRS="${SWARMERY_CLAUDE_PROBE_DIRS-/opt/homebrew/bin:/usr/local/bin}"

usage() {
  sed -n '2,15p' "$0" >&2
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
function_body() {
  cat <<'SWARMERY_ACCOUNTS_BLOCK'
claude() {
  if command -v swarmery >/dev/null 2>&1; then
    swarmery account exec -- claude "$@"
    return
  fi
  command claude "$@"
}
SWARMERY_ACCOUNTS_BLOCK
}

# shim_export_line is the PATH export the shim needs, spelled with $HOME for the
# default dir so the profile stays portable across machines. It is the only
# thing --shim may add to a profile, and only when nothing there exports the
# dir already.
shim_export_line() {
  if [ "$SHIM_DIR" = "${HOME}/.swarmery/bin" ]; then
    # shellcheck disable=SC2016  # the $HOME/$PATH are for the PROFILE to expand
    printf '%s\n' 'export PATH="$HOME/.swarmery/bin:$PATH"'
  else
    # shellcheck disable=SC2016
    printf 'export PATH="%s:$PATH"\n' "$SHIM_DIR"
  fi
}

# render_block <with-function 0|1> <with-export 0|1> prints the marker block.
# With the function only, it is byte-identical to the block earlier versions
# wrote, so a refresh of an existing install produces no diff.
render_block() {
  printf '%s\n' "$BEGIN_MARKER"
  [ "$1" = 1 ] && function_body
  [ "$2" = 1 ] && shim_export_line
  printf '%s\n' "$END_MARKER"
}

# extract_block prints the lines strictly inside the accounts-pack block(s).
extract_block() {
  [ -f "$1" ] || return 0
  awk -v b="$BEGIN_MARKER" -v e="$END_MARKER" '
    $0 == b { inside = 1; next }
    $0 == e { inside = 0; next }
    inside == 1 { print }
  ' "$1"
}

block_has_function() { extract_block "$1" | grep -qFx 'claude() {'; }
block_has_export() { extract_block "$1" | grep -qFx -- "$(shim_export_line)"; }

# profile_exports_shim_dir: does the profile, OUTSIDE our block, already put the
# shim dir on PATH? Then --shim adds nothing (the operator's own line wins).
profile_exports_shim_dir() {
  local file="$1" lines
  [ -f "$file" ] || return 1
  lines="$(strip_block "$file" | grep -E '^[[:space:]]*export[[:space:]]+PATH=' || true)"
  [ -n "$lines" ] || return 1
  if printf '%s\n' "$lines" | grep -qF -- "$SHIM_DIR"; then
    return 0
  fi
  if [ "$SHIM_DIR" = "${HOME}/.swarmery/bin" ]; then
    # shellcheck disable=SC2016,SC2088  # literal spellings of the default dir
    printf '%s\n' "$lines" | grep -qF -e '$HOME/.swarmery/bin' -e '${HOME}/.swarmery/bin' -e '~/.swarmery/bin' && return 0
  fi
  return 1
}

# write_profile <with-function> <with-export>: rewrite the profile so it holds
# exactly that block (none at all when both are 0). Returns commit's status:
# 0 written, 1 nothing to do, 2 a step failed (the profile is left as it was).
write_profile() {
  TMP="$(mktemp)" || return 2
  if [ -f "$PROFILE" ]; then
    strip_block "$PROFILE" >"$TMP" || return 2
    end_with_newline "$TMP" || return 2
  fi
  if [ "$1" = 1 ] || [ "$2" = 1 ]; then
    render_block "$1" "$2" >>"$TMP" || return 2
  fi
  commit "$TMP" "$PROFILE"
}

# ── the shim ────────────────────────────────────────────────────────────────

# real_path follows every symlink in <path> (portable: no GNU readlink -f).
real_path() {
  local p="$1" dir base target n=0
  while [ -L "$p" ] && [ "$n" -lt 40 ]; do
    target="$(readlink "$p")"
    case "$target" in
      /*) p="$target" ;;
      *) p="$(dirname "$p")/$target" ;;
    esac
    n=$((n + 1))
  done
  dir="$(cd "$(dirname "$p")" 2>/dev/null && pwd -P)" || return 1
  base="$(basename "$p")"
  printf '%s/%s\n' "$dir" "$base"
}

# under_shim_dir <path>: does <path>, symlinks resolved, live in the shim dir?
under_shim_dir() {
  local rp sd
  rp="$(real_path "$1")" || return 1
  sd="$(cd "$SHIM_DIR" 2>/dev/null && pwd -P)" || sd="$SHIM_DIR"
  case "$rp" in
    "$sd"/*) return 0 ;;
  esac
  case "$1" in
    "$SHIM_DIR"/*) return 0 ;;
  esac
  return 1
}

# find_real_claude: PATH with the shim dir stripped, then the Go resolver's
# probe list (claudebin: /opt/homebrew/bin, /usr/local/bin, ~/.claude/local,
# ~/.local/bin, ~/.npm-global/bin, ~/bin). Prints the first executable hit.
find_real_claude() {
  local d sd rd c found=""
  sd="$(cd "$SHIM_DIR" 2>/dev/null && pwd -P)" || sd="$SHIM_DIR"
  local IFS=:
  # The splits below are unquoted on purpose (IFS=:); `set -f` stops a PATH
  # entry holding a glob character from expanding into other paths.
  set -f
  for d in $PATH; do
    case "$d" in /*) ;; *) continue ;; esac
    rd="$(cd "$d" 2>/dev/null && pwd -P)" || continue
    [ "$rd" = "$sd" ] && continue
    [ "${d%/}" = "$SHIM_DIR" ] && continue
    c="$d/claude"
    if [ -f "$c" ] && [ -x "$c" ]; then found="$c"; break; fi
  done
  if [ -z "$found" ]; then
    for d in $SYSTEM_PROBE_DIRS "$HOME/.claude/local" "$HOME/.local/bin" "$HOME/.npm-global/bin" "$HOME/bin"; do
      [ -n "$d" ] || continue
      c="$d/claude"
      if [ -f "$c" ] && [ -x "$c" ]; then found="$c"; break; fi
    done
  fi
  set +f
  [ -n "$found" ] || return 1
  printf '%s\n' "$found"
}

# path_has_unsafe_chars <path>: true when <path> holds a character that could
# break out of the double quotes it is written inside (the shim's line 2, the
# profile's export line).
path_has_unsafe_chars() {
  # shellcheck disable=SC1003  # '\' is a literal backslash pattern
  case "$1" in
    *'"'* | *'$'* | *'`'* | *'\'* | *$'\n'*) return 0 ;;
  esac
  return 1
}

# dir_is_safe <dir>: the dir (symlinks resolved) exists, is owned by the
# current user and is writable by neither group nor other. Anything else in it
# could have been planted by someone else and is not trusted.
dir_is_safe() {
  local d uid
  d="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
  uid="$(id -u)" || return 1
  [ -n "$(find "$d" -prune -user "$uid" ! -perm -020 ! -perm -002 2>/dev/null)" ]
}

# is_our_shim <path>: a regular file (not a symlink) carrying the shim header
# and an absolute recorded path on line 2 — i.e. something --shim wrote.
is_our_shim() {
  [ -f "$1" ] && [ ! -L "$1" ] || return 1
  sed -n '1,5p' "$1" 2>/dev/null | grep -qF -- "$SHIM_HEADER" || return 1
  sed -n '2p' "$1" 2>/dev/null | grep -qE '^SWARMERY_REAL_CLAUDE="/.*"$'
}

# shim_status prints one line about the shim.
shim_status() {
  local target="$SHIM_DIR/claude" recorded
  if [ ! -f "$target" ]; then
    echo "shim: not installed ($target does not exist)"
    return 0
  fi
  recorded="$(sed -n '2s/^SWARMERY_REAL_CLAUDE="\(.*\)"$/\1/p' "$target")"
  if [ -n "$recorded" ] && [ -x "$recorded" ]; then
    echo "shim: installed at $target → $recorded"
  else
    echo "shim: installed at $target → ${recorded:-?} (MISSING — re-run --shim)"
  fi
}

install_shim() {
  local real target="$SHIM_DIR/claude" candidate
  # The dir is written into the profile's export line: the same characters the
  # recorded real path refuses, plus ':' (it is a PATH entry).
  case "$SHIM_DIR" in
    /*) ;;
    *) echo "accounts-pack: refusing to install the shim: SWARMERY_BIN_DIR '$SHIM_DIR' is not an absolute path" >&2; return 1 ;;
  esac
  if path_has_unsafe_chars "$SHIM_DIR" || [[ "$SHIM_DIR" == *:* ]]; then
    echo "accounts-pack: refusing to install the shim: SWARMERY_BIN_DIR contains a shell metacharacter or ':'" >&2
    return 1
  fi
  if [ ! -f "$SHIM_SRC" ]; then
    echo "accounts-pack: shim source $SHIM_SRC is missing" >&2
    return 1
  fi
  if [ -n "$REAL_BIN" ]; then
    real="$REAL_BIN"
  else
    real="$(find_real_claude)" || {
      echo "accounts-pack: refusing to install the shim: no claude found outside $SHIM_DIR" >&2
      echo "               (the only claude reachable is the shim itself — pass --real-bin <path>)" >&2
      return 1
    }
  fi
  case "$real" in
    /*) ;;
    *) echo "accounts-pack: refusing to install the shim: '$real' is not an absolute path" >&2; return 1 ;;
  esac
  if path_has_unsafe_chars "$real"; then
    echo "accounts-pack: refusing to install the shim: the path of the real claude contains a shell metacharacter" >&2
    return 1
  fi
  if [ ! -f "$real" ] || [ ! -x "$real" ]; then
    echo "accounts-pack: refusing to install the shim: $real is not an executable file" >&2
    return 1
  fi
  if under_shim_dir "$real"; then
    echo "accounts-pack: refusing to install the shim: $real resolves into $SHIM_DIR — that is the shim, not claude" >&2
    return 1
  fi

  # Every refusal happens BEFORE the first write: this function runs under
  # `|| exit 1`, where set -e is off, so each step below returns on failure.
  local need_profile=1
  profile_exports_shim_dir "$PROFILE" && need_profile=0
  if [ "$need_profile" = 1 ]; then
    assert_markers_paired "$PROFILE" || return 1
  fi
  if { [ -e "$target" ] || [ -L "$target" ]; } && ! is_our_shim "$target"; then
    echo "accounts-pack: refusing to install the shim: $target exists and is not an accounts-pack shim — move it aside first" >&2
    return 1
  fi

  (umask 022 && mkdir -p "$SHIM_DIR") || return 1
  if ! dir_is_safe "$SHIM_DIR"; then
    echo "accounts-pack: refusing to install the shim: $SHIM_DIR is not owned by you or is writable by group/other (chmod go-w it)" >&2
    return 1
  fi
  TMP="$(mktemp)" || return 1
  {
    sed -n '1p' "$SHIM_SRC" &&
      printf 'SWARMERY_REAL_CLAUDE="%s"\n' "$real" &&
      sed -n '3,$p' "$SHIM_SRC"
  } >"$TMP" || return 1
  if grep -qF -- "$SHIM_PLACEHOLDER" "$TMP"; then
    echo "accounts-pack: the shim template's placeholder is not on line 2 — refusing to write" >&2
    return 1
  fi
  if [ -f "$target" ] && cmp -s "$TMP" "$target"; then
    echo "accounts-pack: shim already installed at $target (nothing written)"
  else
    # A fresh, unpredictable name created by mktemp itself: a fixed
    # "$target.new" could already exist as a symlink that cp would follow.
    candidate="$(mktemp "$SHIM_DIR/.claude.XXXXXX")" || return 1
    if ! { cat "$TMP" >"$candidate" && chmod 0755 "$candidate" && mv -f "$candidate" "$target"; }; then
      rm -f "$candidate"
      echo "accounts-pack: could not write the shim to $target" >&2
      return 1
    fi
    echo "accounts-pack: shim installed at $target → $real"
  fi

  [ "$need_profile" = 1 ] || return 0
  local fn=0 rc=0
  block_has_function "$PROFILE" && fn=1
  write_profile "$fn" 1 || rc=$?
  case "$rc" in
    0) echo "accounts-pack: added $SHIM_DIR to PATH in $PROFILE — open a new shell" ;;
    1) ;;
    *) echo "accounts-pack: could not update $PROFILE" >&2; return 1 ;;
  esac
  return 0
}

uninstall_shim() {
  local target="$SHIM_DIR/claude" need_profile=0
  if [ -f "$PROFILE" ] && block_has_export "$PROFILE"; then
    need_profile=1
    assert_markers_paired "$PROFILE" || return 1
  fi
  if [ -e "$target" ] || [ -L "$target" ]; then
    if ! is_our_shim "$target"; then
      echo "accounts-pack: refusing to remove $target: it is not an accounts-pack shim" >&2
      return 1
    fi
    rm -f "$target" || return 1
    echo "accounts-pack: shim removed from $target"
  else
    echo "accounts-pack: no shim at $target (nothing removed)"
  fi
  [ "$need_profile" = 1 ] || return 0
  local fn=0 rc=0
  block_has_function "$PROFILE" && fn=1
  write_profile "$fn" 0 || rc=$?
  case "$rc" in
    0) echo "accounts-pack: removed the shim's PATH line from $PROFILE" ;;
    1) ;;
    *) echo "accounts-pack: could not update $PROFILE" >&2; return 1 ;;
  esac
  return 0
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
    cp -p "$target" "${target}.bak" || return 2
    echo "accounts-pack: backed up ${target} → ${target}.bak" >&2
  fi
  cat "$candidate" >"$target" || return 2
  return 0
}

# ── argument parsing ────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
  case "$1" in
    --uninstall) MODE="uninstall" ;;
    --status) MODE="status" ;;
    --shim) MODE="shim" ;;
    --shim-uninstall) MODE="shim-uninstall" ;;
    --real-bin)
      [ $# -ge 2 ] || { echo "accounts-pack: --real-bin needs a path" >&2; exit 2; }
      REAL_BIN="$2"
      shift
      ;;
    --real-bin=*) REAL_BIN="${1#--real-bin=}" ;;
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
# `if`, not `[ … ] && rm`: when a mode falls off the end of the script, this
# trap's status becomes the script's, and a bare `&&` with TMP unset is 1.
cleanup() { if [ -n "$TMP" ]; then rm -f "$TMP"; fi; }
trap cleanup EXIT

# ── modes ───────────────────────────────────────────────────────────────────

case "$MODE" in
  status)
    if [ ! -f "$PROFILE" ]; then
      echo "not installed — $PROFILE does not exist"
    elif block_has_function "$PROFILE"; then
      echo "installed in $PROFILE"
    else
      echo "not installed in $PROFILE"
    fi
    shim_status
    ;;

  shim)
    install_shim || exit 1
    ;;

  shim-uninstall)
    uninstall_shim || exit 1
    ;;

  install)
    assert_markers_paired "$PROFILE"
    EXPORT=0
    block_has_export "$PROFILE" && EXPORT=1
    if write_profile 1 "$EXPORT"; then
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
    # The shim's PATH line (if --shim added one) outlives the function: the
    # shim is removed by --shim-uninstall, not by this.
    EXPORT=0
    block_has_export "$PROFILE" && EXPORT=1
    TMP="$(mktemp)"
    strip_block "$PROFILE" >"$TMP"
    [ "$EXPORT" = 1 ] && render_block 0 1 >>"$TMP"
    if commit "$TMP" "$PROFILE"; then
      echo "accounts-pack: shell function removed from $PROFILE"
      echo "               already-open shells keep the function until they are restarted (or: unset -f claude)"
    else
      echo "accounts-pack: nothing to remove from $PROFILE"
    fi
    ;;
esac
