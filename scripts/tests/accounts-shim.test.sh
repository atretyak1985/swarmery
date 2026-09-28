#!/bin/bash
# Behavioral tests for the accounts-pack PATH shim:
#   plugins/accounts-pack/bin/claude-shim.sh            (the shim body)
#   plugins/accounts-pack/bin/install-shell-function.sh (--shim / --shim-uninstall)
#
# Framework-free, fully offline and hermetic: a fake `swarmery` (logs the cwd
# it ran in and its argv, then execs the command after `account exec --`) and a
# fake real `claude` (logs its argv, exits 7) live in temp dirs; HOME, the shim
# dir (SWARMERY_BIN_DIR) and the machine-wide probe dirs
# (SWARMERY_CLAUDE_PROBE_DIRS) all point into the temp tree. Nothing under the
# operator's real home is read or written.
# Run locally with `bash scripts/tests/accounts-shim.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALLER="$ROOT/plugins/accounts-pack/bin/install-shell-function.sh"
SHIM_SRC="$ROOT/plugins/accounts-pack/bin/claude-shim.sh"
CLAUDEBIN_GO="$ROOT/tools/swarmery/internal/claudebin/claudebin.go"
BASH_BIN="$(command -v bash)"

W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
W="$(cd "$W" && pwd -P)"

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"; }

export HOME="$W/home"; mkdir -p "$HOME"
export SWARMERY_CLAUDE_PROBE_DIRS=""
SHIMDIR="$W/bin"
REALDIR="$W/real"; mkdir -p "$REALDIR"
SWDIR="$W/sw"; mkdir -p "$SWDIR"
BASEPATH="/usr/bin:/bin"
ARGV_LOG="$W/argv.log"; SW_LOG="$W/sw.log"; SW_ARGV_LOG="$W/sw-argv.log"
export ARGV_LOG SW_LOG SW_ARGV_LOG

# argv is logged one bracketed field per argument, so an argument holding a
# space or an empty argument is visible exactly as it arrived.
cat >"$REALDIR/claude" <<'EOF'
#!/bin/bash
{ printf '[%s]' "$@"; printf '\n'; } >"$ARGV_LOG"
exit "${FAKE_EXIT:-7}"
EOF
chmod +x "$REALDIR/claude"
cat >"$SWDIR/swarmery" <<'EOF'
#!/bin/bash
printf '%s\n' "$PWD" >>"$SW_LOG"
{ printf '[%s]' "$@"; printf '\n'; } >>"$SW_ARGV_LOG"
shift 3   # account exec --
exec "$@"
EOF
chmod +x "$SWDIR/swarmery"

# inst <path-for-the-installer> <args...> -> runs the installer hermetically.
inst() {
  local p="$1"
  shift
  SWARMERY_BIN_DIR="$SHIMDIR" PATH="$p" "$BASH_BIN" "$INSTALLER" "$@"
}
# GNU form first: BSD `stat -c` fails loudly, whereas GNU `stat -f` exits 0.
mode_of() { stat -c '%A' "$1" 2>/dev/null || stat -f '%Sp' "$1" 2>/dev/null; }

# ── 1. the shim is installed correctly ────────────────────────────────────────
RC="$W/rc"; printf '# operator rc\nalias ll="ls -l"\n' >"$RC"
inst "$REALDIR:$BASEPATH" --shim --profile "$RC" >/dev/null 2>&1; rc=$?
m="$(mode_of "$SHIMDIR/claude")"
ph="$(grep -c '__SWARMERY_REAL_CLAUDE__' "$SHIMDIR/claude")"
abs="$(grep -c '^SWARMERY_REAL_CLAUDE="/' "$SHIMDIR/claude")"
rec="$(sed -n '2p' "$SHIMDIR/claude")"
if [ "$rc" -eq 0 ] && [ "$m" = "-rwxr-xr-x" ] && [ "$ph" = "0" ] && [ "$abs" = "1" ] &&
  [ "$rec" = "SWARMERY_REAL_CLAUDE=\"$REALDIR/claude\"" ]; then ok
else bad "1 --shim installs 0755 with the placeholder replaced" "rc 0, -rwxr-xr-x, 0, 1" "rc $rc, $m, $ph, $abs, $rec"; fi

# the profile gained exactly one export line for the shim dir, inside the block
exports="$(grep -cF "export PATH=\"$SHIMDIR:\$PATH\"" "$RC")"
if [ "$exports" = "1" ] && grep -qFx '# >>> swarmery accounts-pack >>>' "$RC" && [ -f "$RC.bak" ]; then ok
else bad "1b profile gets one export line inside the marker block, .bak taken" "1 line + markers + .bak" "$exports; $(cat "$RC")"; fi

# ── 2. idempotent: no diff, no second .bak ────────────────────────────────────
cp -p "$RC" "$W/rc.first"; cp -p "$RC.bak" "$W/rc.bak.first"; cp -p "$SHIMDIR/claude" "$W/shim.first"
inst "$REALDIR:$BASEPATH" --shim --profile "$RC" >/dev/null 2>&1
if cmp -s "$RC" "$W/rc.first" && cmp -s "$RC.bak" "$W/rc.bak.first" && cmp -s "$SHIMDIR/claude" "$W/shim.first"; then ok
else bad "2 a second --shim changes nothing" "no diff" "$(diff "$W/rc.first" "$RC")"; fi

# ── 3. a profile that already exports the dir gets no second export ──────────
RC2="$W/rc2"; printf 'export PATH="%s:$PATH"\n' "$SHIMDIR" >"$RC2"; cp "$RC2" "$W/rc2.orig"
inst "$REALDIR:$BASEPATH" --shim --profile "$RC2" >/dev/null 2>&1
if cmp -s "$RC2" "$W/rc2.orig"; then ok
else bad "3 profile already exporting the shim dir is untouched" "no diff" "$(cat "$RC2")"; fi
# the default dir spelled with $HOME counts too
RC3="$W/rc3"; printf 'export PATH="$HOME/.swarmery/bin:$PATH"\n' >"$RC3"; cp "$RC3" "$W/rc3.orig"
PATH="$REALDIR:$BASEPATH" "$BASH_BIN" "$INSTALLER" --shim --profile "$RC3" >/dev/null 2>&1
if cmp -s "$RC3" "$W/rc3.orig" && [ -x "$HOME/.swarmery/bin/claude" ]; then ok
else bad "3b \$HOME/.swarmery/bin already exported -> no duplicate" "no diff" "$(cat "$RC3")"; fi
rm -rf "$HOME/.swarmery"

# ── 4. function block + shim coexist; a function refresh keeps the export ────
RC4="$W/rc4"; : >"$RC4"
inst "$REALDIR:$BASEPATH" --profile "$RC4" >/dev/null 2>&1
inst "$REALDIR:$BASEPATH" --shim --profile "$RC4" >/dev/null 2>&1
inst "$REALDIR:$BASEPATH" --profile "$RC4" >/dev/null 2>&1
if grep -qFx 'claude() {' "$RC4" && [ "$(grep -cF "export PATH=\"$SHIMDIR:" "$RC4")" = "1" ] &&
  [ "$(grep -cFx '# >>> swarmery accounts-pack >>>' "$RC4")" = "1" ]; then ok
else bad "4 function + export in one block, preserved by a refresh" "both, one block" "$(cat "$RC4")"; fi

# ── 5. refusal: the only claude reachable is the shim ────────────────────────
cp -p "$SHIMDIR/claude" "$SHIMDIR/claude.orig"
ERR="$(inst "$SHIMDIR:$BASEPATH" --shim --profile "$W/rc5" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'refus' && cmp -s "$SHIMDIR/claude" "$SHIMDIR/claude.orig" &&
  [ ! -e "$SHIMDIR/claude.new" ]; then ok
else bad "5 shim-only PATH -> refuse, nothing written" "non-zero, 'refus', no diff" "rc $rc: $ERR"; fi
ERR="$(inst "$REALDIR:$BASEPATH" --shim --real-bin "$SHIMDIR/claude" --profile "$W/rc5" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'refus' && cmp -s "$SHIMDIR/claude" "$SHIMDIR/claude.orig"; then ok
else bad "5b --real-bin pointing at the shim -> refuse" "non-zero, 'refus'" "rc $rc: $ERR"; fi
ln -s "$SHIMDIR/claude" "$W/sneaky-claude"
ERR="$(inst "$REALDIR:$BASEPATH" --shim --real-bin "$W/sneaky-claude" --profile "$W/rc5" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'refus'; then ok
else bad "5c a symlink whose realpath is the shim -> refuse" "non-zero, 'refus'" "rc $rc: $ERR"; fi
rm -f "$SHIMDIR/claude.orig" "$W/rc5"

# ── 6. the loop guard is the argv path and nothing else ───────────────────────
if ! grep -q 'SWARMERY_LAUNCH' "$SHIM_SRC" && [ "$(grep -c 'CLAUDE_CONFIG_DIR' "$SHIM_SRC")" = "0" ] &&
  [ "$(grep -c 'account exec -- "\$SWARMERY_REAL_CLAUDE"' "$SHIM_SRC")" = "1" ] &&
  [ "$(sed -n '2p' "$SHIM_SRC")" = 'SWARMERY_REAL_CLAUDE="__SWARMERY_REAL_CLAUDE__"' ]; then ok
else bad "6 no env marker, no CLAUDE_CONFIG_DIR, one absolute-path exec, placeholder on line 2" "0/0/1/line2" "$(grep -nE 'SWARMERY_LAUNCH|CLAUDE_CONFIG_DIR|account exec' "$SHIM_SRC")"; fi

# ── 7. routed through swarmery; re-resolves after a cd; CLAUDE_CONFIG_DIR is not a guard
PA="$W/projA"; PB="$W/projB"; mkdir -p "$PA" "$PB"
run_pair() {
  : >"$SW_LOG"
  (cd "$PA" && PATH="$SHIMDIR:$SWDIR:$BASEPATH" "$SHIMDIR/claude" --one >/dev/null 2>&1)
  (cd "$PB" && PATH="$SHIMDIR:$SWDIR:$BASEPATH" "$SHIMDIR/claude" --two >/dev/null 2>&1)
  cat "$SW_LOG"
}
LOG_UNSET="$(unset CLAUDE_CONFIG_DIR; run_pair)"
LOG_SET="$(export CLAUDE_CONFIG_DIR="$W/.claude-other"; run_pair)"
want="$(printf '%s\n%s' "$PA" "$PB")"
if [ "$LOG_UNSET" = "$want" ] && [ "$LOG_SET" = "$LOG_UNSET" ]; then ok
else bad "7 A then B, identical with CLAUDE_CONFIG_DIR set and unset" "$want" "unset='$LOG_UNSET' set='$LOG_SET'"; fi
last_argv="$(tail -1 "$SW_ARGV_LOG")"
if [ "$last_argv" = "[account][exec][--][$REALDIR/claude][--two]" ]; then ok
else bad "7b swarmery receives the ABSOLUTE real path in argv" "[account][exec][--][$REALDIR/claude][--two]" "$last_argv"; fi
(cd "$PA" && PATH="$SHIMDIR:$SWDIR:$BASEPATH" "$SHIMDIR/claude" x >/dev/null 2>&1); rc=$?
if [ "$rc" -eq 7 ]; then ok; else bad "7c exit code passes through swarmery" "7" "$rc"; fi

# ── 8. fail open: no swarmery -> the real binary, argv and exit code intact ──
: >"$ARGV_LOG"
PATH="$SHIMDIR:$BASEPATH" "$SHIMDIR/claude" --version extra-arg >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 7 ] && [ "$(cat "$ARGV_LOG")" = "[--version][extra-arg]" ]; then ok
else bad "8 swarmery absent -> exec real; argv + exit code passed through" "7, '[--version][extra-arg]'" "$rc, '$(cat "$ARGV_LOG")'"; fi
printf '#!/bin/sh\nexit 0\n' >"$SWDIR/swarmery-noexec"; chmod 0644 "$SWDIR/swarmery-noexec"
NOEXEC="$W/noexec"; mkdir -p "$NOEXEC"; cp "$SWDIR/swarmery-noexec" "$NOEXEC/swarmery"; chmod 0644 "$NOEXEC/swarmery"
: >"$ARGV_LOG"
PATH="$SHIMDIR:$NOEXEC:$BASEPATH" "$SHIMDIR/claude" a b >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 7 ] && [ "$(cat "$ARGV_LOG")" = "[a][b]" ]; then ok
else bad "8b non-executable swarmery -> fail open" "7, '[a][b]'" "$rc, '$(cat "$ARGV_LOG")'"; fi
# an argument holding a space and an empty argument survive both routes intact
: >"$ARGV_LOG"
(cd "$PA" && PATH="$SHIMDIR:$SWDIR:$BASEPATH" "$SHIMDIR/claude" "a b" "" c >/dev/null 2>&1); rc=$?
sw_last="$(tail -1 "$SW_ARGV_LOG")"
if [ "$rc" -eq 7 ] && [ "$(cat "$ARGV_LOG")" = "[a b][][c]" ] &&
  [ "$sw_last" = "[account][exec][--][$REALDIR/claude][a b][][c]" ]; then ok
else bad "8c via swarmery: 'a b', '' and c preserved exactly" "[a b][][c]" "rc $rc, claude='$(cat "$ARGV_LOG")', swarmery='$sw_last'"; fi
: >"$ARGV_LOG"
PATH="$SHIMDIR:$BASEPATH" "$SHIMDIR/claude" "a b" "" c >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 7 ] && [ "$(cat "$ARGV_LOG")" = "[a b][][c]" ]; then ok
else bad "8d fail-open: 'a b', '' and c preserved exactly" "[a b][][c]" "rc $rc, '$(cat "$ARGV_LOG")'"; fi

# ── 9. the recorded binary is gone: re-probe PATH minus the shim dir ─────────
REAL2="$W/real2"; mkdir -p "$REAL2"; cp -p "$REALDIR/claude" "$REAL2/claude"
mv "$REALDIR/claude" "$W/claude.moved"
: >"$ARGV_LOG"
PATH="$SHIMDIR:$REAL2:$BASEPATH" "$SHIMDIR/claude" moved >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 7 ] && [ "$(cat "$ARGV_LOG")" = "[moved]" ]; then ok
else bad "9 moved binary -> next real claude on PATH" "7, moved" "$rc"; fi
ERR="$(PATH="$SHIMDIR:$BASEPATH" "$SHIMDIR/claude" x 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -eq 127 ] && [ "$(printf '%s\n' "$ERR" | grep -c '^')" = "1" ]; then ok
else bad "9b no real claude anywhere -> one stderr line, exit 127" "127, 1 line" "$rc, '$ERR'"; fi
mv "$W/claude.moved" "$REALDIR/claude"

# ── 10. the installer's candidate list matches claudebin.go ──────────────────
go_list="$(grep -oE '"/opt/homebrew/bin"|"/usr/local/bin"|home, "\.claude", "local"|home, "\.local", "bin"|home, "\.npm-global", "bin"|home, "bin", "claude"' "$CLAUDEBIN_GO" |
  sed -e 's/"//g' -e 's/home, //' -e 's/, /\//g' -e 's#/claude$##' | tr '\n' ' ')"
sh_list="$(grep -oE 'SWARMERY_CLAUDE_PROBE_DIRS-[^}]*' "$INSTALLER" | sed 's/SWARMERY_CLAUDE_PROBE_DIRS-//' | tr ':' ' ') $(grep -oE '"\$HOME/\.claude/local" "\$HOME/\.local/bin" "\$HOME/\.npm-global/bin" "\$HOME/bin"' "$INSTALLER" | sed -e 's/"//g' -e 's/\$HOME\///g')"
if [ "$(printf '%s' "$go_list" | tr -s ' ' | sed 's/ $//')" = "$(printf '%s' "$sh_list" | tr -s ' ' | sed 's/ $//')" ] && [ -n "$go_list" ]; then ok
else bad "10 installer probe list == claudebin.go's" "$go_list" "$sh_list"; fi

# ── 11. status, then uninstall leaves a working terminal ─────────────────────
ST="$(inst "$REALDIR:$BASEPATH" --status --profile "$RC")"
if printf '%s' "$ST" | grep -qF "shim: installed at $SHIMDIR/claude → $REALDIR/claude"; then ok
else bad "11 --status reports the shim and its target" "shim: installed …" "$ST"; fi
inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$RC" >/dev/null 2>&1; rc=$?
fresh="$(env -i HOME="$HOME" PATH="$SHIMDIR:$REALDIR:$BASEPATH" "$BASH_BIN" --norc --noprofile -c 'command -v claude')"
if [ "$rc" -eq 0 ] && [ ! -e "$SHIMDIR/claude" ] && [ "$fresh" = "$REALDIR/claude" ] &&
  ! grep -qF "$SHIMDIR" "$RC" && cmp -s "$RC" "$RC.bak"; then ok
else bad "11b --shim-uninstall removes the shim and its PATH line; claude still resolves" "gone, $REALDIR/claude, rc == .bak" "rc $rc, fresh='$fresh', $(cat "$RC")"; fi
ST="$(inst "$REALDIR:$BASEPATH" --status --profile "$RC")"
if printf '%s' "$ST" | grep -qF 'shim: not installed'; then ok
else bad "11c --status after uninstall" "shim: not installed" "$ST"; fi
inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$RC4" >/dev/null 2>&1
if grep -qFx 'claude() {' "$RC4" && ! grep -qF "$SHIMDIR" "$RC4"; then ok
else bad "11d --shim-uninstall keeps the function block" "function kept, export gone" "$(cat "$RC4")"; fi

# ── 12. a lone begin marker: refuse BEFORE writing anything ──────────────────
RCB="$W/rc-broken"
# shellcheck disable=SC2016  # $PATH is for the profile to expand
printf '# top\n# >>> swarmery accounts-pack >>>\nexport PATH="%s:$PATH"\n# operator tail line\n' "$SHIMDIR" >"$RCB"
cp -p "$RCB" "$W/rc-broken.orig"
OUT="$(inst "$REALDIR:$BASEPATH" --shim --profile "$RCB" 2>&1)"; rc=$?
if [ "$rc" -ne 0 ] && cmp -s "$RCB" "$W/rc-broken.orig" && [ ! -e "$RCB.bak" ] &&
  [ ! -e "$SHIMDIR/claude" ] && ! printf '%s' "$OUT" | grep -q 'shim installed'; then ok
else bad "12 --shim on a lone begin marker: non-zero, profile byte-identical, no shim" "rc!=0, no diff" "rc $rc: $OUT; $(diff "$W/rc-broken.orig" "$RCB")"; fi
inst "$REALDIR:$BASEPATH" --shim --profile "$W/rc-ok" >/dev/null 2>&1
OUT="$(inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$RCB" 2>&1)"; rc=$?
if [ "$rc" -ne 0 ] && cmp -s "$RCB" "$W/rc-broken.orig" && [ ! -e "$RCB.bak" ] && [ -f "$SHIMDIR/claude" ]; then ok
else bad "12b --shim-uninstall on a lone begin marker: non-zero, profile byte-identical, shim kept" "rc!=0, no diff" "rc $rc: $OUT; $(diff "$W/rc-broken.orig" "$RCB")"; fi
inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$W/rc-ok" >/dev/null 2>&1

# ── 13. a `claude` in the shim dir that is not ours is never touched ────────
printf '#!/bin/sh\necho foreign\n' >"$SHIMDIR/claude"; chmod 0755 "$SHIMDIR/claude"
cp -p "$SHIMDIR/claude" "$W/foreign.orig"
ERR="$(inst "$REALDIR:$BASEPATH" --shim --profile "$W/rc-ok" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'not an accounts-pack shim' && cmp -s "$SHIMDIR/claude" "$W/foreign.orig"; then ok
else bad "13 --shim refuses to overwrite a foreign claude" "rc!=0, file unchanged" "rc $rc: $ERR"; fi
ERR="$(inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$W/rc-ok" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'not an accounts-pack shim' && cmp -s "$SHIMDIR/claude" "$W/foreign.orig"; then ok
else bad "13b --shim-uninstall refuses to remove a foreign claude" "rc!=0, file kept" "rc $rc: $ERR"; fi
rm -f "$SHIMDIR/claude"
ln -s "$REALDIR/claude" "$SHIMDIR/claude"
inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$W/rc-ok" >/dev/null 2>&1; rc=$?
if [ "$rc" -ne 0 ] && [ -L "$SHIMDIR/claude" ]; then ok
else bad "13c --shim-uninstall refuses to remove a symlink named claude" "rc!=0, link kept" "rc $rc"; fi
rm -f "$SHIMDIR/claude"

# ── 14. an unsafe shim dir is refused / not trusted; the temp file is safe ──
chmod g+w "$SHIMDIR"
ERR="$(inst "$REALDIR:$BASEPATH" --shim --profile "$W/rc-ok" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'writable by group/other' && [ ! -e "$SHIMDIR/claude" ]; then ok
else bad "14 --shim refuses a group-writable shim dir" "rc!=0, nothing written" "rc $rc: $ERR"; fi
chmod go-w "$SHIMDIR"
printf 'victim\n' >"$W/victim"
rm -f "$SHIMDIR/claude"   # force a real write below
ln -s "$W/victim" "$SHIMDIR/claude.new"
inst "$REALDIR:$BASEPATH" --shim --profile "$W/rc-ok" >/dev/null 2>&1; rc=$?
leftover="$(find "$SHIMDIR" -name '.claude.*' | wc -l | tr -d ' ')"
if [ "$rc" -eq 0 ] && [ "$(cat "$W/victim")" = "victim" ] && [ "$leftover" = "0" ] && [ -x "$SHIMDIR/claude" ]; then ok
else bad "14b the shim is written through a mktemp file, never a fixed .new symlink" "victim intact, no leftover" "rc $rc, victim='$(cat "$W/victim")', leftover $leftover"; fi
rm -f "$SHIMDIR/claude.new"
chmod o+w "$SHIMDIR"
: >"$SW_LOG"; : >"$ARGV_LOG"
ERR="$(cd "$PA" && PATH="$SHIMDIR:$SWDIR:$BASEPATH" "$SHIMDIR/claude" "x y" 2>&1 >/dev/null)"; rc=$?
if [ "$rc" -eq 7 ] && [ ! -s "$SW_LOG" ] && [ "$(cat "$ARGV_LOG")" = "[x y]" ] &&
  [ "$(printf '%s\n' "$ERR" | grep -c '^')" = "1" ]; then ok
else bad "14c other-writable shim dir -> real claude directly, swarmery never run" "7, no swarmery, [x y]" "rc $rc, sw='$(cat "$SW_LOG")', argv='$(cat "$ARGV_LOG")', err='$ERR'"; fi
chmod go-w "$SHIMDIR"
# the preflight hook trusts ~/.swarmery/bin/swarmery only in a safe dir
PFHOME="$W/pfhome"; mkdir -p "$PFHOME/.swarmery/bin"
PF_MARK="$W/pf.mark"; export PF_MARK
# shellcheck disable=SC2016  # $PF_MARK is for the stub to expand
printf '#!/bin/sh\ntouch "$PF_MARK"\nexit 1\n' >"$PFHOME/.swarmery/bin/swarmery"; chmod 0755 "$PFHOME/.swarmery/bin/swarmery"
PREFLIGHT="$ROOT/plugins/accounts-pack/hooks/preflight-account.sh"
JQ_BIN="$(command -v jq || true)"
if [ -n "$JQ_BIN" ]; then
  JQDIR="$W/jqdir"; mkdir -p "$JQDIR"; ln -s "$JQ_BIN" "$JQDIR/jq"
  pf_run() { printf '{"session_id":"s1","cwd":"%s"}' "$PA" | env HOME="$PFHOME" PATH="$JQDIR:/usr/bin:/bin" "$BASH_BIN" "$PREFLIGHT" >/dev/null 2>&1; }
  rm -f "$PF_MARK"; pf_run; rc=$?
  if [ "$rc" -eq 0 ] && [ -e "$PF_MARK" ]; then ok
  else bad "14d preflight runs the cached swarmery from a safe dir" "rc 0, ran" "rc $rc, ran=$([ -e "$PF_MARK" ] && echo y || echo n)"; fi
  chmod g+w "$PFHOME/.swarmery/bin"
  rm -f "$PF_MARK"; pf_run; rc=$?
  if [ "$rc" -eq 0 ] && [ ! -e "$PF_MARK" ]; then ok
  else bad "14e preflight ignores the cached swarmery in a group-writable dir" "rc 0, not run" "rc $rc, ran=$([ -e "$PF_MARK" ] && echo y || echo n)"; fi
  chmod go-w "$PFHOME/.swarmery/bin"
else
  printf '  - 14d/14e skipped: jq not installed\n'
fi

# ── 15. SWARMERY_BIN_DIR is validated before it reaches the profile ─────────
RCV="$W/rc-v"; printf '# v\n' >"$RCV"; cp -p "$RCV" "$W/rc-v.orig"
for bad_dir in "$W/bad\$(touch pwned)" "$W/bad\"q" "$W/bad:colon" "relative/bin"; do
  # run from inside the temp tree: a regression must not create relative/bin elsewhere
  ERR="$(cd "$W" && SWARMERY_BIN_DIR="$bad_dir" PATH="$REALDIR:$BASEPATH" "$BASH_BIN" "$INSTALLER" --shim --profile "$RCV" 2>&1 >/dev/null)"; rc=$?
  if [ "$rc" -ne 0 ] && printf '%s' "$ERR" | grep -q 'refus' && cmp -s "$RCV" "$W/rc-v.orig" && (cd "$W" && [ ! -e "$bad_dir" ]); then ok
  else bad "15 SWARMERY_BIN_DIR='$bad_dir' is refused" "rc!=0, profile unchanged" "rc $rc: $ERR"; fi
done

# ── 16. the shim's re-probe skips every other shim, not only its own dir ────
ALIASDIR="$W/alias"; mkdir -p "$ALIASDIR"; ln -s "$SHIMDIR/claude" "$ALIASDIR/claude"
SHIM2="$W/shim2"; mkdir -p "$SHIM2"; cp -p "$SHIMDIR/claude" "$SHIM2/claude"
REAL3="$W/real3"; mkdir -p "$REAL3"; cp -p "$REALDIR/claude" "$REAL3/claude"
mv "$REALDIR/claude" "$W/claude.moved"
: >"$ARGV_LOG"
# A regression loops shim -> shim through exec (one pid): a 10 s watchdog turns
# that into a failure instead of a hung suite.
PATH="$SHIMDIR:$ALIASDIR:$SHIM2:$REAL3:$BASEPATH" "$SHIMDIR/claude" loop-safe >/dev/null 2>&1 &
loop_pid=$!
(sleep 10; kill -9 "$loop_pid") </dev/null >/dev/null 2>&1 &
dog_pid=$!
wait "$loop_pid"; rc=$?
kill "$dog_pid" 2>/dev/null
if [ "$rc" -eq 7 ] && [ "$(cat "$ARGV_LOG")" = "[loop-safe]" ]; then ok
else bad "16 re-probe skips a symlink to the shim and a second shim dir" "7, [loop-safe]" "rc $rc, '$(cat "$ARGV_LOG")'"; fi
mv "$W/claude.moved" "$REALDIR/claude"
inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$W/rc-ok" >/dev/null 2>&1

# ── 17. success exits 0 even when no temp file was made ─────────────────────
# The EXIT trap is the last thing to run when a mode falls off the end of the
# script; with TMP never set, its status must not become the script's.
: >"$W/rc-none"
inst "$REALDIR:$BASEPATH" --status --profile "$W/rc-none" >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 0 ]; then ok
else bad "17 --status exits 0" "0" "$rc"; fi
inst "$REALDIR:$BASEPATH" --shim-uninstall --profile "$W/rc-none" >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 0 ]; then ok
else bad "17b --shim-uninstall with nothing to remove exits 0" "0" "$rc"; fi

printf 'accounts-shim: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
