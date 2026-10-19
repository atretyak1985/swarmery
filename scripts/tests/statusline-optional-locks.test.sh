#!/bin/bash
# Regression test: every git the statusline runs has optional locks OFF.
#   plugins/core/statusline/statusline.sh — the "git (against cwd)" block.
#
# WHY THIS EXISTS. `git status` is a read, yet by default it takes index.lock to
# write back its refreshed stat cache. The statusline re-renders on every
# prompt, and a render killed mid-`git status` leaves that 0-byte lock behind
# with no holder; the operator's next `git commit` in the primary checkout then
# fails with "index.lock exists". Observed twice on 2026-10-19.
# GIT_OPTIONAL_LOCKS=0 disables only that opportunistic write.
#
# How: a fake `git` first on PATH records $GIT_OPTIONAL_LOCKS and its argv for
# every call, then execs the real git, so the statusline still sees a real repo
# and runs its whole git block. The test asserts that (a) the fake was actually
# reached, including for `status`, and (b) every recorded call saw exactly 0.
#
# Framework-free, offline and hermetic: HOME and TMPDIR are fresh temp dirs and
# the cwd is a throwaway repo, so no real checkout is ever touched.
# Run locally with `bash scripts/tests/statusline-optional-locks.test.sh`; CI
# discovers scripts/tests/*.test.sh.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATUSLINE="$ROOT/plugins/core/statusline/statusline.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

REAL_GIT="$(command -v git)"
if [ -z "$REAL_GIT" ]; then
	echo "statusline-optional-locks: git not found — cannot run" >&2
	exit 1
fi

# ── a throwaway repo with one commit, built with the REAL git ──────────────
REPO="$WORK/repo"
mkdir -p "$REPO"
"$REAL_GIT" -C "$REPO" init -q
printf 'x\n' >"$REPO/f.txt"
"$REAL_GIT" -C "$REPO" add f.txt
"$REAL_GIT" -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q -m init
printf 'y\n' >"$REPO/dirty.txt" # one untracked file, so status has work to do

# ── the fake git: log, then hand off to the real one ───────────────────────
BIN="$WORK/bin"
LOG="$WORK/git-calls.log"
mkdir -p "$BIN"
cat >"$BIN/git" <<EOF
#!/bin/bash
printf '%s|%s\n' "\${GIT_OPTIONAL_LOCKS-<unset>}" "\$*" >>"$LOG"
exec "$REAL_GIT" "\$@"
EOF
chmod +x "$BIN/git"

FAKE_HOME="$WORK/home"
mkdir -p "$FAKE_HOME" "$WORK/tmp"
stdin_json="$(printf '{"model":{"display_name":"Claude"},"workspace":{"current_dir":"%s","project_dir":"%s"}}' "$REPO" "$REPO")"
(
	unset GIT_OPTIONAL_LOCKS CLAUDE_CONFIG_DIR SWARMERY_USAGE_OAUTH SWARMERY_STATUSLINE_FABLE
	printf '%s' "$stdin_json" |
		PATH="$BIN:$PATH" HOME="$FAKE_HOME" TMPDIR="$WORK/tmp" bash "$STATUSLINE" >/dev/null 2>&1
)

# ── (a) the fake was reached, for status in particular ─────────────────────
if [ -s "$LOG" ] && grep -q '|.* status' "$LOG"; then
	ok
else
	bad "(a) the statusline ran git status through the fake (log: $(tr '\n' ';' <"$LOG" 2>/dev/null))"
fi

# ── (b) every call saw GIT_OPTIONAL_LOCKS=0 ────────────────────────────────
offenders="$(grep -v '^0|' "$LOG" 2>/dev/null)"
if [ -s "$LOG" ] && [ -z "$offenders" ]; then
	ok
else
	bad "(b) every git call has GIT_OPTIONAL_LOCKS=0; offenders:
$(printf '%s\n' "$offenders" | sed 's/^/       /')"
fi

printf 'statusline-optional-locks: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
