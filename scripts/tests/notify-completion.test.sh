#!/bin/bash
# Behavioral tests for plugins/core/hooks/notify-completion.sh.
#
# The notification text carries outside data — the hook payload's message and
# the basename of the last file the session touched. It must reach the
# notifier as DATA (one argv item), never as part of the script source the
# notifier compiles. Both notifiers are stubbed on PATH with shims that record
# what they were handed, and OSTYPE is forced per case, so both branches run on
# any platform and nothing ever posts a real notification.
#
# The session file is sandboxed through SWARMERY_RUN_DIR, so the suite reads
# and writes nothing outside its own mktemp dir.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/notify-completion.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

BIN="$TESTDIR/bin"
mkdir -p "$BIN"
# Each shim writes one argv item per line, NUL-free, to its own record file.
for tool in osascript notify-send; do
  cat > "$BIN/$tool" <<EOS
#!/bin/sh
: > "$TESTDIR/$tool.argv"
for a in "\$@"; do printf '%s\n' "\$a" >> "$TESTDIR/$tool.argv"; done
printf '%s\n' "\$#" > "$TESTDIR/$tool.argc"
exit 0
EOS
  chmod +x "$BIN/$tool"
done

export SWARMERY_RUN_DIR="$TESTDIR/run"
unset SWARMERY_SESSION_FILE
mkdir -p "$SWARMERY_RUN_DIR"

HOSTILE='a" & (do shell script "touch PWNED") & ".txt'
today=$(date +%Y%m%d)
jq -nc --arg f "/work/$HOSTILE" '{ts:"t",tool:"Edit",file:$f,cmd:""}' \
  > "$SWARMERY_RUN_DIR/claude-session-${today}.jsonl"

# run <ostype> <payload-json> — run the hook with the shims first on PATH, from
# inside the sandbox so a stray side effect would land where we can see it.
run() {
  rm -f "$TESTDIR"/*.argv "$TESTDIR"/*.argc
  ( cd "$TESTDIR" && printf '%s' "$2" | OSTYPE="$1" PATH="$BIN:$PATH" "$HOOK" ) >/dev/null 2>&1
}

# script_of — the -e script lines handed to osascript (the compiled source).
script_of() {
  awk 'prev == "-e" { print } { prev = $0 }' "$TESTDIR/osascript.argv" 2>/dev/null
}

# ── macOS: last-file basename stays out of the script source ──────
run darwin '{"notification_type":"task_complete"}'
if [ ! -f "$TESTDIR/osascript.argv" ]; then
  bad "darwin task_complete: osascript was not called"
else
  src=$(script_of)
  case "$src" in
    *PWNED*|*'do shell script'*) bad "darwin: the file name leaked into the AppleScript source: $src" ;;
    *) ok ;;
  esac
  if grep -qxF "Task completed (1 tool calls) — last: $HOSTILE" "$TESTDIR/osascript.argv"; then ok
  else bad "darwin: the notification text did not arrive as one argv item: $(cat "$TESTDIR/osascript.argv")"; fi
fi

# ── macOS: the payload message stays out of the script source ─────
msg_payload=$(jq -nc --arg m "$HOSTILE" '{notification_type:"other",message:$m}')
run darwin "$msg_payload"
if [ ! -f "$TESTDIR/osascript.argv" ]; then
  bad "darwin message: osascript was not called"
else
  src=$(script_of)
  case "$src" in
    *PWNED*|*'do shell script'*) bad "darwin: the payload message leaked into the AppleScript source: $src" ;;
    *) ok ;;
  esac
  if grep -qF "$HOSTILE (1 tool calls)" "$TESTDIR/osascript.argv"; then ok
  else bad "darwin: the payload message did not arrive as an argv item: $(cat "$TESTDIR/osascript.argv")"; fi
fi

# ── Linux: notify-send already takes argv; keep it that way ───────
run linux-gnu "$msg_payload"
if grep -qF "$HOSTILE (1 tool calls)" "$TESTDIR/notify-send.argv" 2>/dev/null; then ok
else bad "linux: notify-send did not receive the message as an argv item"; fi

# Nothing in the sandbox (or the cwd the hook ran in) was created by the text.
if [ -e "$TESTDIR/PWNED" ]; then bad "a notification text created a file"; else ok; fi

printf 'notify-completion: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
