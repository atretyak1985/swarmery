#!/bin/bash
# Behavioral tests for plugins/core/hooks/pre-commit-test-gate.sh — the
# lint pass over staged shell scripts and the gate's scratch files.
#
# A staged file name is outside data: it must reach shellcheck as one argv
# item, never as text a shell re-parses. Each case builds a throwaway git repo
# under mktemp, stages scripts with unusual names, stubs shellcheck on PATH
# with a shim that records its argv, and feeds the hook a `git commit` payload.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/pre-commit-test-gate.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

BIN="$TESTDIR/bin"
mkdir -p "$BIN"
cat > "$BIN/shellcheck" <<EOS
#!/bin/sh
for a in "\$@"; do printf '%s\n' "\$a" >> "$TESTDIR/shellcheck.argv"; done
exit 0
EOS
chmod +x "$BIN/shellcheck"

# make_repo <dir> — a fresh repo; prints its physical path (git reports the
# physical toplevel, and on macOS the mktemp dir sits behind a /var symlink).
make_repo() {
  mkdir -p "$1"
  git -C "$1" init -q
  git -C "$1" config user.email t@t.t
  git -C "$1" config user.name t
  (cd "$1" && pwd -P)
}

REPO=$(make_repo "$TESTDIR/repo")

# Names with shell metacharacters that survive xargs' own quote handling.
NAME1="a';touch PWNED;'.sh"
NAME2="b\"';touch PWNED2;'\".sh"
NAME3='plain.sh'
for n in "$NAME1" "$NAME2" "$NAME3"; do
  printf '#!/bin/sh\necho ok\n' > "$REPO/$n"
done
git -C "$REPO" add -A

# A private TMPDIR shows where the gate keeps its scratch output.
GATE_TMP="$TESTDIR/gate-tmp"
mkdir -p "$GATE_TMP"

payload='{"tool_input":{"command":"git commit -m test"}}'
out=$( cd "$REPO" && printf '%s' "$payload" | TMPDIR="$GATE_TMP" PATH="$BIN:$PATH" "$HOOK" 2>&1 )
rc=$?

if [ "$rc" -eq 0 ]; then ok; else bad "hook exited $rc on a clean shellcheck run: $out"; fi

# Nothing a file name says may run.
for f in "$REPO/PWNED" "$REPO/PWNED2" "$TESTDIR/PWNED" "$TESTDIR/PWNED2"; do
  if [ -e "$f" ]; then bad "a staged file name ran a command (found $f)"; else ok; fi
done

# The linter saw each existing name as one argv item, with its repo path.
if grep -qxF "$REPO/$NAME1" "$TESTDIR/shellcheck.argv" 2>/dev/null; then ok
else bad "shellcheck did not receive '$NAME1' as one argument: $(cat "$TESTDIR/shellcheck.argv" 2>/dev/null)"; fi
if grep -qxF "$REPO/$NAME3" "$TESTDIR/shellcheck.argv" 2>/dev/null; then ok
else bad "shellcheck did not receive '$NAME3': $(cat "$TESTDIR/shellcheck.argv" 2>/dev/null)"; fi

# The gate cleans up after itself: no scratch output left in TMPDIR or /tmp.
left=$(find "$GATE_TMP" -mindepth 1 2>/dev/null | head -5)
if [ -z "$left" ]; then ok; else bad "the gate left scratch files behind: $left"; fi

# The repo's own path is outside data too: a checkout directory whose name
# carries quotes and separators must not change what the gate runs.
QREPO=$(make_repo "$TESTDIR/q';touch PWNED3;'repo")
printf '#!/bin/sh\necho ok\n' > "$QREPO/ok.sh"
git -C "$QREPO" add -A
: > "$TESTDIR/shellcheck.argv"
out=$( cd "$QREPO" && printf '%s' "$payload" | TMPDIR="$GATE_TMP" PATH="$BIN:$PATH" "$HOOK" 2>&1 )
rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "hook exited $rc in a repo with a quoted path: $out"; fi
if [ -e "$QREPO/PWNED3" ] || [ -e "$TESTDIR/PWNED3" ]; then bad "the repo path ran a command"; else ok; fi
if grep -qxF "$QREPO/ok.sh" "$TESTDIR/shellcheck.argv" 2>/dev/null; then ok
else bad "shellcheck did not receive the script under a quoted repo path: $(cat "$TESTDIR/shellcheck.argv" 2>/dev/null)"; fi

# A staged deletion is skipped, not reported as a failure.
git -C "$REPO" commit -qm seed >/dev/null 2>&1
git -C "$REPO" rm -q "$NAME3"
: > "$TESTDIR/shellcheck.argv"
out=$( cd "$REPO" && printf '%s' "$payload" | TMPDIR="$GATE_TMP" PATH="$BIN:$PATH" "$HOOK" 2>&1 )
rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "a staged deletion failed the gate (rc=$rc): $out"; fi
if grep -qF "$NAME3" "$TESTDIR/shellcheck.argv" 2>/dev/null; then bad "shellcheck was run on a deleted file"; else ok; fi

printf 'pre-commit-test-gate: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
