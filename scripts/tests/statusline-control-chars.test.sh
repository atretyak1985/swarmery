#!/bin/bash
# Behavioral tests: the statusline prints outside text as text.
#
#   plugins/core/statusline/statusline.sh — the header, LOC and PWD lines.
#
# The session name, the weather cache, the git branch and the working
# directory's name all come from outside the statusline. Raw control bytes in
# them (ESC, BEL) must be stripped, and backslash sequences must not be
# interpreted — only the statusline's own colour codes may reach the terminal.
#
# Hermetic: a git shim on PATH supplies the branch (a real ref name cannot hold
# a control byte), HOME and TMPDIR are private, the weather cache is pre-warmed
# so nothing is fetched.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATUSLINE="$ROOT/plugins/core/statusline/statusline.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

ESC=$'\033'
BEL=$'\007'

BIN="$WORK/bin"; mkdir -p "$BIN"
# git shim: answers the five read-only calls the statusline makes.
cat > "$BIN/git" <<EOS
#!/bin/bash
[ "\${1:-}" = "-C" ] && shift 2
case "\$*" in
  "rev-parse --is-inside-work-tree") echo true ;;
  "rev-parse --abbrev-ref HEAD") printf 'feat${ESC}[2Jx${BEL}y\n' ;;
  "status --porcelain") ;;
  "log -1 --format=%ct") date +%s ;;
  rev-list*) echo 0 ;;
  *) exit 1 ;;
esac
EOS
chmod +x "$BIN/git"

CWD="$WORK/dir${ESC}]0;t${BEL}name"
mkdir -p "$CWD" "$WORK/home" "$WORK/tmp"
printf 'Town%s[5m|+1°C|Clear%s\n' "$ESC" "$BEL" > "$WORK/tmp/agents-statusline-wx-Lviv.txt"

stdin_json=$(jq -nc --arg cwd "$CWD" --arg name "name${ESC}[2J\\033[2Jend${BEL}" \
  '{model:{display_name:"Claude"},version:"2.1.0",session_name:$name,workspace:{current_dir:$cwd,project_dir:$cwd}}')

raw=$(printf '%s' "$stdin_json" | PATH="$BIN:$PATH" HOME="$WORK/home" TMPDIR="$WORK/tmp" \
  "$STATUSLINE" 2>/dev/null)
# Drop the statusline's own SGR colour codes; whatever ESC/BEL is left came
# from outside text.
plain=$(printf '%s' "$raw" | perl -pe 's/\e\[[0-9;]*m//g')

if [ -n "$raw" ]; then ok; else bad "statusline rendered nothing"; fi
if printf '%s' "$plain" | grep -q "$ESC"; then
  bad "an ESC byte from outside text reached the terminal: $(printf '%s' "$plain" | od -c | head -5)"
else ok; fi
if printf '%s' "$plain" | grep -q "$BEL"; then bad "a BEL byte from outside text reached the terminal"; else ok; fi

# A backslash sequence in outside text is printed literally, not interpreted.
case "$plain" in
  *'\033[2Jend'*) ok ;;
  *) bad "a backslash sequence in the session name was interpreted, not printed" ;;
esac
# The text around the stripped bytes survives.
case "$plain" in *'Branch feat[2Jxy'*) ok ;; *) bad "branch text lost: $(printf '%s' "$plain" | grep PWD)" ;; esac
case "$plain" in *'dir]0;tname'*) ok ;; *) bad "directory name lost: $(printf '%s' "$plain" | grep PWD)" ;; esac
case "$plain" in *'Town[5m'*'Clear'*) ok ;; *) bad "weather text lost: $(printf '%s' "$plain" | grep LOC)" ;; esac

printf 'statusline-control-chars: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
